//! Page through Hangar releases until a compatible Paper download is available.
use super::{Catalog, Release, array, binary, field};
use crate::model::{Reference, Target};
use anyhow::{Result, bail, ensure};
use serde_json::json;
impl Catalog {
    pub(super) fn hangar(&self, r: &Reference, t: &Target) -> Result<Release> {
        ensure!(
            matches!(t.loader.as_str(), "paper" | "folia") && !t.minecraft.is_empty(),
            "Hangar needs a Paper or Folia Minecraft version"
        );
        let base = format!(
            "https://hangar.papermc.io/api/v1/projects/{}/versions",
            r.project
        );
        for page in 0usize.. {
            let versions = if r.selector.is_empty() {
                array(&self.get(&format!("{base}?limit=25&offset={}", page * 25))?["result"])?
                    .clone()
            } else {
                let mut url = reqwest::Url::parse(&base)?;
                url.path_segments_mut().unwrap().push(&r.selector);
                vec![self.get(url.as_str())?]
            };
            for v in &versions {
                if r.selector.is_empty()
                    && !v["channel"]["name"]
                        .as_str()
                        .is_some_and(|n| n.eq_ignore_ascii_case("release"))
                {
                    continue;
                }
                let file = &v["downloads"]["PAPER"];
                if v["platformDependencies"]["PAPER"]
                    .as_array()
                    .is_some_and(|a| a.contains(&json!(t.minecraft)))
                    && file["downloadUrl"].is_string()
                {
                    let name = field(v, "name")?;
                    let mut release = binary(name.clone(), name, field(file, "downloadUrl")?);
                    release.digest = field(&file["fileInfo"], "sha256Hash")?;
                    return Ok(release);
                }
            }
            if !r.selector.is_empty() || versions.len() < 25 {
                break;
            }
        }
        bail!("no compatible Hangar release")
    }
}
