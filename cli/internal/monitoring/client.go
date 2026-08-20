// Package monitoring reads live state from Prometheus and Alertmanager over
// their HTTP APIs.
//
// This is the read path AGENTS.md's binary contract needed and did not have:
// cluster status only asserted that the alertmanager-config Secret exists,
// which says nothing about whether an alert fires, where it routes, or
// whether the metric a rule references is ever scraped at all. Nothing here
// mutates — both APIs are read-only from platformctl's side.
package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxErrorBody = 400

// httpClient does one GET against base+path and decodes the JSON body into
// out. Shared by the Prometheus and Alertmanager clients, which otherwise
// speak unrelated response envelopes.
type httpClient struct {
	base string
	http *http.Client
}

func newHTTPClient(base string) *httpClient {
	return &httpClient{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *httpClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected response from %s: HTTP %d: %s", c.base, resp.StatusCode, trimBody(body))
	}
	if readErr != nil {
		return fmt.Errorf("read response from %s%s: %w", c.base, path, readErr)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response from %s%s: %w", c.base, path, err)
	}
	return nil
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(empty response)"
	}
	if len(s) > maxErrorBody {
		return s[:maxErrorBody] + fmt.Sprintf("... (truncated, %d bytes total)", len(s))
	}
	return s
}
