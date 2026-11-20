package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type installedPlugin struct {
	Source     source
	Target     catalog
	ProjectID  string
	ArtifactID string
	Version    string
	Available  string
	JAR        string
	Name       string
	SHA256     string
	Requires   []source
}

type packageState map[string]installedPlugin

func (state packageState) sourceKey(src source) string {
	if src.Provider == "modrinth" {
		for key, entry := range state {
			if entry.Source.Provider == src.Provider && entry.ProjectID != "" && entry.ProjectID == src.Project {
				return key
			}
		}
	}
	return src.key()
}

const stateFile = "mcpkg.lock"

func (server serverDirectory) statePath() string {
	return filepath.Join(filepath.Dir(server.pluginsPath), stateFile)
}

func (server serverDirectory) loadState() (packageState, error) {
	data, err := os.ReadFile(server.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return make(packageState), nil
	}
	if err != nil {
		return nil, err
	}
	var state packageState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("read %s: %w", stateFile, err)
	}
	if state == nil {
		return nil, fmt.Errorf("invalid %s", stateFile)
	}
	seenJARs := make(map[string]bool, len(state))
	for key, entry := range state {
		if key != entry.Source.key() || !sourcePart.MatchString(entry.JAR) || !strings.HasSuffix(entry.JAR, ".jar") || entry.Name == "" {
			return nil, fmt.Errorf("invalid package %q in %s", key, stateFile)
		}
		jar := strings.ToLower(entry.JAR)
		if seenJARs[jar] {
			return nil, fmt.Errorf("duplicate managed JAR %s in %s", entry.JAR, stateFile)
		}
		seenJARs[jar] = true
	}
	return state, nil
}

func (server serverDirectory) lock() (func(), error) {
	lockPath := filepath.Join(server.pluginsPath, ".mcpkg-lock")
	if err := os.Mkdir(lockPath, 0700); err != nil {
		return nil, fmt.Errorf("lock plugins directory: %w", err)
	}
	return func() { os.Remove(lockPath) }, nil
}

func (server serverDirectory) saveState(state packageState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(server.statePath()), ".mcpkg-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(data, '\n'))
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), server.statePath())
}

func stateKeys(state packageState) []string {
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (state packageState) lookup(name string) (string, error) {
	name = strings.ToLower(name)
	if _, ok := state[name]; ok {
		return name, nil
	}
	var key string
	for id, entry := range state {
		if strings.ToLower(entry.Source.Project) != name && strings.ToLower(entry.Name) != name && strings.ToLower(entry.JAR) != name {
			continue
		}
		if key != "" {
			return "", fmt.Errorf("ambiguous package %q; use its source ID", name)
		}
		key = id
	}
	if key == "" {
		return "", fmt.Errorf("%q is not managed by mcpkg", name)
	}
	return key, nil
}

type jarChange struct {
	before string
	after  pluginFile
	staged string
}

type packagePlan struct {
	server    serverDirectory
	state     packageState
	inventory []pluginFile
	original  packageState
	staging   string
	changes   []jarChange
	planned   map[string]bool
}

func (p *packagePlan) stage(ctx context.Context, target catalog, src source, a artifact, local string) error {
	key := src.key()
	staged := filepath.Join(p.staging, fmt.Sprintf("new-%d", len(p.changes)))
	var checksum string
	var err error
	if local != "" {
		file, openErr := os.Open(local)
		if openErr != nil {
			return openErr
		}
		checksum, err = writeArtifact(staged, file, "")
		file.Close()
	} else {
		checksum, err = downloadArtifact(ctx, staged, a)
	}
	if err != nil {
		return fmt.Errorf("stage %s: %w", key, err)
	}
	metadata, err := inspectJAR(staged)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", key, err)
	}
	if local != "" {
		src.Project = strings.ToLower(metadata.Name)
		if !sourcePart.MatchString(src.Project) {
			return fmt.Errorf("invalid local plugin name %q", metadata.Name)
		}
		key = src.key()
		a.Version, a.ID = metadata.Version, checksum
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
		change.before = file.filename
		change.after.filename = file.filename
		change.after.baseJAR = file.baseJAR
		change.after.enabled = file.enabled
	}
	p.changes = append(p.changes, change)
	p.state[key] = installedPlugin{Source: src, Target: target, ProjectID: a.ProjectID, ArtifactID: a.ID, Version: a.Version,
		JAR: change.after.baseJAR, Name: metadata.Name, SHA256: checksum, Requires: a.Requires}
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

func (p *packagePlan) resolve(ctx context.Context, target catalog, src source, visiting map[string]bool, dependency bool) error {
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
	a, err := target.resolve(ctx, src)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", key, err)
	}
	if a.ProjectID != "" {
		key = p.state.sourceKey(source{Provider: src.Provider, Project: a.ProjectID})
		if canonical, found := p.state[key]; found {
			previous, exists = canonical, true
			src.Project = canonical.Source.Project
		} else {
			key = src.key()
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
		src = previous.Source
	}
	if exists && previous.ArtifactID == a.ID && previous.Target == target && previous.Source == src {
		_, err := p.findInstalled(previous)
		return err
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
				reference := required.key()
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
		for i := len(completed) - 1; i >= 0; i-- {
			m := completed[i]
			if moveErr := os.Rename(m.to, m.from); moveErr != nil {
				err = errors.Join(err, fmt.Errorf("restore %s from %s: %w", m.from, m.to, moveErr))
				p.staging = ""
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
	return p.server.saveState(p.state)
}

func (server serverDirectory) manage(ctx context.Context, command, argument string, target catalog, preview bool, output io.Writer) error {
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
		available := 0
		for _, key := range stateKeys(state) {
			entry := state[key]
			if entry.Source.Selector != "" || entry.Source.Provider == "local" {
				continue
			}
			a, err := entry.Target.resolve(ctx, entry.Source)
			if err != nil {
				return fmt.Errorf("check %s: %w", key, err)
			}
			entry.Available = ""
			if a.ID != entry.ArtifactID {
				entry.Available = a.Version
				available++
				fmt.Fprintf(output, "%s: %s -> %s\n", key, entry.Version, a.Version)
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
	plan := packagePlan{server: server, state: state, inventory: inventory, original: original, staging: staging, planned: make(map[string]bool)}
	defer func() {
		if plan.staging != "" {
			os.RemoveAll(plan.staging)
		}
	}()

	switch command {
	case "install":
		if local, ok := strings.CutPrefix(argument, "file:"); ok {
			if local == "" {
				return fmt.Errorf("file: requires a local JAR path")
			}
			if err := plan.stage(ctx, target, source{Provider: "local"}, artifact{}, local); err != nil {
				return err
			}
		} else {
			src, err := parseSource(argument)
			if err != nil {
				return err
			}
			if err := plan.resolve(ctx, target, src, make(map[string]bool), false); err != nil {
				return err
			}
		}
	case "remove":
		key, err := state.lookup(argument)
		if err != nil {
			return err
		}
		file, err := plan.findInstalled(state[key])
		if err != nil {
			return err
		}
		plan.changes = append(plan.changes, jarChange{before: file.filename})
		delete(state, key)
	case "upgrade":
		keys := stateKeys(state)
		if argument != "" {
			key, err := state.lookup(argument)
			if err != nil {
				return err
			}
			keys = []string{key}
		}
		for _, key := range keys {
			entry := state[key]
			if entry.Source.Selector != "" || entry.Source.Provider == "local" {
				continue
			}
			if err := plan.resolve(ctx, entry.Target, entry.Source, make(map[string]bool), false); err != nil {
				return err
			}
		}
	}
	if len(plan.changes) == 0 {
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
