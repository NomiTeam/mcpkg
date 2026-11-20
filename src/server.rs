//! Server layout and lock-file persistence. All writers share the original CLI's directory lock.
use crate::model::{Lock, Target, valid_name};
use anyhow::{Context, Result, ensure};
use serde::Deserialize;
use std::{
    collections::BTreeSet,
    fs,
    io::{self, Write},
    path::{Path, PathBuf},
};
use tempfile::NamedTempFile;

pub struct Guard(PathBuf);
impl Drop for Guard {
    fn drop(&mut self) {
        let _ = fs::remove_dir(&self.0);
    }
}
pub fn guard(dir: &Path) -> Result<Guard> {
    let path = dir.join(".mcpkg-lock");
    fs::create_dir(&path).with_context(|| format!("lock plugin directory {}", dir.display()))?;
    Ok(Guard(path))
}
pub fn load(root: &Path) -> Result<Lock> {
    let path = root.join("mcpkg.lock");
    let f = match fs::File::open(&path) {
        Ok(f) => f,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Lock::new()),
        Err(e) => return Err(e.into()),
    };
    let lock: Lock = serde_json::from_reader(f).context("read mcpkg.lock")?;
    let mut jars = BTreeSet::new();
    for (key, e) in &lock {
        ensure!(
            *key == e.source.key()
                && valid_name(&e.jar)
                && e.jar.ends_with(".jar")
                && !e.name.is_empty()
                && jars.insert(e.jar.to_lowercase()),
            "invalid lock entry {key}"
        )
    }
    Ok(lock)
}
pub fn save(root: &Path, lock: &Lock) -> Result<()> {
    let mut f = NamedTempFile::new_in(root)?;
    serde_json::to_writer_pretty(&mut f, lock)?;
    writeln!(f)?;
    f.as_file().sync_all()?;
    f.persist(root.join("mcpkg.lock"))?;
    Ok(())
}
#[derive(Default, Deserialize, serde::Serialize)]
#[serde(default, deny_unknown_fields)]
struct Config {
    minecraft: String,
    loader: String,
}
pub fn target(root: &Path, loader: Option<String>, version: Option<String>) -> Result<Target> {
    let config = match fs::read(root.join("mcpkg.yml")) {
        Ok(b) => serde_yaml::from_slice(&b).context("read mcpkg.yml")?,
        Err(e) if e.kind() == io::ErrorKind::NotFound => Config::default(),
        Err(e) => return Err(e.into()),
    };
    Ok(Target {
        minecraft: version.unwrap_or(config.minecraft),
        loader: loader.unwrap_or_else(|| {
            if config.loader.is_empty() {
                "paper".into()
            } else {
                config.loader
            }
        }),
    })
}
/// Re-running init fills missing directories and never overwrites the operator's settings.
pub fn init(root: &Path, t: &Target) -> Result<()> {
    for path in ["plugins", "sources.d"] {
        fs::create_dir_all(root.join(path))?
    }
    let mut temp = NamedTempFile::new_in(root)?;
    serde_yaml::to_writer(
        &mut temp,
        &Config {
            minecraft: t.minecraft.clone(),
            loader: t.loader.clone(),
        },
    )?;
    if let Err(e) = temp.persist_noclobber(root.join("mcpkg.yml"))
        && e.error.kind() != io::ErrorKind::AlreadyExists
    {
        return Err(e.error.into());
    }
    println!("Initialized server directory {}", root.display());
    Ok(())
}
