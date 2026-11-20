package source

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/NomiTeam/mcpkg/internal/artifact"
)

func githubRelease(ctx context.Context, s Reference) (Artifact, error) {
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
	if err := artifact.ReadJSON(ctx, endpoint, &release); err != nil {
		return Artifact{}, err
	}
	var matches []Artifact
	for _, asset := range release.Assets {
		matched, _ := path.Match(s.Asset, asset.Name)
		if strings.HasSuffix(asset.Name, ".jar") && (s.Asset == "" || matched) {
			matches = append(matches, Artifact{Version: release.Tag, ID: fmt.Sprintf("%s/%d", release.Tag, asset.ID), URL: asset.URL, Digest: asset.Digest})
		}
	}
	if len(matches) != 1 {
		return Artifact{}, fmt.Errorf("%s %s has %d matching JAR assets; specify #<asset-glob>", s.Project, release.Tag, len(matches))
	}
	return matches[0], nil
}
