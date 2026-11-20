# mcpkg

mcpkg finds and manages Minecraft server plugins from the command line. It also
downloads server JARs and builds JARs from Git repositories.

## Build

Run `go build ./cmd/mcpkg` from the repository root with Go 1.23 or newer. The binary
is `./mcpkg` on Linux or macOS and `.\mcpkg.exe` on Windows.

## Manage plugins

Run from the server directory, or put `-server <directory>` before a command.
Set the Minecraft version and loader in `mcpkg.yml` at the server root, or
specify them per command with `-minecraft` and `-loader`. The flags override
the file, and the default loader is Paper.

```yaml
minecraft: "26.3"
loader: paper
```

```sh
mcpkg -minecraft 26.3 search ViaVersion
mcpkg -minecraft 26.3 install viabackwards
mcpkg list
mcpkg update
mcpkg -dry-run upgrade
mcpkg upgrade
mcpkg disable ViaBackwards
mcpkg enable ViaBackwards
mcpkg check
mcpkg remove ViaBackwards
```

| Source | Install argument |
| --- | --- |
| Modrinth | `viaversion` or `modrinth:viaversion@5.12.2` |
| GitHub releases | `github:ViaVersion/ViaVersion` or `'github:owner/repo#plugin-*.jar@v1.2.0'` |
| PaperMC Hangar | `hangar:ViaVersion` or `hangar:ViaVersion@5.12.2` |
| SpigotMC through Spiget | `spigot:34315` or `spigot:34315@344916` |
| Local or built JAR | `file:./plugin.jar` |
| Configured source | `custom-fishing` or `source:custom-fishing` |

Quote GitHub asset patterns, and include one if the release contains multiple
JARs. Spiget downloads are limited to free, hosted JARs where Spiget can provide
the binary. Use `file:` for premium or external downloads. For GitHub, Spiget,
and local files, choose a build compatible with your server.

`install` installs resolvable Modrinth requirements and checks hard dependencies
from plugin descriptors, including dependencies already present on the server.
`check` reports missing or disabled requirements. `disable` renames the JAR to
`.jar.disabled`, and `enable` removes that suffix (or the older `.off` suffix).
Use the filename from `list` for manually installed plugins. File changes
take effect the next time the server starts.

`mcpkg.lock` records managed plugins and their resolved versions. `update` refreshes
available versions without replacing JARs. `upgrade [plugin]` applies newer
versions, preserving disabled status and skipping local files and versions pinned
with `@`. Install a new local JAR to update a previous local installation.
`remove` deletes only the managed JAR and its lock entry. Plugin data remains
in place.

## Add a plugin source

1. Create a `sources.d/` directory at the server root.
2. Copy [custom-fishing.yml](examples/server/sources.d/custom-fishing.yml) into it. The filename
   supplies the package name. Custom-Fishing requires JDK 21.
3. Run `mcpkg install custom-fishing` and then `mcpkg list`.

A Git source uses `repository`, `ref` and a `build` block. Omit `ref` to
track the remote default branch or use a full commit ID to pin it. Commands
are argument lists. `windows-command` overrides `command` on Windows. The
artifact path is relative to the checkout and may use a filename pattern
that matches one JAR.

`update` compares Git commits and recipes without building. `upgrade` builds
the resolved commit, and `-dry-run upgrade` shows the planned build. The lock
records the full commit and JAR checksum. Move a manually installed copy of
the same plugin out of `plugins/` before installing it from a source. Keep
source files you trust because Git sources execute the configured command.

For a binary release, save a source file such as `sources.d/viaversion.yml`.
The `asset` pattern and release tag in `ref` are optional.

```yaml
type: github-release
repository: ViaVersion/ViaVersion
asset: "ViaVersion-*.jar"
```

## Download a JAR

```sh
mcpkg -minecraft 26.3 download paper paper.jar
mcpkg -minecraft 26.3 download folia folia.jar
mcpkg -minecraft 1.21.11 download spigot spigot.jar
mcpkg download github:ViaVersion/ViaVersion ViaVersion.jar
```

Paper and Folia use the latest stable build for the requested Minecraft version.
Spigot uses official BuildTools to compile the server locally. Have Git and the
JDK required by that Minecraft version on your PATH. Use
`mcpkg download buildtools BuildTools.jar` to fetch BuildTools separately.

## Build a JAR from Git

```sh
mcpkg -ref v1.2.0 build https://github.com/owner/plugin.git build/libs/plugin.jar plugin.jar
mcpkg build ./plugin-repo target/plugin.jar plugin.jar -- mvn -DskipTests package
mcpkg install file:./plugin.jar
```

Supply the JAR path relative to the repository root. Omit `-ref` to build the
default branch. mcpkg uses a Gradle or Maven wrapper when available, or the
installed tool when it detects a build file. Pass `-- <command> [args]` to choose
the build command yourself. On Windows, use `-- cmd.exe /d /c mvn package` when
calling a batch command directly. Builds run repository code with your
permissions, so build repositories you trust.
