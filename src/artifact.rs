//! Transfer and build JARs in disposable directories before publication.
use anyhow::{Context, Result, bail, ensure};
use reqwest::blocking::Client;
use serde::Deserialize;
use sha2::{Digest, Sha256, Sha512};
use std::{
    fs::{self, File},
    io::{self, Read, Write},
    path::{Component, Path},
    process::Command,
};
use tempfile::{NamedTempFile, tempdir};

pub fn checksum(path: &Path) -> Result<String> {
    let mut hash = Sha256::new();
    io::copy(&mut File::open(path)?, &mut hash)?;
    Ok(format!("{:x}", hash.finalize()))
}
fn verify(path: &Path, digest: &str) -> Result<()> {
    let actual = if digest.len() == 128 {
        let mut hash = Sha512::new();
        io::copy(&mut File::open(path)?, &mut hash)?;
        format!("{:x}", hash.finalize())
    } else {
        checksum(path)?
    };
    ensure!(
        actual.eq_ignore_ascii_case(digest),
        "checksum mismatch for {}",
        path.display()
    );
    Ok(())
}
/// Limit the actual streamed body even if the server omits Content-Length.
fn transfer(client: &Client, url: &str, out: &mut impl Write) -> Result<()> {
    const LIMIT: u64 = 256 << 20;
    let body = client.get(url).send()?.error_for_status()?;
    let size = io::copy(&mut body.take(LIMIT + 1), out)?;
    ensure!((1..=LIMIT).contains(&size), "invalid download size: {size}");
    Ok(())
}
/// Only upstream content hashes identify cache entries. Each install gets a private copy.
pub fn fetch(client: &Client, url: &str, digest: &str, cache: &Path, out: &Path) -> Result<()> {
    if digest.is_empty() {
        transfer(client, url, &mut File::create(out)?)?;
        return Ok(());
    }
    ensure!(
        matches!(digest.len(), 64 | 128) && digest.bytes().all(|c| c.is_ascii_hexdigit()),
        "invalid upstream digest"
    );
    fs::create_dir_all(cache)?;
    let path = cache.join(digest);
    if path.try_exists()? {
        fs::copy(&path, out)?;
        if verify(out, digest).is_ok() {
            return Ok(());
        }
        // Repair a corrupt cache entry only after its replacement verifies.
    }
    let mut temp = NamedTempFile::new_in(cache)?;
    transfer(client, url, temp.as_file_mut())?;
    verify(temp.path(), digest)?;
    fs::copy(temp.path(), out)?;
    // Concurrent writers for this key have verified identical bytes. Atomic replacement
    // also lets an interrupted or manually corrupted cache entry recover on retry.
    temp.persist(path)?;
    Ok(())
}
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default, deny_unknown_fields, rename_all = "kebab-case")]
pub struct Recipe {
    pub command: Vec<String>,
    pub windows_command: Vec<String>,
    pub artifact: String,
}

pub fn run(dir: &Path, args: &[String]) -> Result<()> {
    let (exe, flags) = args.split_first().context("missing build command")?;
    ensure!(
        Command::new(exe)
            .args(flags)
            .current_dir(dir)
            .status()
            .with_context(|| format!("run {exe}"))?
            .success(),
        "{exe} failed"
    );
    Ok(())
}
pub fn commit(repo: &str, reference: &str) -> Result<String> {
    if matches!(reference.len(), 40 | 64) && reference.bytes().all(|c| c.is_ascii_hexdigit()) {
        return Ok(reference.to_ascii_lowercase());
    }
    let reference = if reference.is_empty() {
        "HEAD"
    } else {
        reference
    };
    let names = if reference == "HEAD" || reference.starts_with("refs/") {
        vec![reference.into(), format!("{reference}^{{}}")]
    } else {
        vec![
            format!("refs/heads/{reference}"),
            format!("refs/tags/{reference}"),
            format!("refs/tags/{reference}^{{}}"),
        ]
    };
    let output = Command::new("git")
        .args(["ls-remote", "--", repo])
        .args(&names)
        .output()?;
    ensure!(
        output.status.success(),
        "git ls-remote failed for {repo}: {}",
        String::from_utf8_lossy(&output.stderr).trim()
    );
    let refs = String::from_utf8(output.stdout)?;
    let mut commit = None;
    for name in names {
        if let Some(id) = refs
            .lines()
            .filter_map(|l| l.split_once('\t'))
            .find_map(|(id, n)| (n == name).then_some(id))
        {
            ensure!(
                commit.is_none_or(|old| old == id) || name.ends_with("^{}"),
                "ambiguous Git ref {reference}; specify refs/heads/ or refs/tags/"
            );
            commit = Some(id);
        }
    }
    if let Some(commit) = commit {
        return Ok(commit.into());
    }
    bail!("unknown Git ref {reference} in {repo}")
}
pub fn copy_output(dir: &Path, pattern: &str, out: &Path) -> Result<()> {
    ensure!(
        pattern.ends_with(".jar")
            && Path::new(pattern)
                .components()
                .all(|p| matches!(p, Component::Normal(_) | Component::CurDir)),
        "expected a relative JAR path"
    );
    let root = dir.canonicalize()?;
    let search = format!(
        "{}/{}",
        glob::Pattern::escape(&dir.to_string_lossy()),
        pattern
    );
    let matches = glob::glob(&search)?.collect::<Result<Vec<_>, _>>()?;
    ensure!(
        matches.len() == 1,
        "expected one JAR matching {pattern}, found {}",
        matches.len()
    );
    let path = matches[0].canonicalize()?;
    ensure!(path.starts_with(root), "artifact outside checkout");
    fs::copy(path, out)?;
    Ok(())
}
pub fn build(repo: &str, id: &str, recipe: &Recipe, out: &Path) -> Result<()> {
    let temp = tempdir()?;
    let dir = temp.path();
    ensure!(
        Command::new("git")
            .args(["clone", "--quiet", "--no-checkout", "--", repo])
            .arg(dir)
            .status()?
            .success(),
        "git clone failed"
    );
    run(
        dir,
        &[
            "git".into(),
            "checkout".into(),
            "--quiet".into(),
            "--detach".into(),
            id.into(),
        ],
    )?;
    let cmd = if cfg!(windows) && !recipe.windows_command.is_empty() {
        &recipe.windows_command
    } else {
        &recipe.command
    };
    if cmd.is_empty() {
        let gradle = dir.join("gradlew").exists()
            || dir.join("build.gradle").exists()
            || dir.join("build.gradle.kts").exists();
        let (wrapper, system, args) = if gradle {
            ("gradlew", "gradle", vec!["build"])
        } else {
            ensure!(dir.join("pom.xml").exists(), "no Gradle or Maven project");
            ("mvnw", "mvn", vec!["-DskipTests", "package"])
        };
        let windows = if gradle { "gradlew.bat" } else { "mvnw.cmd" };
        let mut command: Vec<String> = if cfg!(windows) && dir.join(windows).exists() {
            vec!["cmd".into(), "/d".into(), "/c".into(), windows.into()]
        } else if dir.join(wrapper).exists() && !cfg!(windows) {
            vec!["sh".into(), wrapper.into()]
        } else {
            vec![system.into()]
        };
        command.extend(args.into_iter().map(String::from));
        run(dir, &command)?;
    } else {
        run(dir, cmd)?
    }
    copy_output(dir, &recipe.artifact, out)
}
