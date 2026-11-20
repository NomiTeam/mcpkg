package source

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"strings"
)

// Reference identifies a project and an optional version or asset selection.
type Reference struct {
	Provider string
	Project  string
	Asset    string
	Selector string
}

// Artifact describes a download or a build at one resolved Git commit.
type Artifact struct {
	ProjectID  string
	Repository string
	Build      *BuildRecipe
	RecipeHash string
	Version    string
	ID         string
	URL        string
	Digest     string
	Requires   []Reference
}

type Catalog struct {
	Loader    string
	Minecraft string
}

var sourcePart = regexp.MustCompile("^[A-Za-z0-9_.-]+$")

func ValidName(name string) bool { return sourcePart.MatchString(name) }

var resourceID = regexp.MustCompile("^[0-9]+$")

func Parse(input string) (Reference, error) {
	provider, project, found := strings.Cut(input, ":")
	if !found {
		provider, project = "modrinth", provider
	}
	project, selector, pinned := strings.Cut(project, "@")
	project, asset, specifiedAsset := strings.Cut(project, "#")
	if pinned && selector == "" || specifiedAsset && asset == "" {
		return Reference{}, fmt.Errorf("empty version or asset selector in %q", input)
	}
	s := Reference{provider, project, asset, selector}
	parts := strings.Split(project, "/")
	for _, part := range parts {
		if !sourcePart.MatchString(part) || part == "." || part == ".." {
			return Reference{}, fmt.Errorf("invalid project %q", project)
		}
	}
	switch provider {
	case "github":
		if len(parts) != 2 {
			return Reference{}, fmt.Errorf("GitHub source needs owner/repository")
		}
		if _, err := path.Match(asset, ""); err != nil {
			return Reference{}, fmt.Errorf("invalid GitHub asset pattern: %w", err)
		}
	case "spigot":
		if !resourceID.MatchString(project) || (selector != "" && !resourceID.MatchString(selector)) {
			return Reference{}, fmt.Errorf("Spigot source needs numeric resource and optional version IDs")
		}
		fallthrough
	case "modrinth", "hangar":
		if len(parts) != 1 || asset != "" {
			return Reference{}, fmt.Errorf("invalid %s source %q", provider, input)
		}
	default:
		return Reference{}, fmt.Errorf("unknown source %q", provider)
	}
	return s, nil
}

func (s Reference) Key() string { return s.Provider + ":" + strings.ToLower(s.Project) }

type BuildRecipe struct {
	Command        []string `yaml:"command"`
	WindowsCommand []string `yaml:"windows-command"`
	Artifact       string   `yaml:"artifact"`
}

func (recipe BuildRecipe) Arguments() []string {
	if runtime.GOOS == "windows" && len(recipe.WindowsCommand) != 0 {
		return recipe.WindowsCommand
	}
	return recipe.Command
}

func (c Catalog) Resolve(ctx context.Context, s Reference) (Artifact, error) {
	switch s.Provider {
	case "modrinth":
		return c.modrinth(ctx, s)
	case "github":
		return githubRelease(ctx, s)
	case "hangar":
		return c.hangar(ctx, s)
	case "spigot":
		return spigotResource(ctx, s)
	default:
		return Artifact{}, fmt.Errorf("source %s cannot be updated online", s.Key())
	}
}
