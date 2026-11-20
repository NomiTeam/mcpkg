package manager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NomiTeam/mcpkg/src/internal/artifact"
	"github.com/NomiTeam/mcpkg/src/internal/source"
)

type jarChange struct {
	before string
	after  pluginFile
	staged string
}

// packagePlan stages a complete change before any installed JAR is replaced.
type packagePlan struct {
	server    Server
	state     packageState
	inventory []pluginFile
	original  packageState
	staging   string
	changes   []jarChange
	planned   map[string]bool
	preview   bool
	builds    []string
	output    io.Writer
	stderr    io.Writer
}

func (p *packagePlan) install(ctx context.Context, target source.Catalog, argument string) error {
	if local, ok := strings.CutPrefix(argument, "file:"); ok {
		if local == "" {
			return fmt.Errorf("file: requires a local JAR path")
		}
		return p.stage(ctx, target, source.Reference{Provider: "local"}, source.Artifact{}, local)
	}

	src, err := p.server.installSource(argument)
	if err != nil {
		return err
	}
	return p.resolve(ctx, target, src, make(map[string]bool), false)
}

func (p *packagePlan) remove(argument string) error {
	key, err := p.state.lookup(argument)
	if err != nil {
		return err
	}
	file, err := p.findInstalled(p.state[key])
	if err != nil {
		return err
	}

	p.changes = append(p.changes, jarChange{before: file.filename})
	delete(p.state, key)
	return nil
}

func (p *packagePlan) upgrade(ctx context.Context, argument string) error {
	keys := stateKeys(p.state)
	if argument != "" {
		key, err := p.state.lookup(argument)
		if err != nil {
			return err
		}
		keys = []string{key}
	}

	for _, key := range keys {
		entry := p.state[key]
		if entry.Source.Selector != "" || entry.Source.Provider == "local" {
			continue
		}
		if err := p.resolve(ctx, entry.Target, entry.Source, make(map[string]bool), false); err != nil {
			return err
		}
	}
	return nil
}

func (p *packagePlan) stage(ctx context.Context, target source.Catalog, src source.Reference, a source.Artifact, local string) error {
	key := src.Key()
	staged := filepath.Join(p.staging, fmt.Sprintf("new-%d", len(p.changes)))
	var checksum string
	var err error
	if a.Build != nil {
		// Build the commit already selected by the resolver, even if the branch moves.
		local = staged + ".jar"
		if err := artifact.BuildRepository(ctx, a.Repository, a.ID, a.Build.Artifact, local, a.Build.Arguments(), p.output, p.stderr); err != nil {
			return fmt.Errorf("build %s at %s: %w", src.Project, a.ID, err)
		}
	}
	if local != "" {
		file, openErr := os.Open(local)
		if openErr != nil {
			return openErr
		}
		checksum, err = artifact.Write(staged, file, "")
		file.Close()
	} else {
		checksum, err = artifact.Download(ctx, staged, a.URL, a.Digest)
	}
	if err != nil {
		return fmt.Errorf("stage %s: %w", key, err)
	}
	metadata, err := inspectJAR(staged)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", key, err)
	}
	if src.Provider == "local" {
		src.Project = strings.ToLower(metadata.Name)
		if !source.ValidName(src.Project) {
			return fmt.Errorf("invalid local plugin name %q", metadata.Name)
		}
		key = src.Key()
		a.Version, a.ID = metadata.Version, checksum
	}
	if a.Build != nil {
		a.Version = fmt.Sprintf("%s (%s)", metadata.Version, a.Version)
	}

	base := "mcpkg-" + src.Provider + "-" + strings.ToLower(strings.ReplaceAll(src.Project, "/", "_")) + ".jar"
	after := pluginFile{filename: base, baseJAR: base, enabled: true, metadata: metadata}
	change := jarChange{after: after, staged: staged}
	if previous, found := p.state[key]; found {
		file, err := p.findInstalled(previous)
		if err != nil {
			return err
		}
		if !strings.EqualFold(metadata.Name, previous.Name) {
			return fmt.Errorf("%s now declares plugin %q (expected %q)", key, metadata.Name, previous.Name)
		}
		// Reuse the installed path so upgrades preserve the disabled suffix.
		change.before = file.filename
		change.after.filename = file.filename
		change.after.baseJAR = file.baseJAR
		change.after.enabled = file.enabled
	}
	p.changes = append(p.changes, change)
	p.state[key] = installedPlugin{Source: src, Target: target, ProjectID: a.ProjectID, ArtifactID: a.ID, Version: a.Version,
		RecipeHash: a.RecipeHash, JAR: change.after.baseJAR, Name: metadata.Name, SHA256: checksum, Requires: a.Requires}
	p.planned[key] = true
	return nil
}

func (p *packagePlan) findInstalled(entry installedPlugin) (pluginFile, error) {
	var matches []pluginFile
	for _, file := range p.inventory {
		if strings.EqualFold(file.baseJAR, entry.JAR) {
			matches = append(matches, file)
		}
	}
	if len(matches) != 1 {
		return pluginFile{}, fmt.Errorf("expected one installed file for %s, found %d", entry.JAR, len(matches))
	}
	file := matches[0]
	if !strings.EqualFold(file.metadata.Name, entry.Name) {
		return pluginFile{}, fmt.Errorf("installed file %s no longer declares %s", file.filename, entry.Name)
	}
	// Check for manual edits before replacing or deleting a managed JAR.
	reader, err := os.Open(filepath.Join(p.server.pluginsPath, file.filename))
	if err != nil {
		return pluginFile{}, err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, reader)
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return pluginFile{}, err
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != entry.SHA256 {
		return pluginFile{}, fmt.Errorf("%s differs from its checksum in %s", file.filename, stateFile)
	}
	return file, nil
}

