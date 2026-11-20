package manager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NomiTeam/mcpkg/src/internal/source"
	"go.yaml.in/yaml/v3"
)

var gitCommitID = regexp.MustCompile("^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$")

type Config struct {
	Minecraft string `yaml:"minecraft"`
	Loader    string `yaml:"loader"`
}

type pluginSource struct {
	Type       string              `yaml:"type"`
	Repository string              `yaml:"repository"`
	Ref        string              `yaml:"ref"`
	Asset      string              `yaml:"asset"`
	Build      *source.BuildRecipe `yaml:"build"`
}

func readConfig(filename string, target any) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	// Reject misspelled keys so a recipe cannot silently use unintended defaults.
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s must contain a single YAML document", filename)
	}
	return nil
}

// LoadConfig reads server defaults and permits command-line-only setups.
func (server Server) LoadConfig() (Config, error) {
	var config Config
	err := readConfig(filepath.Join(server.root, "mcpkg.yml"), &config)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return config, err
	}
	if config.Loader == "" {
		config.Loader = "paper"
	}
	return config, nil
}

func (server Server) loadSource(alias string) (pluginSource, error) {
	var definition pluginSource
	if !source.ValidName(alias) || alias == "." || alias == ".." {
		return definition, fmt.Errorf("invalid source name %q", alias)
	}
	root := server.root
	filename := filepath.Join(root, "sources.d", alias+".yml")
	if err := readConfig(filename, &definition); err != nil {
		return definition, err
	}

	switch definition.Type {
	case "git":
		if definition.Repository == "" || definition.Build == nil || definition.Asset != "" {
			return definition, fmt.Errorf("%s: git source needs repository and build", filename)
		}
		if !filepath.IsAbs(definition.Repository) && !strings.Contains(definition.Repository, "://") &&
			!strings.Contains(definition.Repository, ":") {
			repository, err := filepath.Abs(filepath.Join(root, definition.Repository))
			if err != nil {
				return definition, err
			}
			definition.Repository = repository
		}
		if !filepath.IsLocal(definition.Build.Artifact) || !strings.HasSuffix(definition.Build.Artifact, ".jar") {
			return definition, fmt.Errorf("%s: build.artifact must be a relative JAR path or filename pattern", filename)
		}
		for _, command := range [][]string{definition.Build.Command, definition.Build.WindowsCommand} {
			if len(command) != 0 && strings.TrimSpace(command[0]) == "" {
				return definition, fmt.Errorf("%s: build command needs an executable", filename)
			}
		}
	case "github-release":
		if definition.Repository == "" || definition.Build != nil {
			return definition, fmt.Errorf("%s: github-release source needs an owner/repository", filename)
		}
	default:
		return definition, fmt.Errorf("%s: unknown source type %q", filename, definition.Type)
	}
	return definition, nil
}

func (server Server) installSource(argument string) (source.Reference, error) {
	// An explicitly qualified provider bypasses the configured alias lookup.
	alias, explicit := strings.CutPrefix(argument, "source:")
	if explicit || source.ValidName(alias) {
		_, err := server.loadSource(alias)
		if err == nil {
			return source.Reference{Provider: "source", Project: alias}, nil
		}
		if explicit || !errors.Is(err, os.ErrNotExist) {
			return source.Reference{}, err
		}
	}
	return source.Parse(argument)
}

func (server Server) resolveSource(ctx context.Context, target source.Catalog, src source.Reference) (source.Artifact, error) {
	if src.Provider != "source" {
		return target.Resolve(ctx, src)
	}
	definition, err := server.loadSource(src.Project)
	if err != nil {
		return source.Artifact{}, err
	}

	if definition.Type == "github-release" {
		specification := "github:" + definition.Repository
		if definition.Asset != "" {
			specification += "#" + definition.Asset
		}
		if definition.Ref != "" {
			specification += "@" + definition.Ref
		}
		release, err := source.Parse(specification)
		if err != nil {
			return source.Artifact{}, err
		}
		return target.Resolve(ctx, release)
	}

	commit, err := definition.gitCommit(ctx)
	if err != nil {
		return source.Artifact{}, err
	}
	// The recipe can change the output even when the Git commit stays the same.
	data, err := json.Marshal(definition)
	if err != nil {
		return source.Artifact{}, err
	}
	return source.Artifact{
		ID: commit, Version: commit[:12], Repository: definition.Repository,
		Build: definition.Build, RecipeHash: fmt.Sprintf("%x", sha256.Sum256(data)),
	}, nil
}

// gitCommit resolves a branch, tag, or HEAD without cloning or executing repository code.
func (definition pluginSource) gitCommit(ctx context.Context) (string, error) {
	ref := definition.Ref
	if gitCommitID.MatchString(ref) {
		return strings.ToLower(ref), nil
	}
	if ref == "" {
		ref = "HEAD"
	}
	patterns := []string{ref}
	if ref != "HEAD" && !strings.HasPrefix(ref, "refs/") {
		patterns = []string{"refs/heads/" + ref, "refs/tags/" + ref, "refs/tags/" + ref + "^{}"}
	} else if strings.HasPrefix(ref, "refs/tags/") {
		patterns = append(patterns, ref+"^{}")
	}
	args := append([]string{"ls-remote", "--exit-code", "--", definition.Repository}, patterns...)
	command := exec.CommandContext(ctx, "git", args...)
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) != 0 {
			return "", fmt.Errorf("resolve %q in %s: %s", ref, definition.Repository, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("resolve %q in %s: %w", ref, definition.Repository, err)
	}
	var commit string
	for _, pattern := range patterns {
		// Process the peeled tag last so annotated tags resolve to the commit.
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[1] != pattern || !gitCommitID.MatchString(fields[0]) {
				continue
			}
			if commit != "" && fields[0] != commit && !strings.HasSuffix(pattern, "^{}") {
				return "", fmt.Errorf("Git reference %q names both a branch and a tag; use refs/heads/ or refs/tags/", ref)
			}
			commit = fields[0]
		}
	}
	if commit == "" {
		return "", fmt.Errorf("Git reference %q did not resolve to a commit", ref)
	}
	return commit, nil
}
