//! Fetch stable Paper builds or compile Spigot with its official BuildTools.
use crate::{
    artifact,
    catalog::{Catalog, Release},
    model::{Target, valid_name},
};
use anyhow::{Context, Result, ensure};
use std::path::Path;
const BUILDTOOLS: &str = "https://hub.spigotmc.org/jenkins/job/BuildTools/lastSuccessfulBuild/artifact/target/BuildTools.jar";
pub fn download(c: &Catalog, name: &str, t: &Target, out: &Path) -> Result<()> {
    let cache = c.root.join(".mcpkg-cache");
    if name == "buildtools" {
        return artifact::fetch(c.http()?, BUILDTOOLS, "", &cache, out);
    }
    if !matches!(name, "paper" | "folia" | "spigot") {
        return c.materialize(&c.resolve(&c.reference(name)?, t)?, out);
    }
    ensure!(valid_name(&t.minecraft), "specify a Minecraft version");
    if name == "spigot" {
        let work = tempfile::tempdir()?;
        let dir = work.path();
        artifact::fetch(
            c.http()?,
            BUILDTOOLS,
            "",
            &cache,
            &dir.join("BuildTools.jar"),
        )?;
        artifact::run(
            dir,
            &[
                "java",
                "-jar",
                "BuildTools.jar",
                "--nogui",
                "--rev",
                &t.minecraft,
                "--compile",
                "SPIGOT",
                "--output-dir",
                "output",
            ]
            .map(String::from),
        )?;
        return artifact::copy_output(dir, "output/spigot-*.jar", out);
    }
    let builds = c.get(&format!(
        "https://fill.papermc.io/v3/projects/{name}/versions/{}/builds",
        t.minecraft
    ))?;
    let latest = builds
        .as_array()
        .context("invalid Paper build list")?
        .iter()
        .filter(|b| b["channel"] == "STABLE" && b["downloads"]["server:default"]["url"].is_string())
        .max_by_key(|b| b["id"].as_u64())
        .context("no stable build available")?;
    let file = &latest["downloads"]["server:default"];
    c.materialize(
        &Release {
            url: file["url"].as_str().unwrap().into(),
            digest: file["checksums"]["sha256"]
                .as_str()
                .context("missing build SHA256")?
                .into(),
            ..Default::default()
        },
        out,
    )
}
