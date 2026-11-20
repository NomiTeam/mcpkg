//! Plan an entire dependency change before moving any installed JAR.
use crate::{
    artifact,
    catalog::{Catalog, Release},
    inventory::{self, Plugin},
    model::{self, Installed, Lock, Reference, Target},
    server,
};
use anyhow::{Context, Result, bail, ensure};
use rayon::prelude::*;
use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    path::{Path, PathBuf},
};
use tempfile::TempDir;

pub fn updates(c: &Catalog, lock: &Lock, name: Option<&str>) -> Result<Vec<(String, Release)>> {
    let key = name.map(|s| model::lookup(lock, s)).transpose()?;
    lock.par_iter()
        .filter(|(k, e)| key.as_ref().is_none_or(|s| s == *k) && e.tracks_updates())
        .map(|(k, e)| Ok((k.clone(), c.resolve(&e.source, &e.target)?)))
        .collect()
}
pub fn changed(e: &Installed, r: &Release) -> bool {
    e.id != r.id || e.recipe_hash != r.recipe_hash
}

pub struct Plan<'a> {
    catalog: &'a Catalog,
    dir: PathBuf,
    stage: TempDir,
    files: Vec<Plugin>,
    before: BTreeSet<(String, String)>,
    lock: Lock,
    moves: Vec<(PathBuf, PathBuf)>,
    seen: BTreeMap<String, String>,
    visiting: BTreeSet<String>,
    changed_plugins: BTreeSet<String>,
}
impl<'a> Plan<'a> {
    pub fn new(c: &'a Catalog, lock: Lock) -> Result<Self> {
        let dir = c.root.join("plugins");
        let files = inventory::scan(&dir, true)?;
        let before = inventory::missing(&files, &lock);
        let stage = TempDir::new_in(&dir)?;
        Ok(Self {
            catalog: c,
            dir,
            stage,
            files,
            before,
            lock,
            moves: Vec::new(),
            seen: BTreeMap::new(),
            visiting: BTreeSet::new(),
            changed_plugins: BTreeSet::new(),
        })
    }
    pub fn install(&mut self, arg: &str, t: &Target) -> Result<()> {
        if let Some(path) = arg.strip_prefix("file:") {
            let name = inventory::descriptor(Path::new(path))?.name;
            ensure!(model::valid_name(&name), "invalid plugin name");
            self.stage(
                Reference::new("local", &name),
                t,
                Release::default(),
                Some(Path::new(path)),
            )
        } else {
            let reference = self.catalog.reference(arg)?;
            let release = self.catalog.resolve(&reference, t)?;
            self.stage(reference, t, release, None)
        }
    }
    pub fn upgrade(&mut self, key: &str, r: Release) -> Result<()> {
        let entry = self.lock[key].clone();
        self.stage(entry.source, &entry.target, r, None)
    }
    /// Resolve dependency aliases against the proposed lock, including packages staged earlier
    /// in this transaction. The live inventory stays untouched until validation succeeds.
    fn stage(
        &mut self,
        mut reference: Reference,
        t: &Target,
        r: Release,
        local: Option<&Path>,
    ) -> Result<()> {
        let mut key = model::canonical(&self.lock, &reference);
        if !r.project.is_empty() {
            key = model::canonical(&self.lock, &Reference::new("modrinth", &r.project));
            if !self.lock.contains_key(&key) {
                key = reference.key()
            }
        }
        if let Some(id) = self.seen.get(&key) {
            ensure!(*id == r.id, "conflicting versions of {key}");
            return Ok(());
        }
        self.seen.insert(key.clone(), r.id.clone());
        self.visiting.insert(key.clone());
        if let Some(old) = self.lock.get(&key) {
            reference.project = old.source.project.clone();
        }
        let path = self.stage.path().join(format!("new-{}", self.seen.len()));
        if let Some(local) = local {
            fs::copy(local, &path)?;
        } else {
            self.catalog.materialize(&r, &path)?
        }
        let meta = inventory::descriptor(&path).with_context(|| format!("inspect {key}"))?;
        let base = self
            .lock
            .get(&key)
            .map(|e| e.jar.clone())
            .unwrap_or_else(|| {
                format!(
                    "mcpkg-{}-{}.jar",
                    reference.provider,
                    reference.project.to_ascii_lowercase().replace('/', "_")
                )
            });
        let (filename, enabled) = self.destination(&key, &base, &meta.name)?;
        let version = if r.build.is_some() {
            format!("{} ({})", meta.version, r.version)
        } else if local.is_some() {
            meta.version.clone()
        } else {
            r.version
        };
        self.changed_plugins.insert(meta.name.clone());
        let checksum = artifact::checksum(&path)?;
        self.lock.insert(
            key.clone(),
            Installed {
                source: reference,
                target: t.clone(),
                project_id: r.project,
                id: if local.is_some() {
                    checksum.clone()
                } else {
                    r.id
                },
                recipe_hash: r.recipe_hash,
                version,
                available: String::new(),
                jar: base.clone(),
                name: meta.name.clone(),
                sha256: checksum,
                requires: Some(r.deps.clone()),
            },
        );
        self.files.push(Plugin {
            file: filename.clone(),
            base,
            enabled,
            meta,
            error: None,
        });
        self.moves.push((path, self.dir.join(filename)));
        for dep in r.deps {
            let key = model::canonical(&self.lock, &dep);
            ensure!(
                !self.visiting.contains(&key),
                "cyclic dependency involving {key}"
            );
            if let Some(e) = self.lock.get(&key)
                && (dep.selector.is_empty() || dep.selector == e.id)
                && self.files.iter().any(|p| p.enabled && p.base == e.jar)
            {
                continue;
            }
            let release = self.catalog.resolve(&dep, t)?;
            self.stage(dep, t, release, None)?;
        }
        self.visiting.remove(&key);
        Ok(())
    }
    /// A managed replacement inherits its current suffix. A collision with a manually
    /// installed file is an operator decision and must leave that file intact.
    fn destination(&mut self, key: &str, base: &str, name: &str) -> Result<(String, bool)> {
        ensure!(model::valid_name(base), "invalid managed filename");
        ensure!(
            !self
                .files
                .iter()
                .any(|p| p.base != base && p.meta.name.eq_ignore_ascii_case(name)),
            "{} is already installed under another filename",
            name
        );
        if let Some(old) = self.lock.get(key) {
            ensure!(
                old.name.eq_ignore_ascii_case(name),
                "plugin name changed for {key}"
            );
            ensure!(
                self.files
                    .iter()
                    .filter(|p| p.base.eq_ignore_ascii_case(base))
                    .count()
                    == 1,
                "expected one installed JAR for {key}"
            );
        }
        let mut filename = base.to_owned();
        let mut enabled = true;
        if let Some(existing) = self
            .files
            .iter()
            .position(|p| p.base.eq_ignore_ascii_case(base))
        {
            let old = self
                .lock
                .get(key)
                .context("refusing to replace unmanaged JAR")?;
            let plugin = self.files.remove(existing);
            self.backup(&plugin, old.sha256.clone())?;
            filename = plugin.file;
            enabled = plugin.enabled;
        }
        Ok((filename, enabled))
    }
    fn backup(&mut self, p: &Plugin, digest: String) -> Result<()> {
        let path = self.dir.join(&p.file);
        ensure!(
            artifact::checksum(&path)? == digest,
            "{} was modified since installation",
            p.file
        );
        self.moves.push((
            path,
            self.stage
                .path()
                .join(format!("backup-{}", self.moves.len())),
        ));
        Ok(())
    }
    pub fn remove(&mut self, name: &str) -> Result<()> {
        let key = model::lookup(&self.lock, name)?;
        let entry = self.lock.remove(&key).unwrap();
        ensure!(
            self.files
                .iter()
                .filter(|p| p.base.eq_ignore_ascii_case(&entry.jar))
                .count()
                == 1,
            "expected one installed JAR for {key}"
        );
        let i = self
            .files
            .iter()
            .position(|p| p.base == entry.jar)
            .context("managed JAR missing")?;
        let plugin = self.files.remove(i);
        self.backup(&plugin, entry.sha256)
    }
    pub fn toggle(&mut self, name: &str, enable: bool) -> Result<()> {
        let base = model::lookup(&self.lock, name)
            .ok()
            .map(|k| &self.lock[&k].jar);
        let mut matching = self.files.iter_mut().filter(|p| {
            base == Some(&p.base)
                || [&p.file, &p.base, &p.meta.name]
                    .iter()
                    .any(|v| v.eq_ignore_ascii_case(name))
                || p.base.trim_end_matches(".jar").eq_ignore_ascii_case(name)
        });
        let plugin = matching.next().context("plugin not found")?;
        ensure!(matching.next().is_none(), "ambiguous plugin name");
        if plugin.enabled != enable {
            self.changed_plugins.insert(plugin.meta.name.clone());
            if let Some(error) = &plugin.error {
                bail!("inspect {}: {error}", plugin.file)
            }
            let dest = if enable {
                plugin.base.clone()
            } else {
                format!("{}.disabled", plugin.base)
            };
            self.moves
                .push((self.dir.join(&plugin.file), self.dir.join(&dest)));
            plugin.file = dest;
            plugin.enabled = enable;
        }
        Ok(())
    }
    pub fn apply(self) -> Result<()> {
        // Existing unrelated dependency problems should not block a repair.
        let missing = inventory::missing(&self.files, &self.lock);
        if let Some((plugin, dep)) = missing
            .iter()
            .find(|pair| self.changed_plugins.contains(&pair.0) || !self.before.contains(*pair))
        {
            bail!("{plugin} requires {dep}")
        }
        // Journal completed renames in order. A failure publishing the lock also unwinds the JARs.
        let mut applied = 0;
        let result = (|| -> Result<()> {
            for (src, dest) in &self.moves {
                match fs::symlink_metadata(dest) {
                    Ok(_) => bail!("destination exists: {}", dest.display()),
                    Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                    Err(e) => return Err(e.into()),
                }
                fs::rename(src, dest)?;
                applied += 1
            }
            server::save(&self.catalog.root, &self.lock)
        })();
        if let Err(err) = result {
            for (src, dest) in self.moves[..applied].iter().rev() {
                if let Err(e) = fs::rename(dest, src) {
                    let recovery = self.stage.keep();
                    bail!(
                        "{err}; rollback failed: {e}; backups: {}",
                        recovery.display()
                    )
                }
            }
            return Err(err);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn reverses_completed_renames_when_next_destination_exists() {
        let temp = tempfile::tempdir().unwrap();
        let catalog = Catalog {
            root: temp.path().into(),
            client: Default::default(),
        };
        let dir = catalog.root.join("plugins");
        fs::create_dir(&dir).unwrap();
        let mut plan = Plan::new(&catalog, Lock::new()).unwrap();
        let original = dir.join("original.jar");
        let occupied = dir.join("occupied.jar");
        fs::write(&original, b"old").unwrap();
        fs::write(&occupied, b"keep").unwrap();
        let staged = plan.stage.path().join("new");
        fs::write(&staged, b"new").unwrap();
        plan.moves = vec![
            (original.clone(), plan.stage.path().join("backup")),
            (staged, original.clone()),
            (dir.join("missing"), occupied.clone()),
        ];
        assert!(plan.apply().is_err());
        assert_eq!(fs::read(original).unwrap(), b"old");
        assert_eq!(fs::read(occupied).unwrap(), b"keep");
        assert!(!catalog.root.join("mcpkg.lock").exists());
    }
}
