package truenasexporter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// fakeCaller implements truenas.Caller against canned responses keyed by
// method name, mirroring cli/internal/truenas/fake.go's shape without
// depending on it (that fake is scoped to the reclaim/inventory tests it
// already serves).
type fakeCaller struct {
	responses map[string]any
	errs      map[string]error
}

func (f *fakeCaller) Call(_ context.Context, method string, _ []any, out any) error {
	if err, ok := f.errs[method]; ok {
		return err
	}
	resp, ok := f.responses[method]
	if !ok {
		return errors.New("fakeCaller: no response configured for " + method)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (f *fakeCaller) Close() error { return nil }

// collectAll gathers every metric a Collector emits into a flat, queryable
// slice, the same shape prometheus/client_golang's own testutil works
// against.
func collectAll(t *testing.T, c *Collector) []*dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 256)
	c.Collect(ch)
	close(ch)

	var out []*dto.Metric
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		out = append(out, &pb)
	}
	return out
}

func labelValue(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

func findMetric(metrics []*dto.Metric, labels map[string]string) *dto.Metric {
	for _, m := range metrics {
		match := true
		for k, v := range labels {
			if labelValue(m, k) != v {
				match = false
				break
			}
		}
		if match {
			return m
		}
	}
	return nil
}

func gaugeValue(m *dto.Metric) float64 {
	if m == nil || m.Gauge == nil {
		return -1
	}
	return m.Gauge.GetValue()
}

// TestPoolState_OnlineAndDegraded fixes the two-state convention the deleted
// TrueNASPoolDegraded/TrueNASPoolNotOnline rules already assumed (see
// tenants/platform/services/kube-prometheus-stack/postInstall/rules-truenas.yaml
// history), against the exact status string ADR-0025 live-verified
// (storage -> status=ONLINE).
func TestPoolState_OnlineAndDegraded(t *testing.T) {
	fc := &fakeCaller{responses: map[string]any{
		"pool.query": []map[string]any{
			{"name": "storage", "status": "ONLINE"},
			{"name": "scratch", "status": "DEGRADED"},
		},
	}}
	c := New(fc, nil)
	metrics := collectAll(t, c)

	if v := gaugeValue(findMetric(metrics, map[string]string{"pool": "storage", "state": "online"})); v != 1 {
		t.Errorf("storage online = %v, want 1", v)
	}
	if v := gaugeValue(findMetric(metrics, map[string]string{"pool": "storage", "state": "degraded"})); v != 0 {
		t.Errorf("storage degraded = %v, want 0", v)
	}
	if v := gaugeValue(findMetric(metrics, map[string]string{"pool": "scratch", "state": "online"})); v != 0 {
		t.Errorf("scratch online = %v, want 0", v)
	}
	if v := gaugeValue(findMetric(metrics, map[string]string{"pool": "scratch", "state": "degraded"})); v != 1 {
		t.Errorf("scratch degraded = %v, want 1", v)
	}
}

// TestDatasetCapacity_MatchesADRLiveEvidence reproduces the exact used/
// available bytes docs/adr/0025's evidence table recorded for pool.dataset.query
// (used=83935377792, available=38523525645952), so a future change to
// parseZFSBytes or the query shape is checked against a real captured value
// rather than an invented one.
func TestDatasetCapacity_MatchesADRLiveEvidence(t *testing.T) {
	fc := &fakeCaller{responses: map[string]any{
		"pool.query": []map[string]any{{"name": "storage", "status": "ONLINE"}},
		"pool.dataset.query": []map[string]any{
			{
				"id":        "storage",
				"name":      "storage",
				"used":      map[string]any{"parsed": 83935377792.0},
				"available": map[string]any{"parsed": 38523525645952.0},
			},
		},
	}}
	c := New(fc, nil)
	metrics := collectAll(t, c)

	used := findMetric(metrics, map[string]string{"pool": "storage", "dataset": "storage"})
	// findMetric can't distinguish the used and available families by label
	// alone (both share pool/dataset labels); walk the raw channel output
	// instead via a second, family-aware pass.
	_ = used

	ch := make(chan prometheus.Metric, 256)
	c.Collect(ch)
	close(ch)

	var gotUsed, gotAvail float64 = -1, -1
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		desc := m.Desc().String()
		switch {
		case strings.Contains(desc, "truenas_zfs_dataset_used_bytes"):
			gotUsed = pb.Gauge.GetValue()
		case strings.Contains(desc, "truenas_zfs_dataset_available_bytes"):
			gotAvail = pb.Gauge.GetValue()
		}
	}

	if gotUsed != 83935377792 {
		t.Errorf("dataset used bytes = %v, want 83935377792", gotUsed)
	}
	if gotAvail != 38523525645952 {
		t.Errorf("dataset available bytes = %v, want 38523525645952", gotAvail)
	}
}

// TestDiskTemperatures_MatchesADRLiveEvidence reproduces the exact
// disk.temperatures shape and values docs/adr/0025 captured live
// ({sdb: 36.0, sdd: 37.0, sdc: 38.0, sda: 36.0, nvme0n1: 37.85}), including
// that a null entry (a disk with no reading) must be skipped, not zeroed.
func TestDiskTemperatures_MatchesADRLiveEvidence(t *testing.T) {
	fc := &fakeCaller{responses: map[string]any{
		"disk.temperatures": map[string]any{
			"sda":     36.0,
			"sdb":     36.0,
			"sdc":     38.0,
			"sdd":     37.0,
			"nvme0n1": 37.85,
			"sde":     nil, // disk present but no reading
		},
	}}
	c := New(fc, nil)
	metrics := collectAll(t, c)

	cases := map[string]float64{"sda": 36.0, "sdb": 36.0, "sdc": 38.0, "sdd": 37.0, "nvme0n1": 37.85}
	for disk, want := range cases {
		m := findMetric(metrics, map[string]string{"disk": disk})
		if m == nil {
			t.Errorf("disk %s: no metric emitted", disk)
			continue
		}
		if got := gaugeValue(m); got != want {
			t.Errorf("disk %s temperature = %v, want %v", disk, got, want)
		}
	}
	if m := findMetric(metrics, map[string]string{"disk": "sde"}); m != nil {
		t.Errorf("disk sde: expected no metric for a null reading, got %v", gaugeValue(m))
	}
}

