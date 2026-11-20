//! Identify a single downloadable release asset by tag and filename pattern.
use super::{Catalog, Release, array, binary, field};
use crate::model::Reference;
use anyhow::{Context, Result, ensure};
impl Catalog {
    pub(super) fn github(&self, r: &Reference) -> Result<Release> {
        let mut url = reqwest::Url::parse(&format!(
            "https://api.github.com/repos/{}/releases/",
            r.project
        ))?;
        {
            let mut path = url.path_segments_mut().unwrap();
            path.pop_if_empty();
            if r.selector.is_empty() {
                path.push("latest");
            } else {
                path.push("tags").push(&r.selector);
            }
        }
        let v = self.get(url.as_str())?;
        let pattern = glob::Pattern::new(if r.asset.is_empty() {
            "*.jar"
        } else {
            &r.asset
        })?;
        let assets: Vec<_> = array(&v["assets"])?
            .iter()
            .filter(|a| {
                a["name"]
                    .as_str()
                    .is_some_and(|n| n.ends_with(".jar") && pattern.matches(n))
            })
            .collect();
        ensure!(
            assets.len() == 1,
            "expected one matching release JAR, found {}",
            assets.len()
        );
        let a = assets[0];
        let mut release = binary(
            format!(
                "{}/{}",
                field(&v, "tag_name")?,
                a["id"].as_u64().context("missing asset ID")?
            ),
            field(&v, "tag_name")?,
            field(a, "browser_download_url")?,
        );
        release.digest = a["digest"]
            .as_str()
            .and_then(|d| d.strip_prefix("sha256:"))
            .unwrap_or_default()
            .into();
        Ok(release)
    }
}
