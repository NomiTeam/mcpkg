package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mcpkg:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mcpkg", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("server", ".", "directory containing plugins/")
	gameVersion := flags.String("minecraft", "", "Minecraft version to filter search results")
	loader := flags.String("loader", "paper", "plugin loader to filter search results")
	preview := flags.Bool("dry-run", false, "preview an upgrade without changing installed plugins")
	ref := flags.String("ref", "", "Git reference to build")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: mcpkg [options] <search|list|install|remove|update|upgrade|disable|enable|check|download|build> [arguments]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return nil
	}

	server := newServer(*root)
	command := flags.Arg(0)
	arguments := flags.Args()[1:]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch command {
	case "search":
		query := strings.TrimSpace(strings.Join(arguments, " "))
		if query == "" {
			return fmt.Errorf("usage: mcpkg search <query>")
		}
		projects, err := searchModrinth(ctx, query, *loader, *gameVersion)
		if err != nil {
			return err
		}
		table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "SLUG\tNAME\tDESCRIPTION")
		for _, project := range projects {
			fmt.Fprintf(table, "%s\t%s\t%s\n", project.Slug, project.Title, project.Description)
		}
		return table.Flush()
	case "list":
		if len(arguments) != 0 {
			return fmt.Errorf("usage: mcpkg list")
		}
		plugins, err := server.plugins()
		if err != nil {
			return err
		}
		state, err := server.loadState()
		if err != nil {
			return err
		}
		byFile := make(map[string]installedPlugin)
		for _, entry := range state {
			byFile[entry.JAR] = entry
		}
		table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "STATUS\tFILE\tSOURCE\tVERSION\tAVAILABLE")
		for _, plugin := range plugins {
			entry, managed := byFile[plugin.baseJAR]
			id := "-"
			if managed {
				id = entry.Source.key()
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", plugin.status(), plugin.filename, id, entry.Version, entry.Available)
		}
		return table.Flush()
	case "install", "remove", "update", "upgrade":
		if (command == "install" || command == "remove") && len(arguments) != 1 ||
			command == "update" && len(arguments) != 0 || command == "upgrade" && len(arguments) > 1 {
			return fmt.Errorf("invalid arguments for %s (see README for usage)", command)
		}
		argument := ""
		if len(arguments) == 1 {
			argument = arguments[0]
		}
		return server.manage(ctx, command, argument, catalog{Loader: *loader, Minecraft: *gameVersion}, *preview, stdout)
	case "check":
		if len(arguments) != 0 {
			return fmt.Errorf("usage: mcpkg check")
		}
		files, err := server.inventory()
		if err != nil {
			return err
		}
		state, err := server.loadState()
		if err != nil {
			return err
		}
		problems := dependencyProblems(withSourceDependencies(files, state))
		for _, file := range files {
			if file.enabled && file.metadataError != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", file.filename, file.metadataError))
			}
		}
		for _, problem := range problems {
			fmt.Fprintln(stdout, problem)
		}
		if len(problems) != 0 {
			return fmt.Errorf("%d plugin checks failed", len(problems))
		}
		fmt.Fprintln(stdout, "All required plugin dependencies are enabled.")
		return nil
	case "download":
		if len(arguments) != 2 {
			return fmt.Errorf("usage: mcpkg [-minecraft version] download <paper|folia|spigot|buildtools|source:plugin> <output.jar>")
		}
		if strings.Contains(arguments[0], ":") {
			src, err := parseSource(arguments[0])
			if err != nil {
				return err
			}
			a, err := (catalog{Loader: *loader, Minecraft: *gameVersion}).resolve(ctx, src)
			if err != nil {
				return err
			}
			return publishJAR(arguments[1], func(path string) error {
				_, err := downloadArtifact(ctx, path, a)
				return err
			})
		}
		buildContext, stop := context.WithTimeout(context.Background(), 30*time.Minute)
		defer stop()
		return downloadServer(buildContext, arguments[0], *gameVersion, arguments[1], stdout, stderr)
	case "build":
		if len(arguments) < 3 || (len(arguments) > 3 && (arguments[3] != "--" || len(arguments) == 4)) {
			return fmt.Errorf("usage: mcpkg [-ref ref] build <repository> <artifact-path> <output.jar> [-- <command> ...]")
		}
		var buildCommand []string
		if len(arguments) > 3 {
			buildCommand = arguments[4:]
		}
		buildContext, stop := context.WithTimeout(context.Background(), 30*time.Minute)
		defer stop()
		return repositoryBuild(buildContext, arguments[0], *ref, arguments[1], arguments[2], buildCommand, stdout, stderr)
	case "enable", "disable":
		if len(arguments) != 1 {
			return fmt.Errorf("usage: mcpkg %s <jar filename>", command)
		}
		filename, err := server.setEnabled(arguments[0], command == "enable")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s (takes effect when the server starts)\n", filename)
		return err
	default:
		return fmt.Errorf("unknown command %q (see mcpkg -help)", command)
	}
}
