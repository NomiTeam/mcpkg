package manager

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/NomiTeam/mcpkg/internal/source"
)

// Manage serializes package changes and dispatches the requested operation.
func (server Server) Manage(ctx context.Context, command, argument string, target source.Catalog, preview bool, output, stderr io.Writer) error {
	unlock, err := server.lock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := server.loadState()
	if err != nil {
		return err
	}

	if command == "update" {
		return server.refreshVersions(ctx, state, preview, output)
	}

	inventory, err := server.inventory()
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(server.pluginsPath, ".mcpkg-stage-*")
	if err != nil {
		return err
	}
	original := make(packageState, len(state))
	for key, entry := range state {
		original[key] = entry
	}
	plan := packagePlan{
		server: server, state: state, inventory: inventory, original: original, staging: staging,
		planned: make(map[string]bool), preview: preview, output: output, stderr: stderr,
	}
	defer func() {
		if plan.staging != "" {
			os.RemoveAll(plan.staging)
		}
	}()

	switch command {
	case "install":
		err = plan.install(ctx, target, argument)
	case "remove":
		err = plan.remove(argument)
	case "upgrade":
		err = plan.upgrade(ctx, argument)
	default:
		return fmt.Errorf("unknown package operation %q", command)
	}
	if err != nil {
		return err
	}

	for _, build := range plan.builds {
		fmt.Fprintln(output, build)
	}
	if len(plan.changes) == 0 {
		if len(plan.builds) != 0 {
			return nil
		}
		_, err := fmt.Fprintln(output, "No changes.")
		return err
	}
	if err := plan.validate(); err != nil {
		return err
	}
	for _, change := range plan.changes {
		switch {
		case change.staged == "":
			fmt.Fprintf(output, "Remove %s\n", change.before)
		case change.before == "":
			fmt.Fprintf(output, "Install %s %s\n", change.after.metadata.Name, change.after.metadata.Version)
		default:
			fmt.Fprintf(output, "Replace %s with %s %s\n", change.before, change.after.metadata.Name, change.after.metadata.Version)
		}
	}
	if preview {
		return nil
	}
	return plan.commit()
}

// refreshVersions updates availability in the lock while leaving JARs on disk untouched.
func (server Server) refreshVersions(ctx context.Context, state packageState, preview bool, output io.Writer) error {
	available := 0
	for _, key := range stateKeys(state) {
		entry := state[key]
		if entry.Source.Selector != "" || entry.Source.Provider == "local" {
			continue
		}
		a, err := server.resolveSource(ctx, entry.Target, entry.Source)
		if err != nil {
			return fmt.Errorf("check %s: %w", key, err)
		}
		entry.Available = ""
		if a.ID != entry.ArtifactID || a.RecipeHash != entry.RecipeHash {
			entry.Available = a.Version
			available++
			if a.ID == entry.ArtifactID {
				fmt.Fprintf(output, "%s: build recipe changed at %s\n", key, a.Version)
			} else {
				fmt.Fprintf(output, "%s: %s -> %s\n", key, entry.Version, a.Version)
			}
		}
		state[key] = entry
	}
	if available == 0 {
		fmt.Fprintln(output, "No updates available.")
	}
	if preview {
		return nil
	}
	return server.saveState(state)
}
