package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

type source struct {
	Provider string
	Project  string
	Asset    string
	Selector string
}

type artifact struct {
	ProjectID string
	Version   string
	ID        string
	URL       string
	Digest    string
	Requires  []source
}

type catalog struct {
	Loader    string
	Minecraft string
}

var sourcePart = regexp.MustCompile("^[A-Za-z0-9_.-]+$")
var resourceID = regexp.MustCompile("^[0-9]+$")

func parseSource(input string) (source, error) {
	provider, project, found := strings.Cut(input, ":")
	if !found {
		provider, project = "modrinth", provider
	}
	project, selector, pinned := strings.Cut(project, "@")
	project, asset, specifiedAsset := strings.Cut(project, "#")
	if pinned && selector == "" || specifiedAsset && asset == "" {
		return source{}, fmt.Errorf("empty version or asset selector in %q", input)
	}
	s := source{provider, project, asset, selector}
	parts := strings.Split(project, "/")
	for _, part := range parts {
		if !sourcePart.MatchString(part) || part == "." || part == ".." {
			return source{}, fmt.Errorf("invalid project %q", project)
		}
	}
	switch provider {
	case "github":
		if len(parts) != 2 {
			return source{}, fmt.Errorf("GitHub source needs owner/repository")
		}
		if _, err := path.Match(asset, ""); err != nil {
			return source{}, fmt.Errorf("invalid GitHub asset pattern: %w", err)
		}
	case "spigot":
		if !resourceID.MatchString(project) || (selector != "" && !resourceID.MatchString(selector)) {
			return source{}, fmt.Errorf("Spigot source needs numeric resource and optional version IDs")
		}
		fallthrough
	case "modrinth", "hangar":
		if len(parts) != 1 || asset != "" {
			return source{}, fmt.Errorf("invalid %s source %q", provider, input)
		}
	default:
		return source{}, fmt.Errorf("unknown source %q", provider)
	}
	return s, nil
}

func (s source) key() string { return s.Provider + ":" + strings.ToLower(s.Project) }

func request(ctx context.Context, endpoint string) (*http.Response, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("invalid HTTPS URL %q", endpoint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NomiTeam/mcpkg (https://github.com/NomiTeam/mcpkg)")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %s", response.Request.URL, response.Status)
	}
	if response.Request.URL.Scheme != "https" {
		response.Body.Close()
		return nil, fmt.Errorf("%s redirected to an insecure URL", endpoint)
	}
	return response, nil
}

func getJSON(ctx context.Context, endpoint string, target any) error {
	response, err := request(ctx, endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

func (c catalog) resolve(ctx context.Context, s source) (artifact, error) {
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
		return artifact{}, fmt.Errorf("source %s cannot be updated online", s.key())
	}
}

func (c catalog) modrinth(ctx context.Context, s source) (artifact, error) {
	if c.Minecraft == "" {
		return artifact{}, fmt.Errorf("-minecraft is required for Modrinth installs")
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
	if err := getJSON(ctx, endpoint, &versions); err != nil {
		return artifact{}, err
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].Published.After(versions[j].Published) })
	for _, v := range versions {
		if s.Selector == "" && v.Type != "release" || s.Selector != "" && s.Selector != v.ID && s.Selector != v.Number {
			continue
		}
		var a artifact
		for _, f := range v.Files {
			if !strings.HasSuffix(f.Filename, ".jar") || f.Hashes["sha512"] == "" {
				continue
			}
			if a.URL == "" || f.Primary {
				a = artifact{ProjectID: v.ProjectID, Version: v.Number, ID: v.ID, URL: f.URL, Digest: "sha512:" + f.Hashes["sha512"]}
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
				if err := getJSON(ctx, "https://api.modrinth.com/v2/version/"+url.PathEscape(d.Version), &version); err != nil {
					return artifact{}, err
				}
				d.Project = version.Project
			}
			if d.Project == "" {
				return artifact{}, fmt.Errorf("%s requires an external file %q; install it manually", s.Project, d.Filename)
			}
			a.Requires = append(a.Requires, source{Provider: "modrinth", Project: d.Project, Selector: d.Version})
		}
		return a, nil
	}
	return artifact{}, fmt.Errorf("no compatible Modrinth JAR for %s on %s %s", s.Project, c.Loader, c.Minecraft)
}

func githubRelease(ctx context.Context, s source) (artifact, error) {
	endpoint := "https://api.github.com/repos/" + s.Project + "/releases/latest"
	if s.Selector != "" {
		endpoint = "https://api.github.com/repos/" + s.Project + "/releases/tags/" + url.PathEscape(s.Selector)
	}
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			ID           int64
			Name, Digest string
			URL          string `json:"browser_download_url"`
		}
	}
	if err := getJSON(ctx, endpoint, &release); err != nil {
		return artifact{}, err
	}
	var matches []artifact
	for _, asset := range release.Assets {
		matched, _ := path.Match(s.Asset, asset.Name)
		if strings.HasSuffix(asset.Name, ".jar") && (s.Asset == "" || matched) {
			matches = append(matches, artifact{Version: release.Tag, ID: fmt.Sprintf("%s/%d", release.Tag, asset.ID), URL: asset.URL, Digest: asset.Digest})
		}
	}
	if len(matches) != 1 {
		return artifact{}, fmt.Errorf("%s %s has %d matching JAR assets; specify #<asset-glob>", s.Project, release.Tag, len(matches))
	}
	return matches[0], nil
}