// alertListRow builds one alert.list entry in the full shape the 25.10.4
// middleware's api/v25_10_4/alert.py Alert model returns, so the collector is
// exercised against the fields it must ignore as well as the ones it reads.
func alertListRow(klass, level string, dismissed bool, args map[string]any) map[string]any {
	return map[string]any{
		"uuid": klass + "-uuid", "source": "SMART", "klass": klass, "args": args,
		"node": "Controller A", "key": klass + "-key",
		"datetime":        map[string]any{"$date": 1760400000000},
		"last_occurrence": map[string]any{"$date": 1760400000000},
		"dismissed":       dismissed, "mail": nil, "text": "text", "id": klass + "-uuid",
		"level": level, "formatted": "formatted", "one_shot": false,
	}
}

// TestAlerts_CountsActiveByClassAndLevel fixes the class strings to the ones
// the middleware derives from SMART*AlertClass names (alert/base.py strips
// the AlertClass suffix), and proves dismissed alerts, args and serials stay
// out of the exported series.
func TestAlerts_CountsActiveByClassAndLevel(t *testing.T) {
	fc := &fakeCaller{responses: map[string]any{
		"alert.list": []map[string]any{
			alertListRow("SMARTFailedSelfTest", "WARNING", false, map[string]any{"name": "sda", "serial": "S1"}),
			alertListRow("SMARTFailedSelfTest", "WARNING", false, map[string]any{"name": "sdb", "serial": "S2"}),
			alertListRow("SMARTSpareBlockCount", "CRITICAL", false, map[string]any{"name": "/dev/sdc", "serial": "S3", "sb": 80}),
			alertListRow("SMARTUncorrectedErrors", "WARNING", true, map[string]any{"name": "sdd", "serial": "S4", "ue": 3}),
			alertListRow("ZpoolCapacityCritical", "CRITICAL", false, map[string]any{"volume": "storage"}),
		},
	}}
	metrics := collectAll(t, New(fc, nil))

	want := map[[2]string]float64{
		{"SMARTFailedSelfTest", "WARNING"}:    2,
		{"SMARTSpareBlockCount", "CRITICAL"}:  1,
		{"ZpoolCapacityCritical", "CRITICAL"}: 1,
	}
	got := map[[2]string]float64{}
	for _, m := range metrics {
		if k := labelValue(m, "klass"); k != "" {
			got[[2]string{k, labelValue(m, "level")}] = gaugeValue(m)
			for _, lp := range m.GetLabel() {
				if lp.GetName() != "klass" && lp.GetName() != "level" {
					t.Errorf("truenas_alert_active carries unexpected label %q", lp.GetName())
				}
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("truenas_alert_active series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("truenas_alert_active{klass=%q,level=%q} = %v, want %v", k[0], k[1], got[k], v)
		}
	}
	if _, ok := got[[2]string{"SMARTUncorrectedErrors", "WARNING"}]; ok {
		t.Error("a dismissed alert must not be counted as active")
	}
}

// TestCollect_DegradesPerSubCollector proves one failing middleware method
// (a rename, a permissions gap) reports that one collector's success gauge
// as 0 without losing the metrics the other, unrelated calls still produced
// — the same shape cli/internal/truenas/inventory.go already uses for its
// iSCSI session read.
func TestCollect_DegradesPerSubCollector(t *testing.T) {
	fc := &fakeCaller{
		responses: map[string]any{
			"pool.query": []map[string]any{{"name": "storage", "status": "ONLINE"}},
			"disk.temperatures": map[string]any{
				"sda": 36.0,
			},
		},
		errs: map[string]error{
			"alert.list": errors.New("truenas rpc error -32601: Method does not exist"),
		},
	}
	// pool.dataset.query has no configured response, which fakeCaller also
	// reports as an error — exercising the same degrade path from an
	// unconfigured rather than an explicitly-erroring method.
	c := New(fc, nil)
	metrics := collectAll(t, c)

	if v := gaugeValue(findMetric(metrics, map[string]string{"pool": "storage", "state": "online"})); v != 1 {
		t.Errorf("pool_state still expected despite dataset/alerts failures, got %v", v)
	}
	if v := gaugeValue(findMetric(metrics, map[string]string{"disk": "sda"})); v != 36.0 {
		t.Errorf("disk_temperature still expected despite dataset/alerts failures, got %v", v)
	}

	success := map[string]float64{}
	for _, m := range metrics {
		if labelValue(m, "collector") != "" {
			success[labelValue(m, "collector")] = gaugeValue(m)
		}
	}
	if success["pool_state"] != 1 {
		t.Errorf("collector_success{pool_state} = %v, want 1", success["pool_state"])
	}
	if success["disk_temperature"] != 1 {
		t.Errorf("collector_success{disk_temperature} = %v, want 1", success["disk_temperature"])
	}
	if success["dataset_capacity"] != 0 {
		t.Errorf("collector_success{dataset_capacity} = %v, want 0 (no response configured)", success["dataset_capacity"])
	}
	if success["alerts"] != 0 {
		t.Errorf("collector_success{alerts} = %v, want 0 (method does not exist)", success["alerts"])
	}
}
