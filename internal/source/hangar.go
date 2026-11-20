package source

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/NomiTeam/mcpkg/internal/artifact"
)

func (c Catalog) hangar(ctx context.Context, s Reference) (Artifact, error) {
	if c.Loader != "paper" && c.Loader != "folia" {
		return Artifact{}, fmt.Errorf("Hangar requires -loader paper or folia")
	}
	if c.Minecraft == "" {
		return Artifact{}, fmt.Errorf("-minecraft is required for Hangar installs")
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
			if err := artifact.ReadJSON(ctx, endpoint+"/"+url.PathEscape(s.Selector), &v); err != nil {
				return Artifact{}, err
			}
			versions.Result = []release{v}
		} else if err := artifact.ReadJSON(ctx, fmt.Sprintf("%s?limit=25&offset=%d", endpoint, offset), &versions); err != nil {
			return Artifact{}, err
		}
		for _, v := range versions.Result {
			if s.Selector == "" && !strings.EqualFold(v.Channel.Name, "Release") {
				continue
			}
			d := v.Downloads["PAPER"]
			if d.DownloadURL != "" && d.FileInfo.SHA256Hash != "" && slices.Contains(v.PlatformDependencies["PAPER"], c.Minecraft) {
				return Artifact{Version: v.Name, ID: v.Name, URL: d.DownloadURL, Digest: "sha256:" + d.FileInfo.SHA256Hash}, nil
			}
		}
		if s.Selector != "" || len(versions.Result) < 25 {
			break
		}
	}
	return Artifact{}, fmt.Errorf("no hosted Hangar JAR for %s on Minecraft %s", s.Project, c.Minecraft)
}
