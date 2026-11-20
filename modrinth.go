package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	var result struct {
		Hits []modrinthProject `json:"hits"`
	}
	if err := getJSON(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("search Modrinth: %w", err)
	}
	return result.Hits, nil
}
