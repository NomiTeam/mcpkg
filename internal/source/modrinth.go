package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/NomiTeam/mcpkg/internal/artifact"
)

type modrinthProject struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func SearchModrinth(ctx context.Context, query, loader, gameVersion string) ([]modrinthProject, error) {
	facets := [][]string{{"project_type:plugin"}, {"categories:" + loader}}
	if gameVersion != "" {
		facets = append(facets, []string{"versions:" + gameVersion})
	}
	encodedFacets, err := json.Marshal(facets)
	if err != nil {
		return nil, fmt.Errorf("encode Modrinth search filters: %w", err)
	}
	parameters := url.Values{"query": {query}, "facets": {string(encodedFacets)}}
	endpoint := "https://api.modrinth.com/v2/search?" + parameters.Encode()
	var result struct {
		Hits []modrinthProject `json:"hits"`
	}
	if err := artifact.ReadJSON(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("search Modrinth: %w", err)
	}
	return result.Hits, nil
}

func (c Catalog) modrinth(ctx context.Context, s Reference) (Artifact, error) {
	if c.Minecraft == "" {
		return Artifact{}, fmt.Errorf("-minecraft is required for Modrinth installs")
	}
	parameters := url.Values{
		"loaders":       {fmt.Sprintf("[%q]", c.Loader)},
		"game_versions": {fmt.Sprintf("[%q]", c.Minecraft)},
	}
	var versions []struct {
		ID        string
		ProjectID string    `json:"project_id"`
		Number    string    `json:"version_number"`
		Type      string    `json:"version_type"`
		Published time.Time `json:"date_published"`
		Files     []struct {
			URL, Filename string
			Primary       bool
			Hashes        map[string]string
		}
		Dependencies []struct {
			Project  string `json:"project_id"`
			Version  string `json:"version_id"`
			Type     string `json:"dependency_type"`
			Filename string `json:"file_name"`
		}
	}
	endpoint := "https://api.modrinth.com/v2/project/" + url.PathEscape(s.Project) + "/version?" + parameters.Encode()
	if err := artifact.ReadJSON(ctx, endpoint, &versions); err != nil {
		return Artifact{}, err
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].Published.After(versions[j].Published) })
	for _, v := range versions {
		if s.Selector == "" && v.Type != "release" || s.Selector != "" && s.Selector != v.ID && s.Selector != v.Number {
			continue
		}
		var a Artifact
		for _, f := range v.Files {
			if !strings.HasSuffix(f.Filename, ".jar") || f.Hashes["sha512"] == "" {
				continue
			}
			if a.URL == "" || f.Primary {
				a = Artifact{ProjectID: v.ProjectID, Version: v.Number, ID: v.ID, URL: f.URL, Digest: "sha512:" + f.Hashes["sha512"]}
			}
			if f.Primary {
				break
			}
		}
		if a.URL == "" {
			continue
		}
		for _, d := range v.Dependencies {
			if d.Type != "required" {
				continue
			}
			if d.Project == "" && d.Version != "" {
				var version struct {
					Project string `json:"project_id"`
				}
				if err := artifact.ReadJSON(ctx, "https://api.modrinth.com/v2/version/"+url.PathEscape(d.Version), &version); err != nil {
					return Artifact{}, err
				}
				d.Project = version.Project
			}
			if d.Project == "" {
				return Artifact{}, fmt.Errorf("%s requires an external file %q; install it manually", s.Project, d.Filename)
			}
			a.Requires = append(a.Requires, Reference{Provider: "modrinth", Project: d.Project, Selector: d.Version})
		}
		return a, nil
	}
	return Artifact{}, fmt.Errorf("no compatible Modrinth JAR for %s on %s %s", s.Project, c.Loader, c.Minecraft)
}
