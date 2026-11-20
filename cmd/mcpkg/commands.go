package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/NomiTeam/mcpkg/internal/artifact"
	"github.com/NomiTeam/mcpkg/internal/manager"
	"github.com/NomiTeam/mcpkg/internal/source"
)

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mcpkg", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("server", ".", "directory containing plugins/")
	gameVersion := flags.String("minecraft", "", "Minecraft version to filter search results")
	loader := flags.String("loader", "", "plugin loader (defaults to mcpkg.yml or paper)")
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

	server := manager.NewServer(*root)
	config, err := server.LoadConfig()
	if err != nil {
		return err
	}
	if *gameVersion == "" {
		*gameVersion = config.Minecraft
	}
	if *loader == "" {
		*loader = config.Loader
	}
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
		projects, err := source.SearchModrinth(ctx, query, *loader, *gameVersion)
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
		plugins, err := server.List()
		if err != nil {
			return err
		}
		table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "STATUS\tFILE\tSOURCE\tVERSION\tAVAILABLE")
		for _, plugin := range plugins {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", plugin.Status, plugin.File, plugin.Source, plugin.Version, plugin.Available)
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
		manageContext, stop := context.WithTimeout(context.Background(), 30*time.Minute)
		defer stop()
		return server.Manage(manageContext, command, argument, source.Catalog{Loader: *loader, Minecraft: *gameVersion}, *preview, stdout, stderr)
	case "check":
		if len(arguments) != 0 {
			return fmt.Errorf("usage: mcpkg check")
		}
		problems, err := server.Check()
		if err != nil {
			return err
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
			src, err := source.Parse(arguments[0])
			if err != nil {
				return err
			}
			a, err := (source.Catalog{Loader: *loader, Minecraft: *gameVersion}).Resolve(ctx, src)
			if err != nil {
				return err
			}
			return artifact.Publish(arguments[1], func(path string) error {
				_, err := artifact.Download(ctx, path, a.URL, a.Digest)
				return err
			})
		}
		buildContext, stop := context.WithTimeout(context.Background(), 30*time.Minute)
		defer stop()
		return artifact.DownloadServer(buildContext, arguments[0], *gameVersion, arguments[1], stdout, stderr)
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
		return artifact.BuildRepository(buildContext, arguments[0], *ref, arguments[1], arguments[2], buildCommand, stdout, stderr)
	case "enable", "disable":
		if len(arguments) != 1 {
			return fmt.Errorf("usage: mcpkg %s <jar filename>", command)
		}
		filename, err := server.SetEnabled(arguments[0], command == "enable")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s (takes effect when the server starts)\n", filename)
		return err
	default:
		return fmt.Errorf("unknown command %q (see mcpkg -help)", command)
	}
}
