package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jdwlabs/platform/internal/k8s"
)

func newAlertsServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func runAlerts(t *testing.T, prometheusURL, alertmanagerURL string, args ...string) (string, error) {
	t.Helper()
	kc := k8s.NewFake()
	root := NewRootForTest(kc, nil)
	full := append([]string{}, args...)
	if prometheusURL != "" {
		full = append(full, "--prometheus-addr", prometheusURL)
	}
	if alertmanagerURL != "" {
		full = append(full, "--alertmanager-addr", alertmanagerURL)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(full)
	err := root.Execute()
	return out.String(), err
}

// ---------------------------------------------------------------------------
// alerts list
// ---------------------------------------------------------------------------

// One alert routed to both receivers, readable through platformctl rather
// than resolved offline against the config tree.
const twoReceiverAlert = `[
	{"labels":{"alertname":"TrueNASPoolDegraded","severity":"critical"},
	 "annotations":{"summary":"pool tank is degraded"},
	 "startsAt":"2026-08-20T09:00:00Z",
	 "status":{"state":"active"},
	 "receivers":[{"name":"discord"},{"name":"ai-sre"}]}
]`

func TestAlertsList_ReportsReceivers(t *testing.T) {
	var gotQuery string
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(twoReceiverAlert))
	})

	out, err := runAlerts(t, "", srv.URL, "cluster", "alerts", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "TrueNASPoolDegraded") || !strings.Contains(out, "ai-sre|discord") {
		t.Fatalf("receivers not reported:\n%s", out)
	}
	if strings.Contains(gotQuery, "silenced=true") || strings.Contains(gotQuery, "inhibited=true") {
		t.Errorf("default list must not request silenced/inhibited alerts: %s", gotQuery)
	}
}

func TestAlertsList_NoActiveAlertsReportsCleanly(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	out, err := runAlerts(t, "", srv.URL, "cluster", "alerts", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no active alerts") {
		t.Fatalf("empty alert list must say so definitively:\n%s", out)
	}
}

func TestAlertsListJSON_EmitsOneEventPerAlertPlusSummary(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(twoReceiverAlert))
	})
	out, err := runAlerts(t, "", srv.URL, "--json", "cluster", "alerts", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 1 alert event plus a summary, got %d:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, `{"ts":`) || !strings.Contains(l, `"phase":"alerts"`) {
			t.Fatalf("not a platformctl event: %s", l)
		}
	}
}

// ---------------------------------------------------------------------------
// alerts targets
// ---------------------------------------------------------------------------

