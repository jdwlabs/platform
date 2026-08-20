package monitoring

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
)

func TestCandidateMetricNames(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "simple threshold",
			query: `truenas_pool_health != 0`,
			want:  []string{"truenas_pool_health"},
		},
		{
			name:  "wrapped in a function and a duration",
			query: `rate(ai_sre_relay_errors_total[5m]) > 0`,
			want:  []string{"ai_sre_relay_errors_total"},
		},
		{
			name:  "label matchers are excluded, metric name is not",
			query: `up{job="truenas",instance=~"truenas.*"} == 0`,
			want:  []string{"up"},
		},
		{
			name:  "aggregation keywords and __name__ are excluded",
			query: `sum by (job) (rate(scrape_samples_scraped[5m])) == 0`,
			want:  []string{"scrape_samples_scraped"},
		},
		{
			name:  "binary expression across two metrics",
			query: `node_filesystem_avail_bytes / node_filesystem_size_bytes < 0.1`,
			want:  []string{"node_filesystem_avail_bytes", "node_filesystem_size_bytes"},
		},
		{
			name:  "absent() has no metric name to extract from its own call",
			query: `absent(some_metric{job="x"})`,
			want:  []string{"some_metric"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CandidateMetricNames(tt.query)
			sort.Strings(got)
			want := append([]string(nil), tt.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("CandidateMetricNames(%q) = %v, want %v", tt.query, got, want)
			}
		})
	}
}

// stubChecker answers SeriesExists from a fixed set, so CheckRuleSeries is
// tested against the extraction+lookup contract without an HTTP server.
type stubChecker struct {
	exists map[string]bool
	err    error
}

func (s stubChecker) SeriesExists(_ context.Context, metric string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.exists[metric], nil
}

func TestCheckRuleSeries(t *testing.T) {
	ctx := context.Background()

	t.Run("found when any candidate has series", func(t *testing.T) {
		checker := stubChecker{exists: map[string]bool{"node_filesystem_avail_bytes": true}}
		status, err := CheckRuleSeries(ctx, checker,
			"node_filesystem_avail_bytes / node_filesystem_size_bytes < 0.1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status != SeriesFound {
			t.Errorf("status = %q, want found", status)
		}
	})

	t.Run("missing when no candidate has series", func(t *testing.T) {
		checker := stubChecker{exists: map[string]bool{}}
		status, err := CheckRuleSeries(ctx, checker, "nonexistent_metric > 0")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status != SeriesMissing {
			t.Errorf("status = %q, want missing", status)
		}
	})

	t.Run("unknown when nothing could be extracted", func(t *testing.T) {
		checker := stubChecker{exists: map[string]bool{}}
		status, err := CheckRuleSeries(ctx, checker, "1 > 0")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status != SeriesUnknown {
			t.Errorf("status = %q, want unknown", status)
		}
	})

	t.Run("a query failure aborts rather than reporting missing", func(t *testing.T) {
		checker := stubChecker{err: errors.New("prometheus unreachable")}
		_, err := CheckRuleSeries(ctx, checker, "some_metric > 0")
		if err == nil {
			t.Fatal("expected the query error to propagate")
		}
	})
}
