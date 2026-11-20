package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NomiTeam/mcpkg/src/internal/source"
)

type installedPlugin struct {
	Source     source.Reference
	Target     source.Catalog
	ProjectID  string
	ArtifactID string
	RecipeHash string `json:",omitempty"`
	Version    string
	Available  string
	JAR        string
	Name       string
	SHA256     string
	Requires   []source.Reference
}

type packageState map[string]installedPlugin

func (state packageState) sourceKey(src source.Reference) string {
	if src.Provider == "modrinth" {
		for key, entry := range state {
			if entry.Source.Provider == src.Provider && entry.ProjectID != "" && entry.ProjectID == src.Project {
				return key
			}
		}
	}
	return src.Key()
}

const stateFile = "mcpkg.lock"

func (server Server) statePath() string {
	return filepath.Join(server.root, stateFile)
}

func (server Server) loadState() (packageState, error) {
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
		if key != entry.Source.Key() || !source.ValidName(entry.JAR) || !strings.HasSuffix(entry.JAR, ".jar") || entry.Name == "" {
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

func (server Server) lock() (func(), error) {
	// All commands that mutate the inventory share this per-server lock.
	lockPath := filepath.Join(server.pluginsPath, ".mcpkg-lock")
	if err := os.Mkdir(lockPath, 0700); err != nil {
		return nil, fmt.Errorf("lock plugins directory: %w", err)
	}
	return func() { os.Remove(lockPath) }, nil
}

func (server Server) saveState(state packageState) error {
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
	// Publish the lock only after its full contents have been written and synced.
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
