// Package truenasexporter polls TrueNAS's JSON-RPC middleware for the three
// things docs/adr/0025 confirmed the Graphite push cannot carry — pool state,
// pool/dataset capacity, and disk temperature — and serves them as Prometheus
// gauges.
//
// It reuses truenas.Caller (cli/internal/truenas/transport.go) rather than
// opening its own WebSocket client: that code is already the one thing in
// this repo that has actually authenticated against this NAS, and ADR-0025
// exists precisely because a second, unaudited implementation of this
// transport is how a credential gets burned learning things the first one
// already knows.
package truenasexporter

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jdwlabs/platform/internal/truenas"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	poolStateDesc = prometheus.NewDesc(
		"truenas_zfs_pool_state",
		"Whether a ZFS pool is in the named state (1) or not (0). Mirrors the "+
			"two-state convention the deleted TrueNASPoolDegraded/TrueNASPoolNotOnline "+
			"rules already assumed, so those rules are reinstated unchanged.",
		[]string{"pool", "state"}, nil,
	)
	datasetUsedDesc = prometheus.NewDesc(
		"truenas_zfs_dataset_used_bytes",
		"Bytes used on a pool's root dataset, read from pool.dataset.query's own "+
			"used.parsed — the unit TrueNAS reports natively, per docs/adr/0025.",
		[]string{"pool", "dataset"}, nil,
	)
	datasetAvailableDesc = prometheus.NewDesc(
		"truenas_zfs_dataset_available_bytes",
		"Bytes available on a pool's root dataset, read from pool.dataset.query's "+
			"own available.parsed.",
		[]string{"pool", "dataset"}, nil,
	)
	diskTemperatureDesc = prometheus.NewDesc(
		"truenas_disk_temperature_celsius",
		"Disk temperature read from disk.temperatures (SMART, cached NAS-side for "+
			"5m per docs/adr/0025). Labeled by device name — disk.temperatures does "+
			"not return a serial, unlike the deleted rule's assumption.",
		[]string{"disk"}, nil,
	)
	smartTestPassedDesc = prometheus.NewDesc(
		"truenas_disk_smart_test_passed",
		"1 if the most recent SMART self-test on this disk reported success, 0 if "+
			"it reported a definite failure. Omitted for a disk with no test history "+
			"— never guessed.",
		[]string{"disk"}, nil,
	)
	scrapeOKDesc = prometheus.NewDesc(
		"truenas_exporter_collector_success",
		"1 if the named sub-collector's middleware call succeeded on the most "+
			"recent scrape, 0 otherwise. One series per collector rather than a "+
			"single up value, because a NAS upgrade renaming one method must not "+
			"read as every metric being down.",
		[]string{"collector"}, nil,
	)
)

// Collector implements prometheus.Collector against a single already-
// authenticated TrueNAS session.
//
// It never dials or re-dials — see cmd/truenas-prometheus-exporter/main.go
// for why a scrape-time reconnect is deliberately not implemented here.
type Collector struct {
	caller truenas.Caller
	// onError receives every sub-collector failure so main can log it; the
	// Collector itself never logs, so it stays trivially testable against a
	// fake Caller with no log-format assertions.
	onError func(collector string, err error)
}

// New builds a Collector over an already-dialed session.
func New(caller truenas.Caller, onError func(collector, msg string)) *Collector {
	c := &Collector{caller: caller}
	if onError != nil {
		c.onError = func(collector string, err error) { onError(collector, err.Error()) }
	} else {
		c.onError = func(string, error) {}
	}
	return c
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolStateDesc
	ch <- datasetUsedDesc
	ch <- datasetAvailableDesc
	ch <- diskTemperatureDesc
	ch <- smartTestPassedDesc
	ch <- scrapeOKDesc
}

// Collect runs every sub-collector independently: one middleware method
// being renamed or erroring must degrade that one metric family, not the
// whole scrape. This is the same "read what you can, report what you
// couldn't" shape cli/internal/truenas/inventory.go already uses for the
// iSCSI session read.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()

	c.run(ch, "pool_state", c.collectPoolState)
	c.run(ch, "dataset_capacity", c.collectDatasetCapacity)
	c.run(ch, "disk_temperature", c.collectDiskTemperatures)
	c.run(ch, "smart_test", c.collectSmartTests)

	_ = ctx
}

func (c *Collector) run(ch chan<- prometheus.Metric, name string, fn func(context.Context, chan<- prometheus.Metric) error) {
	err := fn(context.Background(), ch)
	ok := 1.0
	if err != nil {
		ok = 0.0
		c.onError(name, err)
	}
	ch <- prometheus.MustNewConstMetric(scrapeOKDesc, prometheus.GaugeValue, ok, name)
}

type poolRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (c *Collector) collectPoolState(ctx context.Context, ch chan<- prometheus.Metric) error {
	var rows []poolRow
	if err := c.caller.Call(ctx, "pool.query", []any{[]any{}}, &rows); err != nil {
		return fmt.Errorf("pool.query: %w", err)
	}
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		status := strings.ToUpper(r.Status)
		ch <- prometheus.MustNewConstMetric(poolStateDesc, prometheus.GaugeValue,
			boolFloat(status == "ONLINE"), r.Name, "online")
		ch <- prometheus.MustNewConstMetric(poolStateDesc, prometheus.GaugeValue,
			boolFloat(status == "DEGRADED"), r.Name, "degraded")
	}
	return nil
}

