package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The fixture reproduces the shape that matters for routing evidence: one
// alert routed to both receivers, produced live rather than resolved offline
// against the config tree.
const alertsJSON = `[
	{"labels":{"alertname":"TrueNASPoolDegraded","severity":"critical"},
	 "annotations":{"summary":"pool tank is degraded"},
	 "startsAt":"2026-08-20T09:00:00Z",
	 "status":{"state":"active"},
	 "receivers":[{"name":"discord"},{"name":"ai-sre"}]},
	{"labels":{"alertname":"NoReceiverAlert","severity":"warning"},
	 "annotations":{},
	 "startsAt":"2026-08-20T09:05:00Z",
	 "status":{"state":"active"},
	 "receivers":[]}
]`

func TestAlerts_ParsesReceiversSorted(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(alertsJSON))
	}))
	t.Cleanup(srv.Close)

	ac := NewAlertmanagerClient(srv.URL)
	alerts, err := ac.Alerts(context.Background(), AlertOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2", len(alerts))
	}

	a := alerts[0]
	if a.Name != "TrueNASPoolDegraded" {
		t.Errorf("name = %q", a.Name)
	}
	if len(a.Receivers) != 2 || a.Receivers[0] != "ai-sre" || a.Receivers[1] != "discord" {
		t.Errorf("receivers = %v, want sorted [ai-sre discord]", a.Receivers)
	}
	if a.State != "active" {
		t.Errorf("state = %q, want active", a.State)
	}

	if len(alerts[1].Receivers) != 0 {
		t.Errorf("unrouted alert must report zero receivers, got %v", alerts[1].Receivers)
	}

	// Default options must ask Alertmanager to exclude silenced/inhibited
	// alerts, not merely filter them client-side.
	if gotQuery != "active=true&inhibited=false&silenced=false" {
		t.Errorf("query = %q, want the default active/unsilenced/uninhibited filter", gotQuery)
	}
}

func TestAlerts_IncludeSilencedAndInhibited(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	ac := NewAlertmanagerClient(srv.URL)
	if _, err := ac.Alerts(context.Background(), AlertOptions{IncludeSilenced: true, IncludeInhibited: true, Receiver: "discord"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "active=true&inhibited=true&receiver=discord&silenced=true"
	if gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}
