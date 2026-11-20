package manager

import (
	"archive/zip"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type pluginMetadata struct {
	Name         string
	Version      string
	Provides     []string
	Depend       []string
	Dependencies map[string]map[string]struct {
		Required *bool
	}
}

func inspectJAR(filename string) (pluginMetadata, error) {
	jar, err := zip.OpenReader(filename)
	if err != nil {
		return pluginMetadata{}, err
	}
	defer jar.Close()

	// Paper reads its own descriptor when a JAR also contains a Bukkit descriptor.
	for _, descriptor := range []string{"paper-plugin.yml", "plugin.yml"} {
		for _, file := range jar.File {
			if file.Name != descriptor {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				return pluginMetadata{}, err
			}
			var metadata pluginMetadata
			err = yaml.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&metadata)
			reader.Close()
			if err != nil {
				return pluginMetadata{}, fmt.Errorf("read %s: %w", descriptor, err)
			}
			if strings.TrimSpace(metadata.Name) == "" {
				return pluginMetadata{}, fmt.Errorf("%s has no plugin name", descriptor)
			}
			return metadata, nil
		}
	}
	return pluginMetadata{}, fmt.Errorf("JAR has no plugin.yml or paper-plugin.yml")
}

func (server Server) inventory() ([]pluginFile, error) {
	files, err := server.plugins()
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].metadata, files[i].metadataError = inspectJAR(filepath.Join(server.pluginsPath, files[i].filename))
	}
	return files, nil
}

func dependencyProblems(files []pluginFile) []string {
	provided := map[string]bool{}
	for _, file := range files {
		if file.enabled && file.metadata.Name != "" {
			provided[strings.ToLower(file.metadata.Name)] = true
			for _, alias := range file.metadata.Provides {
				provided[strings.ToLower(alias)] = true
			}
		}
	}
	var problems []string
	for _, file := range files {
		if !file.enabled {
			continue
		}
		required := slices.Clone(file.metadata.Depend)
		for _, phase := range file.metadata.Dependencies {
			for name, rule := range phase {
				if rule.Required == nil || *rule.Required {
					required = append(required, name)
				}
			}
		}
		for _, name := range required {
			if !provided[strings.ToLower(name)] {
				problems = append(problems, fmt.Sprintf("%s requires %s", file.metadata.Name, name))
			}
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems)
}

func checkDependencies(before, after []pluginFile, changed map[string]bool) error {
	prior := dependencyProblems(before)
	// Existing broken plugins should not prevent changes to unrelated plugins.
	for _, problem := range dependencyProblems(after) {
		name, _, _ := strings.Cut(problem, " requires ")
		if changed[name] || !slices.Contains(prior, problem) {
			return fmt.Errorf("missing or disabled dependency: %s", problem)
		}
	}
	return nil
}

func withSourceDependencies(files []pluginFile, state packageState) []pluginFile {
	files = slices.Clone(files)
	byJAR := make(map[string]installedPlugin, len(state))
	for _, entry := range state {
		byJAR[entry.JAR] = entry
	}
	for i := range files {
		entry := byJAR[files[i].baseJAR]
		// Registries can declare requirements that do not appear in the JAR descriptor.
		for _, required := range entry.Requires {
			dependency, exists := state[state.sourceKey(required)]
			name := required.Key()
			if exists {
				name = dependency.Name
			}
			files[i].metadata.Depend = append(slices.Clone(files[i].metadata.Depend), name)
		}
	}
	return files
}
