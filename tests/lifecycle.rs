//! Exercise the published CLI, including a source-only package changing underneath an install.
use std::{
    fs,
    io::Write,
    path::Path,
    process::{Command, Output},
};
use tempfile::tempdir;

fn cli(server: &Path, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_mcpkg"))
        .current_dir(server)
        .args(args)
        .output()
        .unwrap()
}
fn success(out: Output) {
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
}
fn git(repo: &Path, args: &[&str]) {
    assert!(
        Command::new("git")
            .arg("-C")
            .arg(repo)
            .args([
                "-c",
                "user.name=Fixture",
                "-c",
                "user.email=fixture@example.invalid"
            ])
            .args(args)
            .status()
            .unwrap()
            .success()
    )
}
fn jar(path: &Path, name: &str, version: &str, dep: &str) {
    let mut zip = zip::ZipWriter::new(fs::File::create(path).unwrap());
    zip.start_file("plugin.yml", zip::write::SimpleFileOptions::default())
        .unwrap();
    writeln!(zip, "name: {name}\nversion: '{version}'\ndepend: [{dep}]").unwrap();
    zip.finish().unwrap();
}
#[test]
fn init_preserves_settings_and_installed_files() {
    let root = tempdir().unwrap();
    let dir = root.path();
    success(cli(dir, &["init"]));
    assert!(dir.join("sources.d").is_dir());
    let config = "minecraft: '1.21'\nloader: folia\n";
    fs::write(dir.join("mcpkg.yml"), config).unwrap();
    jar(&dir.join("plugins/manual.jar"), "Manual", "1", "");
    success(cli(dir, &["init"]));
    assert_eq!(fs::read_to_string(dir.join("mcpkg.yml")).unwrap(), config);
    success(cli(dir, &["check"]));
    assert!(dir.join("plugins/manual.jar").is_file());
}
#[test]
fn source_upgrade_preserves_disabled_state_and_dependencies() {
    let root = tempdir().unwrap();
    let dir = root.path();
    success(cli(dir, &["init"]));
    let repo = dir.join("repository");
    fs::create_dir(&repo).unwrap();
    git(&repo, &["init", "-q"]);
    jar(&repo.join("input.jar"), "Example", "1", "Library");
    git(&repo, &["add", "."]);
    git(&repo, &["commit", "-qm", "first"]);
    fs::write(dir.join("sources.d/example.yml"),"type: git\nrepository: repository\nbuild:\n  command: [git, status, --short]\n  artifact: input.jar\n").unwrap();
    assert!(!cli(dir, &["install", "example"]).status.success());
    assert!(!dir.join("mcpkg.lock").exists());
    jar(&dir.join("lib.jar"), "Library", "1", "");
    success(cli(dir, &["install", "file:lib.jar"]));
    success(cli(dir, &["install", "example"]));
    assert!(!cli(dir, &["remove", "Library"]).status.success());
    success(cli(dir, &["disable", "Example"]));
    jar(&repo.join("input.jar"), "Example", "2", "Library");
    git(&repo, &["commit", "-qam", "second"]);
    success(cli(dir, &["update"]));
    let state = fs::read(dir.join("mcpkg.lock")).unwrap();
    success(cli(dir, &["-dry-run", "upgrade"]));
    assert_eq!(fs::read(dir.join("mcpkg.lock")).unwrap(), state);
    success(cli(dir, &["upgrade"]));
    let path = dir.join("plugins/mcpkg-source-example.jar.disabled");
    assert_eq!(
        fs::read(path).unwrap(),
        fs::read(repo.join("input.jar")).unwrap()
    );
    success(cli(dir, &["enable", "Example"]));
    success(cli(dir, &["check"]));
    success(cli(dir, &["remove", "Example"]));
    success(cli(dir, &["remove", "Library"]));
    assert_eq!(fs::read_dir(dir.join("plugins")).unwrap().count(), 0);
}

#[test]
fn modified_managed_jar_and_suffix_collision_preserve_files() {
    let root = tempdir().unwrap();
    let dir = root.path();
    success(cli(dir, &["init"]));
    let input = dir.join("input.jar");
    jar(&input, "Library", "1", "");
    success(cli(dir, &["install", "file:input.jar"]));
    let state = fs::read(dir.join("mcpkg.lock")).unwrap();
    let live = dir.join("plugins/mcpkg-local-library.jar");
    let disabled = live.with_extension("jar.disabled");
    fs::copy(&input, &disabled).unwrap();
    assert!(!cli(dir, &["disable", "Library"]).status.success());
    assert!(live.exists() && disabled.exists());
    fs::remove_file(disabled).unwrap();
    jar(&live, "Library", "edited", "");
    assert!(!cli(dir, &["remove", "Library"]).status.success());
    assert!(!cli(dir, &["install", "file:input.jar"]).status.success());
    assert!(live.exists());
    assert_eq!(fs::read(dir.join("mcpkg.lock")).unwrap(), state);
}
