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
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: mcpkg [options] <search|list|disable|enable> [argument]")
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
	switch command {
	case "search":
		query := strings.TrimSpace(strings.Join(arguments, " "))
		if query == "" {
			return fmt.Errorf("usage: mcpkg search <query>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

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
		table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "STATUS\tFILE")
		for _, plugin := range plugins {
			fmt.Fprintf(table, "%s\t%s\n", plugin.status(), plugin.filename)
		}
		return table.Flush()
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
