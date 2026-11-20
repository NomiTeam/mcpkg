//! Resolve free hosted Spigot resources through the Spiget API.
use super::{Catalog, Release, binary, field};
use crate::model::Reference;
use anyhow::{Context, Result, ensure};
impl Catalog {
    pub(super) fn spigot(&self, r: &Reference) -> Result<Release> {
        let url = format!("https://api.spiget.org/v2/resources/{}", r.project);
        let resource = self.get(&url)?;
        ensure!(
            resource["premium"] != true && resource["external"] != true,
            "manual download required"
        );
        let selector = if r.selector.is_empty() {
            "latest"
        } else {
            &r.selector
        };
        let v = self.get(&format!("{url}/versions/{selector}"))?;
        let id = v["id"].as_u64().context("missing version ID")?.to_string();
        let download = if r.selector.is_empty() {
            format!("{url}/download")
        } else {
            format!("{url}/versions/{id}/download")
        };
        Ok(binary(id, field(&v, "name")?, download))
    }
}