func (p *packagePlan) resolve(ctx context.Context, target source.Catalog, src source.Reference, visiting map[string]bool, dependency bool) error {
	key := p.state.sourceKey(src)
	previous, exists := p.state[key]
	if visiting[key] {
		return fmt.Errorf("cyclic source dependency involving %s", key)
	}
	if p.planned[key] || (dependency && exists && (src.Selector == "" || src.Selector == previous.ArtifactID)) {
		if src.Selector != "" && previous.ArtifactID != src.Selector && previous.Version != src.Selector {
			return fmt.Errorf("conflicting version constraints for %s", key)
		}
		return nil
	}
	a, err := p.server.resolveSource(ctx, target, src)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", key, err)
	}
	if a.ProjectID != "" {
		// Match registry IDs to packages that were originally installed by slug.
		key = p.state.sourceKey(source.Reference{Provider: src.Provider, Project: a.ProjectID})
		if canonical, found := p.state[key]; found {
			previous, exists = canonical, true
			src.Project = canonical.Source.Project
		} else {
			key = src.Key()
		}
	}
	if visiting[key] {
		return fmt.Errorf("cyclic source dependency involving %s", key)
	}
	if p.planned[key] {
		if previous.ArtifactID != a.ID {
			return fmt.Errorf("conflicting version constraints for %s", key)
		}
		return nil
	}
	if dependency && exists {
		if previous.ArtifactID != a.ID && previous.Source.Selector != "" {
			return fmt.Errorf("%s requires version %s but %s is pinned", key, a.Version, previous.Version)
		}
		// Keep the user's version selection when resolving a dependency.
		src = previous.Source
	}
	if exists && previous.ArtifactID == a.ID && previous.RecipeHash == a.RecipeHash && previous.Target == target && previous.Source == src {
		_, err := p.findInstalled(previous)
		return err
	}
	if p.preview && a.Build != nil {
		p.builds = append(p.builds, fmt.Sprintf("Build and install %s at %s", src.Project, a.Version))
		return nil
	}

	visiting[key] = true
	defer delete(visiting, key)
	if err := p.stage(ctx, target, src, a, ""); err != nil {
		return err
	}
	for _, required := range a.Requires {
		if err := p.resolve(ctx, target, required, visiting, true); err != nil {
			return err
		}
	}
	return nil
}

func (p *packagePlan) validate() error {
	// Evaluate dependencies against the resulting inventory, including unmanaged JARs.
	proposed := slices.Clone(p.inventory)
	changed := map[string]bool{}
	for _, change := range p.changes {
		proposed = slices.DeleteFunc(proposed, func(file pluginFile) bool { return file.filename == change.before })
		if change.staged == "" {
			continue
		}
		for _, file := range proposed {
			if strings.EqualFold(file.baseJAR, change.after.baseJAR) || strings.EqualFold(file.metadata.Name, change.after.metadata.Name) {
				return fmt.Errorf("%s conflicts with existing plugin file %s", change.after.metadata.Name, file.filename)
			}
		}
		proposed = append(proposed, change.after)
		changed[change.after.metadata.Name] = true
	}
	for _, key := range stateKeys(p.state) {
		entry := p.state[key]
		for _, required := range entry.Requires {
			dependency, exists := p.state[p.state.sourceKey(required)]
			if !exists || (required.Selector != "" && dependency.ArtifactID != required.Selector && dependency.Version != required.Selector) {
				reference := required.Key()
				if required.Selector != "" {
					reference += "@" + required.Selector
				}
				return fmt.Errorf("%s requires %s", entry.Name, reference)
			}
		}
	}
	return checkDependencies(withSourceDependencies(p.inventory, p.original), withSourceDependencies(proposed, p.state), changed)
}

func (p *packagePlan) commit() (err error) {
	type move struct{ from, to string }
	var completed []move
	defer func() {
		if err == nil {
			return
		}
		// Undo in reverse order so a replacement is removed before its backup is restored.
		for i := len(completed) - 1; i >= 0; i-- {
			m := completed[i]
			if moveErr := os.Rename(m.to, m.from); moveErr != nil {
				err = errors.Join(err, fmt.Errorf("restore %s from %s: %w", m.from, m.to, moveErr))
				p.staging = "" // Retain the backups for recovery.
			}
		}
	}()
	rename := func(from, to string) error {
		if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("target %s already exists", to)
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		completed = append(completed, move{from, to})
		return nil
	}
	for i, change := range p.changes {
		if change.before != "" {
			if err := rename(filepath.Join(p.server.pluginsPath, change.before), filepath.Join(p.staging, fmt.Sprintf("old-%d", i))); err != nil {
				return err
			}
		}
		if change.staged != "" {
			if err := rename(change.staged, filepath.Join(p.server.pluginsPath, change.after.filename)); err != nil {
				return err
			}
		}
	}
	// Save the new lock state after every staged JAR has reached its destination.
	return p.server.saveState(p.state)
}