func (c catalog) hangar(ctx context.Context, s source) (artifact, error) {
	if c.Loader != "paper" && c.Loader != "folia" {
		return artifact{}, fmt.Errorf("Hangar requires -loader paper or folia")
	}
	if c.Minecraft == "" {
		return artifact{}, fmt.Errorf("-minecraft is required for Hangar installs")
	}
	type release struct {
		Name                 string
		Channel              struct{ Name string }
		PlatformDependencies map[string][]string
		Downloads            map[string]struct {
			DownloadURL string
			FileInfo    struct{ SHA256Hash string }
		}
	}
	endpoint := "https://hangar.papermc.io/api/v1/projects/" + s.Project + "/versions"
	for offset := 0; ; offset += 25 {
		var versions struct{ Result []release }
		if s.Selector != "" {
			var v release
			if err := getJSON(ctx, endpoint+"/"+url.PathEscape(s.Selector), &v); err != nil {
				return artifact{}, err
			}
			versions.Result = []release{v}
		} else if err := getJSON(ctx, fmt.Sprintf("%s?limit=25&offset=%d", endpoint, offset), &versions); err != nil {
			return artifact{}, err
		}
		for _, v := range versions.Result {
			if s.Selector == "" && !strings.EqualFold(v.Channel.Name, "Release") {
				continue
			}
			d := v.Downloads["PAPER"]
			if d.DownloadURL != "" && d.FileInfo.SHA256Hash != "" && slices.Contains(v.PlatformDependencies["PAPER"], c.Minecraft) {
				return artifact{Version: v.Name, ID: v.Name, URL: d.DownloadURL, Digest: "sha256:" + d.FileInfo.SHA256Hash}, nil
			}
		}
		if s.Selector != "" || len(versions.Result) < 25 {
			break
		}
	}
	return artifact{}, fmt.Errorf("no hosted Hangar JAR for %s on Minecraft %s", s.Project, c.Minecraft)
}

func spigotResource(ctx context.Context, s source) (artifact, error) {
	endpoint := "https://api.spiget.org/v2/resources/" + s.Project
	var resource struct{ Premium, External bool }
	if err := getJSON(ctx, endpoint, &resource); err != nil {
		return artifact{}, err
	}
	if resource.Premium || resource.External {
		return artifact{}, fmt.Errorf("Spigot resource %s requires a manual download; install its local JAR", s.Project)
	}
	selector := s.Selector
	if selector == "" {
		selector = "latest"
	}
	var version struct {
		Name string
		ID   int64
	}
	if err := getJSON(ctx, endpoint+"/versions/"+selector, &version); err != nil {
		return artifact{}, err
	}
	if version.ID <= 0 {
		return artifact{}, fmt.Errorf("invalid Spigot version for resource %s", s.Project)
	}
	downloadURL := endpoint + "/download"
	if s.Selector != "" {
		downloadURL = fmt.Sprintf("%s/versions/%d/download", endpoint, version.ID)
	}
	return artifact{Version: version.Name, ID: fmt.Sprint(version.ID), URL: downloadURL}, nil
}
