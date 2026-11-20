package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type modrinthProject struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func searchModrinth(ctx context.Context, query, loader, gameVersion string) ([]modrinthProject, error) {
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build Modrinth search request: %w", err)
	}
	request.Header.Set("User-Agent", "NomiTeam/mcpkg (https://github.com/NomiTeam/mcpkg)")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("search Modrinth: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search Modrinth: HTTP %s", response.Status)
	}

	var result struct {
		Hits []modrinthProject `json:"hits"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("read Modrinth search results: %w", err)
	}
	return result.Hits, nil
}
