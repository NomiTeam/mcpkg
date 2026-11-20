//! Read server-local recipes and resolve immutable Git commits.
use super::{Catalog, Release};
use crate::{
    artifact::{self, Recipe},
    model::{Reference, Target, valid_name},
};
use anyhow::{Context, Result, bail, ensure};
use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::{fs, path::Path};
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Definition {
    #[serde(rename = "type")]
    kind: String,
    repository: String,
    #[serde(default, rename = "ref")]
    reference: String,
    #[serde(default)]
    asset: String,
    build: Option<Recipe>,
}
impl Definition {
    // The previous CLI fingerprinted this ordered JSON record. Keep its field order,
    // null slice representation and escaping so unchanged recipes survive migration.
    fn fingerprint(&self) -> Result<String> {
        #[derive(serde::Serialize)]
        #[serde(rename_all = "PascalCase")]
        struct Build<'a> {
            command: Option<&'a [String]>,
            windows_command: Option<&'a [String]>,
            artifact: &'a str,
        }
        #[derive(serde::Serialize)]
        #[serde(rename_all = "PascalCase")]
        struct Record<'a> {
            #[serde(rename = "Type")]
            kind: &'a str,
            repository: &'a str,
            #[serde(rename = "Ref")]
            reference: &'a str,
            asset: &'a str,
            build: Build<'a>,
        }
        let recipe = self.build.as_ref().context("missing build recipe")?;

        let build = Build {
            command: (!recipe.command.is_empty()).then_some(&recipe.command[..]),
            windows_command: (!recipe.windows_command.is_empty())
                .then_some(&recipe.windows_command[..]),
            artifact: &recipe.artifact,
        };
        let record = Record {
            kind: &self.kind,
            repository: &self.repository,
            reference: &self.reference,
            asset: &self.asset,
            build,
        };
        let json = serde_json::to_string(&record)?
            .replace('&', r"\u0026")
            .replace('<', r"\u003c")
            .replace('>', r"\u003e");
        Ok(format!("{:x}", Sha256::digest(json)))
    }
}

impl Catalog {
    pub(super) fn configured(&self, r: &Reference, t: &Target) -> Result<Release> {
        ensure!(valid_name(&r.project), "invalid source alias");
        let path = self
            .root
            .join("sources.d")
            .join(format!("{}.yml", r.project));
        let bytes = fs::read(&path).with_context(|| format!("read {}", path.display()))?;
        let mut d: Definition = serde_yaml::from_slice(&bytes)?;
        let reference = if r.selector.is_empty() {
            &d.reference
        } else {
            &r.selector
        };
        match d.kind.as_str() {
            "github-release" => {
                ensure!(d.build.is_none(), "build applies only to Git sources");
                let mut github = Reference::parse(&format!("github:{}", d.repository))?;
                github.asset = d.asset;
                github.selector = reference.clone();
                self.resolve(&github, t)
            }
            "git" => {
                d.repository =
                    if !d.repository.contains(':') && !Path::new(&d.repository).is_absolute() {
                        std::path::absolute(self.root.join(&d.repository))?
                            .to_string_lossy()
                            .into_owned()
                    } else {
                        d.repository.clone()
                    };
                ensure!(d.asset.is_empty(), "asset applies only to release sources");
                let id = artifact::commit(&d.repository, reference)?;
                let recipe_hash = d.fingerprint()?;
                let repo = d.repository;
                Ok(Release {
                    version: id.chars().take(12).collect(),
                    id,
                    build: Some((repo, d.build.context("missing build recipe")?)),
                    recipe_hash,
                    ..Default::default()
                })
            }
            _ => bail!("unknown source type {}", d.kind),
        }
    }
}
