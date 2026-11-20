package source

import (
	"context"
	"fmt"

	"github.com/NomiTeam/mcpkg/src/internal/artifact"
)

func spigotResource(ctx context.Context, s Reference) (Artifact, error) {
	endpoint := "https://api.spiget.org/v2/resources/" + s.Project
	var resource struct{ Premium, External bool }
	if err := artifact.ReadJSON(ctx, endpoint, &resource); err != nil {
		return Artifact{}, err
	}
	if resource.Premium || resource.External {
		return Artifact{}, fmt.Errorf("Spigot resource %s requires a manual download; install its local JAR", s.Project)
	}
	selector := s.Selector
	if selector == "" {
		selector = "latest"
	}
	var version struct {
		Name string
		ID   int64
	}
	if err := artifact.ReadJSON(ctx, endpoint+"/versions/"+selector, &version); err != nil {
		return Artifact{}, err
	}
	if version.ID <= 0 {
		return Artifact{}, fmt.Errorf("invalid Spigot version for resource %s", s.Project)
	}
	// The latest endpoint can use Spiget's CDN. Pinned versions use their own URL.
	downloadURL := endpoint + "/download"
	if s.Selector != "" {
		downloadURL = fmt.Sprintf("%s/versions/%d/download", endpoint, version.ID)
	}
	return Artifact{Version: version.Name, ID: fmt.Sprint(version.ID), URL: downloadURL}, nil
}
