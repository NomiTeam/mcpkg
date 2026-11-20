# mcpkg

mcpkg is a command-line tool for finding and managing Minecraft server plugins.

## Build

With Go 1.23 or later, run `go build .` from the repository root. The binary is
`./mcpkg` on Linux or macOS and `.\mcpkg.exe` on Windows.

## Usage

Run mcpkg from your server directory or supply `-server <directory>` before the
command. Use the filename shown by `list` for `disable` and `enable`.

```sh
mcpkg list
mcpkg -minecraft 26.3 search LuckPerms
mcpkg disable LuckPerms.jar
mcpkg enable LuckPerms.jar.disabled
```

Search uses Modrinth and defaults to Paper. Use `-loader folia` to search for
Folia plugins. Enabling and disabling rename the JAR in `plugins/` and take
effect the next time the server starts. The current commands handle filenames
directly. Check plugin dependencies before disabling a plugin.