type zfsProperty struct {
	Parsed any `json:"parsed"`
}

type datasetRow struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Used      *zfsProperty `json:"used"`
	Available *zfsProperty `json:"available"`
}

// collectDatasetCapacity reads only each pool's root dataset (id == pool
// name): docs/adr/0025 verified used/available at exactly that level, and
// enumerating every child dataset without a live-checked filter would risk
// unbounded cardinality on a NAS this exporter has never actually queried.
func (c *Collector) collectDatasetCapacity(ctx context.Context, ch chan<- prometheus.Metric) error {
	var pools []poolRow
	if err := c.caller.Call(ctx, "pool.query", []any{[]any{}}, &pools); err != nil {
		return fmt.Errorf("pool.query: %w", err)
	}

	var firstErr error
	for _, p := range pools {
		if p.Name == "" {
			continue
		}
		filters := []any{[]any{"id", "=", p.Name}}
		var rows []datasetRow
		if err := c.caller.Call(ctx, "pool.dataset.query", []any{filters}, &rows); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pool.dataset.query(%s): %w", p.Name, err)
			}
			continue
		}
		for _, r := range rows {
			if r.Used != nil {
				if used, err := parseZFSBytes(r.Used.Parsed); err == nil {
					ch <- prometheus.MustNewConstMetric(datasetUsedDesc, prometheus.GaugeValue,
						float64(used), p.Name, r.ID)
				}
			}
			if r.Available != nil {
				if avail, err := parseZFSBytes(r.Available.Parsed); err == nil {
					ch <- prometheus.MustNewConstMetric(datasetAvailableDesc, prometheus.GaugeValue,
						float64(avail), p.Name, r.ID)
				}
			}
		}
	}
	return firstErr
}

func (c *Collector) collectDiskTemperatures(ctx context.Context, ch chan<- prometheus.Metric) error {
	var temps map[string]*float64
	// An empty names list is documented to mean "every disk" — passing []
	// rather than omitting the arg keeps this one explicit call shape rather
	// than a second, untested one for "all disks".
	if err := c.caller.Call(ctx, "disk.temperatures", []any{[]any{}}, &temps); err != nil {
		return fmt.Errorf("disk.temperatures: %w", err)
	}
	for disk, celsius := range temps {
		if celsius == nil || disk == "" {
			// A disk with no SMART temperature support (or asleep) reports null
			// here rather than omitting the key; skipping it beats fabricating 0,
			// which downstream would read as "ice cold" instead of "unknown".
			continue
		}
		ch <- prometheus.MustNewConstMetric(diskTemperatureDesc, prometheus.GaugeValue, *celsius, disk)
	}
	return nil
}

type smartTestRow struct {
	Disk  string `json:"disk"`
	Tests []struct {
		Status string `json:"status"`
	} `json:"tests"`
}

// smartPassStatuses and smartFailStatuses are the two ends of TrueNAS's
// smartctl self-test status vocabulary this exporter is confident about
// without a live read of this NAS's own response shape — see the ADR
// addendum this PR adds. Anything else (RUNNING, ABORTED_BY_HOST, a status
// this exporter has never seen) is left unclassified rather than guessed
// into either bucket.
var (
	smartPassStatuses = map[string]bool{"SUCCESS": true, "COMPLETED": true, "COMPLETED_WITHOUT_ERROR": true}
	smartFailStatuses = map[string]bool{"FAILED": true, "COMPLETED_WITH_ERROR": true, "ERROR": true}
)

func (c *Collector) collectSmartTests(ctx context.Context, ch chan<- prometheus.Metric) error {
	var rows []smartTestRow
	if err := c.caller.Call(ctx, "smart.test.results.query", []any{}, &rows); err != nil {
		return fmt.Errorf("smart.test.results.query: %w", err)
	}
	for _, r := range rows {
		if r.Disk == "" || len(r.Tests) == 0 {
			continue
		}
		status := strings.ToUpper(r.Tests[0].Status)
		switch {
		case smartPassStatuses[status]:
			ch <- prometheus.MustNewConstMetric(smartTestPassedDesc, prometheus.GaugeValue, 1, r.Disk)
		case smartFailStatuses[status]:
			ch <- prometheus.MustNewConstMetric(smartTestPassedDesc, prometheus.GaugeValue, 0, r.Disk)
			// A status this exporter does not recognise is left unemitted for
			// that disk rather than defaulted to pass or fail.
		}
	}
	return nil
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// parseZFSBytes reads the numeric forms pool.dataset.query's "parsed"
// sub-field uses for a byte count. Mirrors truenas.parseSize's cases
// (unexported in that package, so duplicated rather than reached into).
func parseZFSBytes(raw any) (int64, error) {
	switch v := raw.(type) {
	case nil:
		return 0, fmt.Errorf("nil")
	case float64:
		return int64(v), nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("size %q is not an integer", v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("size is %T, want a number", raw)
	}
}
