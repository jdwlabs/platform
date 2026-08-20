package monitoring

import (
	"context"
	"fmt"
	"net/url"
	"sort"
)

// AlertmanagerClient reads Alertmanager's v2 HTTP API.
type AlertmanagerClient struct {
	c *httpClient
}

func NewAlertmanagerClient(base string) *AlertmanagerClient {
	return &AlertmanagerClient{c: newHTTPClient(base)}
}

// Alert is one Alertmanager alert reduced to what answers "this alert reaches
// these receivers" — previously only answerable by resolving the routing
// config offline, because nothing in platformctl could read live routing
// state.
type Alert struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Receivers   []string
	State       string // "active", "suppressed", "unprocessed"
	StartsAt    string
}

type alertmanagerAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	Status      struct {
		State string `json:"state"`
	} `json:"status"`
	Receivers []struct {
		Name string `json:"name"`
	} `json:"receivers"`
}

// AlertOptions narrows which alerts Alerts returns. The zero value matches
// Alertmanager's own default view: active, unsuppressed alerts.
type AlertOptions struct {
	IncludeSilenced  bool
	IncludeInhibited bool
	Receiver         string
}

// Alerts returns the alerts Alertmanager currently holds, filtered per opts.
func (a *AlertmanagerClient) Alerts(ctx context.Context, opts AlertOptions) ([]Alert, error) {
	q := url.Values{}
	q.Set("active", "true")
	q.Set("silenced", boolStr(opts.IncludeSilenced))
	q.Set("inhibited", boolStr(opts.IncludeInhibited))
	if opts.Receiver != "" {
		q.Set("receiver", opts.Receiver)
	}

	var raw []alertmanagerAlert
	if err := a.c.get(ctx, "/api/v2/alerts?"+q.Encode(), &raw); err != nil {
		return nil, fmt.Errorf("list Alertmanager alerts: %w", err)
	}

	out := make([]Alert, 0, len(raw))
	for _, r := range raw {
		receivers := make([]string, 0, len(r.Receivers))
		for _, rc := range r.Receivers {
			receivers = append(receivers, rc.Name)
		}
		sort.Strings(receivers)
		out = append(out, Alert{
			Name:        r.Labels["alertname"],
			Labels:      r.Labels,
			Annotations: r.Annotations,
			Receivers:   receivers,
			State:       r.Status.State,
			StartsAt:    r.StartsAt,
		})
	}
	return out, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
