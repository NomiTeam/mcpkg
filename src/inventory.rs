//! Read plugin descriptors without extracting JARs or executing server code.
use crate::model::{Lock, canonical};
use anyhow::{Context, Result, ensure};
use rayon::prelude::*;
use serde::Deserialize;
use std::{
    collections::{BTreeMap, BTreeSet},
    fs::{self, File},
    io::Read,
    path::Path,
};

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Descriptor {
    pub name: String,
    pub version: String,
    pub provides: Vec<String>,
    pub depend: Vec<String>,
    dependencies: BTreeMap<String, BTreeMap<String, Rule>>,
}
#[derive(Clone, Debug, Default, Deserialize)]
struct Rule {
    required: Option<bool>,
}

pub fn descriptor(path: &Path) -> Result<Descriptor> {
    // Stream the central directory. Large shaded plugins commonly contain thousands of
    // class entries, but only one descriptor needs decompression or allocation.
    let mut buffer = vec![0; rawzip::RECOMMENDED_BUFFER_SIZE];
    let archive = rawzip::ZipArchive::from_file(File::open(path)?, &mut buffer)?;
    let mut entries = archive.entries(&mut buffer);
    let mut selected = None;
    while let Some(entry) = entries.next_entry()? {
        let paper = entry.file_path().as_ref() == b"paper-plugin.yml";
        if paper || (selected.is_none() && entry.file_path().as_ref() == b"plugin.yml") {
            selected = Some((entry.wayfinder(), entry.compression_method()));
            if paper {
                break;
            }
        }
    }
    let (position, method) = selected.context("missing plugin.yml or paper-plugin.yml")?;
    let entry = archive.get_entry(position)?;
    let reader: Box<dyn Read> = match method {
        rawzip::CompressionMethod::STORE => Box::new(entry.reader()),
        rawzip::CompressionMethod::DEFLATE => {
            Box::new(flate2::read::DeflateDecoder::new(entry.reader()))
        }
        _ => anyhow::bail!("unsupported descriptor compression {method}"),
    };
    let mut bytes = Vec::new();
    entry
        .verifying_reader(reader)
        .take((1 << 20) + 1)
        .read_to_end(&mut bytes)?;
    ensure!(bytes.len() <= 1 << 20, "descriptor exceeds 1 MiB");
    // Bukkit consumes the first document even when the plugin appends a YAML separator.
    let document = serde_yaml::Deserializer::from_slice(&bytes)
        .next()
        .context("empty descriptor")?;
    let meta = Descriptor::deserialize(document)?;
    ensure!(!meta.name.trim().is_empty(), "missing plugin name");
    Ok(meta)
}
#[derive(Clone, Debug)]
pub struct Plugin {
    pub file: String,
    pub base: String,
    pub enabled: bool,
    pub meta: Descriptor,
    pub error: Option<String>,
}

/// Listing needs filenames only. Dependency checks read independent ZIP directories in parallel.
pub fn scan(dir: &Path, inspect: bool) -> Result<Vec<Plugin>> {
    let mut files = Vec::new();
    for entry in fs::read_dir(dir).with_context(|| format!("read {}", dir.display()))? {
        let entry = entry?;
        if !entry.file_type()?.is_file() {
            continue;
        }
        let file = entry.file_name().to_string_lossy().into_owned();
        let base = file
            .strip_suffix(".disabled")
            .or_else(|| file.strip_suffix(".off"))
            .unwrap_or(&file);
        if base.ends_with(".jar") {
            files.push(Plugin {
                base: base.into(),
                enabled: base == file,
                file,
                meta: Descriptor::default(),
                error: None,
            });
        }
    }
    files.sort_by(|a, b| a.file.cmp(&b.file));
    if inspect {
        files
            .par_iter_mut()
            .for_each(|p| match descriptor(&dir.join(&p.file)) {
                Ok(meta) => p.meta = meta,
                Err(e) => p.error = Some(e.to_string()),
            });
    }
    Ok(files)
}

/// A plugin may satisfy requirements under its declared name or any provided alias.
pub fn missing(files: &[Plugin], lock: &Lock) -> BTreeSet<(String, String)> {
    let mut provided = BTreeSet::new();
    for p in files.iter().filter(|p| p.enabled) {
        provided.insert(p.meta.name.to_lowercase());
        provided.extend(p.meta.provides.iter().map(|n| n.to_lowercase()));
    }
    let by_jar: BTreeMap<_, _> = lock.values().map(|e| (&e.jar, e)).collect();
    let mut missing = BTreeSet::new();
    for p in files.iter().filter(|p| p.enabled) {
        let mut names = p.meta.depend.clone();
        for phase in p.meta.dependencies.values() {
            names.extend(
                phase
                    .iter()
                    .filter(|(_, r)| r.required.unwrap_or(true))
                    .map(|(n, _)| n.clone()),
            );
        }
        if let Some(entry) = by_jar.get(&p.base) {
            for r in entry.requires.iter().flatten() {
                let key = canonical(lock, r);
                match lock.get(&key) {
                    Some(dep)
                        if r.selector.is_empty()
                            || dep.id == r.selector
                            || dep.version == r.selector =>
                    {
                        names.push(dep.name.clone())
                    }
                    Some(_) => {
                        missing.insert((p.meta.name.clone(), format!("{key}@{}", r.selector)));
                    }
                    None => {
                        missing.insert((p.meta.name.clone(), key));
                    }
                }
            }
        }
        for name in names {
            if !provided.contains(&name.to_lowercase()) {
                missing.insert((p.meta.name.clone(), name));
            }
        }
    }
    missing
}