func promHandler(t *testing.T, targetsBody string, samplesResponder func(job, instance string) string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/targets":
			_, _ = w.Write([]byte(targetsBody))
		case "/api/v1/query":
			q := r.URL.Query().Get("query")
			job, instance := extractJobInstance(q)
			_, _ = w.Write([]byte(samplesResponder(job, instance)))
		case "/api/v1/rules":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// extractJobInstance pulls job="..." and instance="..." out of a
// scrape_samples_scraped{job="x",instance="y"} query string for the test
// server to route on — a small stand-in for a real label matcher parse.
func extractJobInstance(q string) (job, instance string) {
	job = between(q, `job="`, `"`)
	instance = between(q, `instance="`, `"`)
	return
}

func between(s, prefix, suffix string) string {
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	j := strings.Index(rest, suffix)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

const targetsUpAndDown = `{"status":"success","data":{"activeTargets":[
	{"labels":{"job":"truenas","instance":"truenas:9633"},"scrapeUrl":"http://truenas:9633/metrics",
	 "lastScrape":"2026-08-20T10:00:00Z","lastScrapeDuration":0.05,"health":"up"},
	{"labels":{"job":"ai-sre-relay","instance":"ai-sre-relay-0:8080"},"scrapeUrl":"http://ai-sre-relay-0:8080/metrics",
	 "lastError":"connection refused","lastScrape":"2026-08-20T09:55:00Z","lastScrapeDuration":0,"health":"down"}
]}}`

func TestAlertsTargets_UpWithSamplesIsHealthy(t *testing.T) {
	srv := newAlertsServer(t, promHandler(t, targetsUpAndDown, func(job, instance string) string {
		return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"42"]}]}}`
	}))

	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "targets")
	// The ai-sre-relay target is Down, so the command must still exit non-zero
	// even though truenas itself is healthy.
	if err == nil {
		t.Fatalf("expected a non-zero exit for the down target:\n%s", out)
	}
	if !strings.Contains(out, "truenas") || !strings.Contains(out, "up") {
		t.Fatalf("healthy target not reported:\n%s", out)
	}
	if !strings.Contains(out, "down") {
		t.Fatalf("down target not reported:\n%s", out)
	}
}

func TestAlertsTargets_UpWithZeroSamplesIsUnhealthy(t *testing.T) {
	// Reachable and reporting Up while serving nothing: health alone would
	// call this target fine.
	upOnly := `{"status":"success","data":{"activeTargets":[
		{"labels":{"job":"truenas","instance":"truenas:9633"},"scrapeUrl":"http://truenas:9633/metrics",
		 "lastScrape":"2026-08-20T10:00:00Z","lastScrapeDuration":0.05,"health":"up"}
	]}}`
	srv := newAlertsServer(t, promHandler(t, upOnly, func(job, instance string) string {
		return `{"status":"success","data":{"resultType":"vector","result":[]}}`
	}))

	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "targets", "--fields", "job,health,samplesScraped")
	if err == nil {
		t.Fatalf("a Up target with zero samples must exit non-zero:\n%s", out)
	}
	if !strings.Contains(out, "0") {
		t.Fatalf("samplesScraped=0 must be reported:\n%s", out)
	}
}

func TestAlertsTargets_SkipSamplesCheckAvoidsTheExtraQuery(t *testing.T) {
	upOnly := `{"status":"success","data":{"activeTargets":[
		{"labels":{"job":"truenas","instance":"truenas:9633"},"scrapeUrl":"http://truenas:9633/metrics",
		 "lastScrape":"2026-08-20T10:00:00Z","lastScrapeDuration":0.05,"health":"up"}
	]}}`
	queried := false
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query" {
			queried = true
		}
		_, _ = w.Write([]byte(upOnly))
	})

	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "targets", "--skip-samples-check", "--full")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if queried {
		t.Error("--skip-samples-check must not issue the samples query")
	}
	if !strings.Contains(out, "skipped") {
		t.Errorf("samplesScraped must report as skipped:\n%s", out)
	}
}

func TestAlertsTargets_JobFilter(t *testing.T) {
	srv := newAlertsServer(t, promHandler(t, targetsUpAndDown, func(job, instance string) string {
		return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`
	}))
	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "targets", "--job", "truenas")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if strings.Contains(out, "ai-sre-relay") {
		t.Errorf("--job truenas must exclude ai-sre-relay:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// alerts rules
// ---------------------------------------------------------------------------

const mixedRules = `{"status":"success","data":{"groups":[
	{"name":"truenas.rules","rules":[
		{"type":"alerting","name":"TrueNASPoolDegraded","query":"truenas_pool_health != 0","state":"inactive","health":"ok"},
		{"type":"recording","name":"instance:node_cpu:rate5m","query":"rate(node_cpu_seconds_total[5m])"}
	]},
	{"name":"broken.rules","rules":[
		{"type":"alerting","name":"BrokenSeriesRule","query":"nonexistent_metric > 0","state":"inactive","health":"ok"}
	]}
]}}`

func TestAlertsRules_ExcludesRecordingRulesAndFlagsMissingSeries(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/rules":
			_, _ = w.Write([]byte(mixedRules))
		case "/api/v1/query":
			q := r.URL.Query().Get("query")
			if q == "truenas_pool_health" {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
				return
			}
			// nonexistent_metric, and anything else: no series.
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "rules")
	if err == nil {
		t.Fatalf("a rule with a missing series must exit non-zero:\n%s", out)
	}
	if strings.Contains(out, "instance:node_cpu:rate5m") {
		t.Errorf("recording rule must be excluded:\n%s", out)
	}
	if !strings.Contains(out, "TrueNASPoolDegraded") || !strings.Contains(out, "found") {
		t.Errorf("healthy rule's series status not reported as found:\n%s", out)
	}
	if !strings.Contains(out, "BrokenSeriesRule") || !strings.Contains(out, "missing") {
		t.Errorf("rule with no series must be reported as missing:\n%s", out)
	}
}

func TestAlertsRules_SkipSeriesCheckReportsUnknownAndPasses(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query" {
			t.Error("--skip-series-check must not issue any series query")
		}
		_, _ = w.Write([]byte(mixedRules))
	})

	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "rules", "--skip-series-check")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("skipped series check must report unknown, not missing:\n%s", out)
	}
}

func TestAlertsRules_GroupFilter(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(mixedRules))
	})
	out, err := runAlerts(t, srv.URL, "", "cluster", "alerts", "rules", "--group", "truenas.rules")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if strings.Contains(out, "BrokenSeriesRule") {
		t.Errorf("--group truenas.rules must exclude broken.rules:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// field validation, shared with all three subcommands
// ---------------------------------------------------------------------------

func TestAlerts_FieldsAndFullAreMutuallyExclusive(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	out, err := runAlerts(t, "", srv.URL, "cluster", "alerts", "list", "--fields", "alertname", "--full")
	if err == nil {
		t.Fatalf("expected an error for --fields with --full:\n%s", out)
	}
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("error must name the conflict:\n%s", out)
	}
}

func TestAlerts_UnknownFieldIsRejected(t *testing.T) {
	srv := newAlertsServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	out, err := runAlerts(t, "", srv.URL, "cluster", "alerts", "list", "--fields", "bogus")
	if err == nil {
		t.Fatalf("expected an error for an unknown field:\n%s", out)
	}
	if !strings.Contains(out, "unknown alert field bogus") {
		t.Errorf("error must name the field and kind:\n%s", out)
	}
}

func TestAlertsRulesDryRunIsRejected(t *testing.T) {
	out, err := runAlerts(t, "", "", "cluster", "alerts", "rules", "--dry-run")
	if err == nil {
		t.Fatalf("expected --dry-run to be rejected as an unknown flag:\n%s", out)
	}
	if !strings.Contains(out, "unknown flag: --dry-run") {
		t.Errorf("rejection must name the flag:\n%s", out)
	}
}
