//! Command-line entry point. Keep registry access lazy so local commands start cheaply.
mod artifact;
mod catalog;
mod distribution;
mod inventory;
mod manager;
mod model;
mod server;
use crate::{catalog::Catalog, manager::Plan};
use anyhow::{Context, Result, bail, ensure};
use clap::{Parser, Subcommand};
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};

#[derive(Parser)]
#[command(version, about = "Manage Minecraft server plugins")]
struct Args {
    #[arg(long, global = true, default_value = ".")]
    server: PathBuf,
    #[arg(long, global = true)]
    minecraft: Option<String>,
    #[arg(long, global = true)]
    loader: Option<String>,
    #[arg(long, global = true)]
    dry_run: bool,
    #[arg(long = "ref", global = true, default_value = "")]
    reference: String,
    #[command(subcommand)]
    action: Action,
}
#[derive(Subcommand)]
enum Action {
    /// Create an empty server layout in the selected directory.
    Init,
    List,
    Check,
    Search {
        #[arg(required = true, num_args = 1..)]
        query: Vec<String>,
    },
    Install {
        package: String,
    },
    Remove {
        package: String,
    },
    Disable {
        package: String,
    },
    Enable {
        package: String,
    },
    Update,
    Upgrade {
        package: Option<String>,
    },
    Download {
        source: String,
        destination: PathBuf,
    },
    Build {
        repository: String,
        artifact: String,
        destination: PathBuf,
        #[arg(last = true)]
        command: Vec<String>,
    },
}
fn publish(dest: &Path, write: impl FnOnce(&Path) -> Result<()>) -> Result<()> {
    ensure!(
        dest.extension()
            .is_some_and(|ext| ext.eq_ignore_ascii_case("jar")),
        "destination must have a .jar extension"
    );
    ensure!(!dest.exists(), "destination exists: {}", dest.display());
    let parent = dest
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let file = tempfile::NamedTempFile::new_in(parent)?;
    write(file.path())?;
    let mut buffer = vec![0; rawzip::RECOMMENDED_BUFFER_SIZE];
    rawzip::ZipArchive::from_file(fs::File::open(file.path())?, &mut buffer)
        .context("output is not a JAR")?;
    file.persist_noclobber(dest)?;
    Ok(())
}
fn run() -> Result<()> {
    // Older scripts use Go-style single-dash long options before the subcommand.
    let mut options = true;
    let args = std::env::args_os().map(|s| {
        if options && let Some(arg) = s.to_str() {
            if matches!(
                arg,
                "init"
                    | "list"
                    | "check"
                    | "search"
                    | "install"
                    | "remove"
                    | "disable"
                    | "enable"
                    | "update"
                    | "upgrade"
                    | "download"
                    | "build"
            ) {
                options = false
            }
            if matches!(
                arg.split('=').next(),
                Some("-server" | "-minecraft" | "-loader" | "-ref" | "-dry-run")
            ) {
                return format!("-{arg}").into();
            }
        }
        s
    });
    let a = Args::parse_from(args);
    let t = server::target(&a.server, a.loader, a.minecraft)?;
    let root = &a.server;
    let dir = root.join("plugins");
    if matches!(a.action, Action::Init) {
        return server::init(root, &t);
    }
    if matches!(a.action, Action::List) {
        let lock = server::load(root)?;
        let by_file: BTreeMap<_, _> = lock.iter().map(|(k, e)| (&e.jar, (k, e))).collect();
        println!("STATUS\tFILE\tSOURCE\tVERSION\tAVAILABLE");
        for p in inventory::scan(&dir, false)? {
            let (source, version, available) = by_file
                .get(&p.base)
                .map(|(k, e)| (k.as_str(), e.version.as_str(), e.available.as_str()))
                .unwrap_or(("-", "", ""));
            println!(
                "{}\t{}\t{source}\t{version}\t{available}",
                if p.enabled { "enabled" } else { "disabled" },
                p.file
            )
        }
        return Ok(());
    }
    // Bound metadata reads and remote resolutions even on large server hosts.
    rayon::ThreadPoolBuilder::new()
        .num_threads(std::thread::available_parallelism()?.get().min(8))
        .build_global()?;
    if matches!(a.action, Action::Check) {
        let files = inventory::scan(&dir, true)?;
        let missing = inventory::missing(&files, &server::load(root)?);
        let mut failed = !missing.is_empty();
        for (name, dep) in missing {
            eprintln!("{name} requires {dep}")
        }
        for p in files {
            if p.enabled
                && let Some(error) = p.error
            {
                failed = true;
                eprintln!("{}: {error}", p.file)
            }
        }
        ensure!(!failed, "dependency check failed");
        println!("Dependencies satisfied");
        return Ok(());
    }
    let c = Catalog {
        root: root.into(),
        client: Default::default(),
    };
    match a.action {
        Action::Search { query } => c.search(&query.join(" "), &t),
        Action::Build {
            repository,
            artifact,
            destination,
            command,
        } => publish(&destination, |out| {
            artifact::build(
                &repository,
                &artifact::commit(&repository, &a.reference)?,
                &artifact::Recipe {
                    command,
                    artifact,
                    ..Default::default()
                },
                out,
            )
        }),
        Action::Download {
            source,
            destination,
        } => publish(&destination, |out| {
            distribution::download(&c, &source, &t, out)
        }),
        action => {
            fs::create_dir_all(&dir)?;
            let _guard = server::guard(&dir)?;
            let mut lock = server::load(root)?;
            if matches!(action, Action::Update) {
                for (key, r) in manager::updates(&c, &lock, None)? {
                    let entry = lock.get_mut(&key).unwrap();
                    entry.available = if manager::changed(entry, &r) {
                        r.version
                    } else {
                        String::new()
                    };
                    if !entry.available.is_empty() {
                        println!("{key}\t{}", entry.available)
                    }
                }
                return server::save(root, &lock);
            }
            if let Action::Upgrade { package } = action {
                let releases = manager::updates(&c, &lock, package.as_deref())?
                    .into_iter()
                    .filter(|(key, r)| manager::changed(&lock[key], r))
                    .collect::<Vec<_>>();
                for (key, r) in &releases {
                    println!("{key}\t{}", r.version)
                }
                if a.dry_run || releases.is_empty() {
                    return Ok(());
                }
                let mut plan = Plan::new(&c, lock)?;
                for (key, r) in releases {
                    plan.upgrade(&key, r)?
                }
                return plan.apply();
            }
            let mut plan = Plan::new(&c, lock)?;
            match action {
                Action::Install { package } => plan.install(&package, &t)?,
                Action::Remove { package } => plan.remove(&package)?,
                Action::Enable { package } => plan.toggle(&package, true)?,
                Action::Disable { package } => plan.toggle(&package, false)?,
                _ => bail!("invalid operation"),
            }
            plan.apply().context("apply plugin changes")
        }
    }
}
fn main() {
    if let Err(e) = run() {
        eprintln!("mcpkg: {e:#}");
        std::process::exit(1)
    }
}
