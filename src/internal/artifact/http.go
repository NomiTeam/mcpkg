package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

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

func ReadJSON(ctx context.Context, endpoint string, target any) error {
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
