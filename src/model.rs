//! Persistent package identity. Field names on disk are shared with the original CLI.
use anyhow::{Result, bail, ensure};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

pub type Lock = BTreeMap<String, Installed>;

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase", default)]
pub struct Reference {
    pub provider: String,
    pub project: String,
    pub asset: String,
    pub selector: String,
}

pub fn valid_name(value: &str) -> bool {
    !value.is_empty()
        && value != "."
        && value != ".."
        && value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
}

impl Reference {
    pub fn new(provider: &str, project: &str) -> Self {
        Self {
            provider: provider.into(),
            project: project.into(),
            ..Self::default()
        }
    }
    pub fn key(&self) -> String {
        format!("{}:{}", self.provider, self.project.to_ascii_lowercase())
    }
    /// Parse provider:project#asset@version. Asset globs apply only to GitHub releases.
    pub fn parse(spec: &str) -> Result<Self> {
        let (provider, rest) = spec.split_once(':').unwrap_or(("modrinth", spec));
        let (rest, selector) = rest.split_once('@').unwrap_or((rest, ""));
        let (project, asset) = rest.split_once('#').unwrap_or((rest, ""));
        ensure!(
            !spec.ends_with('@') && !rest.ends_with('#'),
            "empty selector in {spec}"
        );
        let parts: Vec<_> = project.split('/').collect();
        ensure!(
            parts.iter().all(|s| valid_name(s)),
            "invalid project: {project}"
        );
        match provider {
            "github" => {
                ensure!(parts.len() == 2, "GitHub requires owner/repo");
                glob::Pattern::new(asset)?;
            }
            "modrinth" | "hangar" | "source" => ensure!(
                parts.len() == 1 && asset.is_empty(),
                "invalid {provider} project"
            ),
            "spigot" => ensure!(
                project.bytes().all(|b| b.is_ascii_digit())
                    && selector.bytes().all(|b| b.is_ascii_digit())
                    && asset.is_empty(),
                "Spigot requires numeric IDs"
            ),
            _ => bail!("unknown provider: {provider}"),
        }
        Ok(Self {
            provider: provider.into(),
            project: project.into(),
            asset: asset.into(),
            selector: selector.into(),
        })
    }
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase", default)]
pub struct Target {
    pub loader: String,
    pub minecraft: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase", default)]
pub struct Installed {
    pub source: Reference,
    pub target: Target,
    #[serde(rename = "ProjectID")]
    pub project_id: String,
    #[serde(rename = "ArtifactID")]
    pub id: String,
    pub recipe_hash: String,
    pub version: String,
    pub available: String,
    #[serde(rename = "JAR")]
    pub jar: String,
    pub name: String,
    #[serde(rename = "SHA256")]
    pub sha256: String,
    // Go wrote null for an empty dependency slice.
    pub requires: Option<Vec<Reference>>,
}
impl Installed {
    pub fn tracks_updates(&self) -> bool {
        self.source.provider != "local" && self.source.selector.is_empty()
    }
}

pub fn lookup(lock: &Lock, name: &str) -> Result<String> {
    let query = name.to_ascii_lowercase();
    if lock.contains_key(&query) {
        return Ok(query);
    }
    let mut matches = lock.iter().filter(|(_, e)| {
        [&e.name, &e.jar, &e.source.project]
            .iter()
            .any(|v| v.eq_ignore_ascii_case(name))
    });
    let (key, _) = matches
        .next()
        .ok_or_else(|| anyhow::anyhow!("{name} is not managed by mcpkg"))?;
    ensure!(
        matches.next().is_none(),
        "ambiguous package {name}; use a source ID"
    );
    Ok(key.clone())
}
pub fn canonical(lock: &Lock, r: &Reference) -> String {
    // Modrinth dependencies commonly refer to the opaque ID of a package installed by slug.
    if r.provider == "modrinth"
        && let Some((key, _)) = lock
            .iter()
            .find(|(_, e)| e.source.provider == "modrinth" && e.project_id == r.project)
    {
        return key.clone();
    }
    r.key()
}
