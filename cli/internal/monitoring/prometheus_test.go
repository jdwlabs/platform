package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

const targetsJSON = `{"status":"success","data":{"activeTargets":[
	{"labels":{"job":"truenas","instance":"truenas:9633"},"scrapeUrl":"http://truenas:9633/metrics",
	 "lastScrape":"2026-08-20T10:00:00Z","lastScrapeDuration":0.05,"health":"up"},
	{"labels":{"job":"ai-sre-relay","instance":"ai-sre-relay-0:8080"},"scrapeUrl":"http://ai-sre-relay-0:8080/metrics",
	 "lastError":"connection refused","lastScrape":"2026-08-20T09:55:00Z","lastScrapeDuration":0,"health":"down"}
]}}`

func TestTargets_ParsesHealthAndTimestamps(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/targets" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(targetsJSON))
	})

	pc := NewPrometheusClient(srv.URL)
	targets, err := pc.Targets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}

	up := targets[0]
	if up.Job != "truenas" || up.Health != "up" || up.LastScrape != "2026-08-20T10:00:00Z" {
		t.Errorf("up target parsed wrong: %+v", up)
	}

	down := targets[1]
	if down.Health != "down" || down.LastError != "connection refused" {
		t.Errorf("down target parsed wrong: %+v", down)
	}
}

func TestSamplesScraped_ReadsInstantVectorValue(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, "scrape_samples_scraped") ||
			!strings.Contains(q, `job="truenas"`) || !strings.Contains(q, `instance="truenas:9633"`) {
			t.Errorf("unexpected query: %s", q)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{},"value":[1755680000,"37"]}
		]}}`))
	})

	pc := NewPrometheusClient(srv.URL)
	n, err := pc.SamplesScraped(context.Background(), "truenas", "truenas:9633")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 37 {
		t.Errorf("samples scraped = %d, want 37", n)
	}
}

func TestSamplesScraped_EmptyResultIsZero(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})

	pc := NewPrometheusClient(srv.URL)
	n, err := pc.SamplesScraped(context.Background(), "job", "instance")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("samples scraped = %d, want 0", n)
	}
}

const rulesJSON = `{"status":"success","data":{"groups":[
	{"name":"truenas.rules","rules":[
		{"type":"alerting","name":"TrueNASPoolDegraded","query":"truenas_pool_health != 0","state":"inactive","health":"ok"},
		{"type":"recording","name":"instance:node_cpu:rate5m","query":"rate(node_cpu_seconds_total[5m])"}
	]},
	{"name":"broken.rules","rules":[
		{"type":"alerting","name":"BrokenRule","query":"nonexistent_metric > 0","state":"firing","health":"err","lastError":"query timed out"}
	]}
]}}`

func TestRules_ExcludesRecordingRules(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/rules" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(rulesJSON))
	})

	pc := NewPrometheusClient(srv.URL)
	rules, err := pc.Rules(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2 (recording rule must be excluded): %+v", len(rules), rules)
	}
	if rules[0].Group != "truenas.rules" || rules[0].Name != "TrueNASPoolDegraded" {
		t.Errorf("first rule parsed wrong: %+v", rules[0])
	}
	if rules[1].Health != "err" || rules[1].LastError != "query timed out" {
		t.Errorf("second rule parsed wrong: %+v", rules[1])
	}
}

func TestSeriesExists(t *testing.T) {
	tests := []struct {
		name   string
		result string
		want   bool
	}{
		{"has series", `[{"metric":{"job":"x"},"value":[1,"1"]}]`, true},
		{"no series", `[]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":` + tt.result + `}}`))
			})
			pc := NewPrometheusClient(srv.URL)
			got, err := pc.SeriesExists(context.Background(), "some_metric")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("SeriesExists = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrometheusClient_ErrorStatusIsReported(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid parameter"}`))
	})
	pc := NewPrometheusClient(srv.URL)
	if _, err := pc.Targets(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid parameter") {
		t.Errorf("Targets error = %v, want it to name the Prometheus error", err)
	}
}
