package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const disabledSuffix = ".disabled"

type serverDirectory struct {
	pluginsPath string
}

type pluginFile struct {
	filename      string
	baseJAR       string
	enabled       bool
	metadata      pluginMetadata
	metadataError error
}

func newServer(root string) serverDirectory {
	return serverDirectory{pluginsPath: filepath.Join(root, "plugins")}
}

func (plugin pluginFile) status() string {
	if plugin.enabled {
		return "enabled"
	}
	return "disabled"
}

func (server serverDirectory) plugins() ([]pluginFile, error) {
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
			base, disabled = strings.CutSuffix(filename, ".off")
		}
		if strings.HasSuffix(base, ".jar") {
			plugins = append(plugins, pluginFile{filename: filename, baseJAR: base, enabled: !disabled})
		}
	}
	return plugins, nil
}

func (server serverDirectory) setEnabled(filename string, enabled bool) (string, error) {
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
