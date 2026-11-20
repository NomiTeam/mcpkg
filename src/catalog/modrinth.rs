//! Select compatible Modrinth releases and their required project versions.
use super::{Catalog, Release, array, binary, field};
use crate::model::{Reference, Target, valid_name};
use anyhow::{Result, bail, ensure};
use serde_json::{Value, json};
impl Catalog {
    pub fn search(&self, query: &str, t: &Target) -> Result<()> {
        let mut facets = vec![
            vec!["project_type:plugin".into()],
            vec![format!("categories:{}", t.loader)],
        ];
        if !t.minecraft.is_empty() {
            facets.push(vec![format!("versions:{}", t.minecraft)])
        }
        let result: Value = self
            .http()?
            .get("https://api.modrinth.com/v2/search")
            .query(&[
                ("query", query),
                ("facets", &serde_json::to_string(&facets)?),
            ])
            .send()?
            .error_for_status()?
            .json()?;
        println!("SLUG\tNAME\tDESCRIPTION");
        for hit in array(&result["hits"])? {
            println!(
                "{}\t{}\t{}",
                field(hit, "slug")?,
                field(hit, "title")?,
                field(hit, "description")?
            )
        }
        Ok(())
    }
    pub(super) fn modrinth(&self, r: &Reference, t: &Target) -> Result<Release> {
        ensure!(!t.minecraft.is_empty(), "set the Minecraft version");
        let versions: Vec<Value> = self
            .http()?
            .get(format!(
                "https://api.modrinth.com/v2/project/{}/version",
                r.project
            ))
            .query(&[
                ("loaders", json!([t.loader]).to_string()),
                ("game_versions", json!([t.minecraft]).to_string()),
            ])
            .send()?
            .error_for_status()?
            .json()?;
        let mut candidates = versions
            .iter()
            .filter(|v| {
                if r.selector.is_empty() {
                    v["version_type"] == "release"
                } else {
                    v["id"] == r.selector || v["version_number"] == r.selector
                }
            })
            .collect::<Vec<_>>();
        candidates.sort_by_key(|v| std::cmp::Reverse(v["date_published"].as_str()));
        for v in candidates {
            let file = array(&v["files"])?
                .iter()
                .filter(|f| {
                    f["filename"].as_str().is_some_and(|n| n.ends_with(".jar"))
                        && f["hashes"]["sha512"].is_string()
                })
                .max_by_key(|f| f["primary"] == true);
            let Some(file) = file else { continue };
            let mut release = binary(
                field(v, "id")?,
                field(v, "version_number")?,
                field(file, "url")?,
            );
            release.project = field(v, "project_id")?;
            release.digest = field(&file["hashes"], "sha512")?;
            for dep in array(&v["dependencies"])?
                .iter()
                .filter(|d| d["dependency_type"] == "required")
            {
                let version = dep["version_id"].as_str().unwrap_or_default();
                let project = if let Some(id) = dep["project_id"].as_str() {
                    id.into()
                } else {
                    ensure!(
                        valid_name(version),
                        "external dependency requires manual installation"
                    );
                    field(
                        &self.get(&format!("https://api.modrinth.com/v2/version/{version}"))?,
                        "project_id",
                    )?
                };
                let mut reference = Reference::new("modrinth", &project);
                reference.selector = version.into();
                release.deps.push(reference);
            }
            return Ok(release);
        }
        bail!("no compatible Modrinth release")
    }
}
