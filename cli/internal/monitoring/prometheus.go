package monitoring

import (
	"context"
	"fmt"
	"net/url"
)

// PrometheusClient reads Prometheus's HTTP API. It never issues a mutating
// call — Prometheus has none that matter to platformctl.
type PrometheusClient struct {
	c *httpClient
}

func NewPrometheusClient(base string) *PrometheusClient {
	return &PrometheusClient{c: newHTTPClient(base)}
}

// Target is one scrape target reduced to what answers "is this Up, and when
// did it last scrape". SamplesScraped is the second rung: a target can be Up
// while genuinely serving nothing, which JDWLABS-335 already found — health
// alone is the check that missed it.
type Target struct {
	Job                string
	Instance           string
	Health             string // "up", "down", or "unknown"
	LastScrape         string // RFC3339; empty if never scraped
	LastScrapeDuration float64
	LastError          string
	ScrapeURL          string
	// SamplesScraped is populated only when the caller asks for it (Targets
	// does not query it itself — see SamplesScraped below), because it costs
	// one extra query per target and is meaningless for a target that is
	// already Down.
	SamplesScraped int64
}

type prometheusQueryResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value  []any             `json:"value"`
			Metric map[string]string `json:"metric"`
		} `json:"result"`
	} `json:"data"`
}

type prometheusTargetsResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ActiveTargets []struct {
			Labels             map[string]string `json:"labels"`
			ScrapeURL          string            `json:"scrapeUrl"`
			LastError          string            `json:"lastError"`
			LastScrape         string            `json:"lastScrape"`
			LastScrapeDuration float64           `json:"lastScrapeDuration"`
			Health             string            `json:"health"`
		} `json:"activeTargets"`
	} `json:"data"`
}

// Targets returns every active scrape target. SamplesScraped is left zero;
// call SamplesScraped for a target explicitly, because it is an extra query
// per target and callers that only want health/lastScrape should not pay for it.
func (p *PrometheusClient) Targets(ctx context.Context) ([]Target, error) {
	var resp prometheusTargetsResponse
	if err := p.c.get(ctx, "/api/v1/targets", &resp); err != nil {
		return nil, fmt.Errorf("list Prometheus targets: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("list Prometheus targets: %s", resp.Error)
	}
	out := make([]Target, 0, len(resp.Data.ActiveTargets))
	for _, t := range resp.Data.ActiveTargets {
		health := t.Health
		if health == "" {
			health = "unknown"
		}
		out = append(out, Target{
			Job:                t.Labels["job"],
			Instance:           t.Labels["instance"],
			Health:             health,
			LastScrape:         t.LastScrape,
			LastScrapeDuration: t.LastScrapeDuration,
			LastError:          t.LastError,
			ScrapeURL:          t.ScrapeURL,
		})
	}
	return out, nil
}

// SamplesScraped reads the scrape_samples_scraped meta-metric for one
// job/instance pair — the count of samples Prometheus parsed out of that
// target's last successful scrape. A target reporting Up with zero samples is
// exactly the JDWLABS-335 shape: reachable, authenticating, and returning a
// response body with nothing in it.
func (p *PrometheusClient) SamplesScraped(ctx context.Context, job, instance string) (int64, error) {
	query := fmt.Sprintf(`scrape_samples_scraped{job=%s,instance=%s}`, quotePromString(job), quotePromString(instance))
	result, err := p.instantQuery(ctx, query)
	if err != nil {
		return 0, err
	}
	if len(result) == 0 {
		return 0, nil
	}
	return valueAsInt(result[0].Value), nil
}

// Rule is one alerting rule (recording rules are dropped — they have no state
// to report and no receiver to reach).
type Rule struct {
	Group     string
	Name      string
	Query     string
	State     string // "inactive", "pending", "firing"
	Health    string // "ok", "err", "unknown"
	LastError string
}

type prometheusRulesResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Groups []struct {
			Name  string `json:"name"`
			Rules []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Query     string `json:"query"`
				State     string `json:"state"`
				Health    string `json:"health"`
				LastError string `json:"lastError"`
			} `json:"rules"`
		} `json:"groups"`
	} `json:"data"`
}

// Rules returns every alerting rule across every rule group, in the order
// Prometheus reports them. Recording rules are excluded.
func (p *PrometheusClient) Rules(ctx context.Context) ([]Rule, error) {
	var resp prometheusRulesResponse
	if err := p.c.get(ctx, "/api/v1/rules", &resp); err != nil {
		return nil, fmt.Errorf("list Prometheus rules: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("list Prometheus rules: %s", resp.Error)
	}
	var out []Rule
	for _, g := range resp.Data.Groups {
		for _, r := range g.Rules {
			if r.Type != "alerting" {
				continue
			}
			health := r.Health
			if health == "" {
				health = "unknown"
			}
			out = append(out, Rule{
				Group:     g.Name,
				Name:      r.Name,
				Query:     r.Query,
				State:     r.State,
				Health:    health,
				LastError: r.LastError,
			})
		}
	}
	return out, nil
}

// SeriesExists reports whether a bare metric name has at least one series in
// Prometheus right now. It queries the name alone, with no label matchers —
// the question is only ever "does anything emit this metric at all", not
// whether any particular series does, because the rule's own label matchers
// are exactly the part a false-negative-prone regex extraction should not be
// trusted to reproduce.
func (p *PrometheusClient) SeriesExists(ctx context.Context, metric string) (bool, error) {
	result, err := p.instantQuery(ctx, metric)
	if err != nil {
		return false, err
	}
	return len(result) > 0, nil
}

func (p *PrometheusClient) instantQuery(ctx context.Context, query string) ([]struct {
	Value  []any             `json:"value"`
	Metric map[string]string `json:"metric"`
}, error) {
	var resp prometheusQueryResponse
	path := "/api/v1/query?" + url.Values{"query": {query}}.Encode()
	if err := p.c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("query Prometheus %q: %w", query, err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("query Prometheus %q: %s", query, resp.Error)
	}
	return resp.Data.Result, nil
}

func quotePromString(s string) string {
	return `"` + s + `"`
}

// valueAsInt reads a Prometheus instant-vector sample's [timestamp, value]
// pair. The value arrives as a JSON string (Prometheus's API encodes samples
// that way to dodge float precision loss), so this parses it defensively and
// truncates toward zero rather than erroring — a fractional sample count is
// impossible for scrape_samples_scraped, and if the value cannot be read at
// all zero is the same "assume not proven" verdict SamplesScraped's caller
// already applies to a target with no successful scrape.
func valueAsInt(v []any) int64 {
	if len(v) != 2 {
		return 0
	}
	s, ok := v[1].(string)
	if !ok {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
		return 0
	}
	return int64(f)
}
