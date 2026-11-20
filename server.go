package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const disabledSuffix = ".disabled"

type serverDirectory struct {
	pluginsPath string
}

type pluginFile struct {
	filename string
	baseJAR  string
	enabled  bool
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
	lockPath := filepath.Join(server.pluginsPath, ".mcpkg-lock")
	if err := os.Mkdir(lockPath, 0700); err != nil {
		return "", fmt.Errorf("lock plugins directory: %w", err)
	}
	defer os.Remove(lockPath)

	plugins, err := server.plugins()
	if err != nil {
		return "", err
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
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return "", fmt.Errorf("rename %q to %q: %w", match.filename, target, err)
	}
	return target, nil
}
