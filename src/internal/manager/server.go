package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const disabledSuffix = ".disabled"

// Server owns the plugin directory and package configuration for one Minecraft server.
type Server struct {
	root        string
	pluginsPath string
}

// PluginStatus combines the JAR on disk with its lock entry when managed.
type PluginStatus struct {
	Status    string
	File      string
	Source    string
	Version   string
	Available string
}

type pluginFile struct {
	filename      string
	baseJAR       string
	enabled       bool
	metadata      pluginMetadata
	metadataError error
}

func NewServer(root string) Server {
	return Server{root: root, pluginsPath: filepath.Join(root, "plugins")}
}

// List shows both managed packages and JARs installed outside mcpkg.
func (server Server) List() ([]PluginStatus, error) {
	files, err := server.plugins()
	if err != nil {
		return nil, err
	}
	state, err := server.loadState()
	if err != nil {
		return nil, err
	}

	byFile := make(map[string]installedPlugin, len(state))
	for _, entry := range state {
		byFile[entry.JAR] = entry
	}
	statuses := make([]PluginStatus, 0, len(files))
	for _, file := range files {
		entry, managed := byFile[file.baseJAR]
		id := "-"
		if managed {
			id = entry.Source.Key()
		}
		statuses = append(statuses, PluginStatus{
			Status: file.status(), File: file.filename, Source: id,
			Version: entry.Version, Available: entry.Available,
		})
	}
	return statuses, nil
}

func (server Server) Check() ([]string, error) {
	files, err := server.inventory()
	if err != nil {
		return nil, err
	}
	state, err := server.loadState()
	if err != nil {
		return nil, err
	}

	problems := dependencyProblems(withSourceDependencies(files, state))
	for _, file := range files {
		if file.enabled && file.metadataError != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", file.filename, file.metadataError))
		}
	}
	return problems, nil
}

func (plugin pluginFile) status() string {
	if plugin.enabled {
		return "enabled"
	}
	return "disabled"
}

func (server Server) plugins() ([]pluginFile, error) {
	entries, err := os.ReadDir(server.pluginsPath)
	if err != nil {
		return nil, fmt.Errorf("read plugins directory: %w", err)
	}

	var plugins []pluginFile
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		filename := entry.Name()
		base, disabled := strings.CutSuffix(filename, disabledSuffix)
		if !disabled {
			// Older server directories may already use .off for disabled plugins.
			base, disabled = strings.CutSuffix(filename, ".off")
		}
		if strings.HasSuffix(base, ".jar") {
			plugins = append(plugins, pluginFile{filename: filename, baseJAR: base, enabled: !disabled})
		}
	}
	return plugins, nil
}

// SetEnabled validates the resulting inventory before changing the JAR suffix.
func (server Server) SetEnabled(filename string, enabled bool) (string, error) {
	unlock, err := server.lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	plugins, err := server.inventory()
	if err != nil {
		return "", err
	}
	state, err := server.loadState()
	if err != nil {
		return "", err
	}

	// A managed plugin can also be selected by its source ID or declared name.
	if !slices.ContainsFunc(plugins, func(plugin pluginFile) bool {
		return filename == plugin.filename || filename == plugin.baseJAR
	}) {
		key, err := state.lookup(filename)
		if err != nil {
			return "", err
		}
		filename = state[key].JAR
	}

	var match *pluginFile
	for _, plugin := range plugins {
		if filename != plugin.filename && filename != plugin.baseJAR {
			continue
		}
		if match != nil {
			return "", fmt.Errorf("multiple files match %q; inspect mcpkg list", filename)
		}
		match = &plugin
	}
	if match == nil {
		return "", fmt.Errorf("plugin file %q not found in %s", filename, server.pluginsPath)
	}
	if match.enabled == enabled {
		return match.filename, nil
	}
	if match.metadataError != nil {
		return "", fmt.Errorf("inspect %s: %w", match.filename, match.metadataError)
	}

	target := match.baseJAR
	if !enabled {
		target += disabledSuffix
	}
	sourcePath := filepath.Join(server.pluginsPath, match.filename)
	targetPath := filepath.Join(server.pluginsPath, target)
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return "", fmt.Errorf("check target %q: %w", targetPath, err)
		}
		return "", fmt.Errorf("target %q already exists", targetPath)
	}
	plugins = withSourceDependencies(plugins, state)
	proposed := slices.Clone(plugins)
	for i := range proposed {
		if proposed[i].filename == match.filename {
			proposed[i].enabled = enabled
		}
	}
	if err := checkDependencies(plugins, proposed, map[string]bool{match.metadata.Name: true}); err != nil {
		return "", err
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return "", fmt.Errorf("rename %q to %q: %w", match.filename, target, err)
	}
	return target, nil
}
