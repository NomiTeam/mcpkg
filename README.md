# mcpkg

mcpkg finds and manages Minecraft server plugins from the command line. It also
downloads server JARs and builds JARs from Git repositories.

## Build

Run `go build .` from the repository root with Go 1.23 or newer. The binary
is `./mcpkg` on Linux or macOS and `.\mcpkg.exe` on Windows.

## Manage plugins

Run from the server directory, or put `-server <directory>` before a command.
Include `-minecraft <version>` when installing from Modrinth or Hangar.
Modrinth defaults to Paper. Choose another loader with `-loader`.

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
