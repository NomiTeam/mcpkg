//! Resolve registry and configured source metadata independently of installing files.
mod configured;
mod github;
mod hangar;
mod modrinth;
mod spigot;

use crate::{
    artifact::{self, Recipe},
    model::{Reference, Target, valid_name},
};
use anyhow::{Context, Result, bail};
use reqwest::blocking::Client;
use serde_json::Value;

use std::path::{Path, PathBuf};

#[derive(Clone, Debug, Default)]
pub struct Release {
    pub id: String,
    pub version: String,
    pub project: String,
    pub url: String,
    pub digest: String,
    pub deps: Vec<Reference>,
    pub build: Option<(String, Recipe)>,
    pub recipe_hash: String,
}
pub struct Catalog {
    pub client: std::sync::OnceLock<reqwest::Result<Client>>,
    pub root: PathBuf,
}
fn field(v: &Value, k: &str) -> Result<String> {
    Ok(v[k]
        .as_str()
        .with_context(|| format!("registry omitted {k}"))?
        .into())
}
fn array(v: &Value) -> Result<&Vec<Value>> {
    v.as_array().context("expected registry result array")
}
fn binary(id: String, version: String, url: String) -> Release {
    Release {
        id,
        version,
        url,
        ..Default::default()
    }
}
impl Catalog {
    pub fn http(&self) -> Result<&Client> {
        self.client
            .get_or_init(|| {
                Client::builder()
                    .user_agent("NomiTeam/mcpkg (https://github.com/NomiTeam/mcpkg)")
                    .timeout(std::time::Duration::from_secs(180))
                    .build()
            })
            .as_ref()
            .map_err(|err| anyhow::anyhow!("create HTTP client: {err}"))
    }
    pub fn get(&self, url: &str) -> Result<Value> {
        Ok(self.http()?.get(url).send()?.error_for_status()?.json()?)
    }
    pub fn reference(&self, spec: &str) -> Result<Reference> {
        if valid_name(spec)
            && self
                .root
                .join("sources.d")
                .join(format!("{spec}.yml"))
                .is_file()
        {
            Ok(Reference::new("source", spec))
        } else {
            Reference::parse(spec)
        }
    }
    pub fn resolve(&self, r: &Reference, t: &Target) -> Result<Release> {
        match r.provider.as_str() {
            "source" => self.configured(r, t),
            "github" => self.github(r),
            "modrinth" => self.modrinth(r, t),
            "spigot" => self.spigot(r),
            "hangar" => self.hangar(r, t),
            _ => bail!("unsupported source {}", r.provider),
        }
        .with_context(|| format!("resolve {}", r.key()))
    }
    pub fn materialize(&self, r: &Release, out: &Path) -> Result<()> {
        if let Some((repo, recipe)) = &r.build {
            artifact::build(repo, &r.id, recipe, out)
        } else {
            artifact::fetch(
                self.http()?,
                &r.url,
                &r.digest,
                &self.root.join(".mcpkg-cache"),
                out,
            )
        }
    }
}
