# ADR: TrueNAS metrics — what the Graphite push can and cannot carry on 25.10

Status: accepted. The Graphite half is reworked and deployed by this change;
the API-polling half is specified here and blocked on a credential.

## Problem

JDWLABS-335 shipped a Graphite push pipeline (TrueNAS reporting exporter →
NodePort 30203 → in-cluster `prom/graphite-exporter` → Prometheus) and nine
alert rules over it. The pipeline was configured correctly end to end and
still produced no data: `graphite_dropped_samples_total` climbed past 455,000
while zero `truenas_*` series existed in Prometheus.

The cause was not configuration. **The chart names the mapping matched on do
not exist on TrueNAS SCALE 25.10.**

## Evidence

A raw capture of the plaintext Graphite stream — 31,968 lines, 476 distinct
metric paths — was taken by pointing the NAS exporter at a temporary sink.
It settles the question without inference:

- The prefix and namespace were always right. Every single line reads
  `truenas.truenas.…`, so the mapping's first two segments matched perfectly
  and the failure was entirely in the third.
- 25.10 replaced the stock netdata collectors with TrueNAS-specific ones.
  What is actually pushed, by chart: `services` (168 paths), `nfsd` (84), two
  `cgroup_*` (76), `truenas_disk_stats` (25), `truenas_arcstats` (25),
  `net_operstate` (21), `truenas_cpu_usage` (17), `cputemp` (17), `system`
  (11), assorted `net_*` (23), `truenas_meminfo` (2), `netdata` (1).
- `zfspool.state_*` does not occur. The only `zfs` substring anywhere in the
  476 is `services.*.zfs_zed`, which is the ZED daemon's own CPU and IO
  accounting, not pool state.
- `disk_space` does not occur. No filesystem or pool capacity is pushed in
  any form.
- Disk temperature is not pushed. `cputemp.temperatures.cpu{,0-15}` is CPU
  package only, and all seventeen dimensions carry byte-identical values.
  `/reporting/graphs` does advertise a `disktemp` graph, but
  `POST /reporting/get_data` returns `[]` for `sda`, `sdb` and `nvme0n1` over
  a one-hour window while the same call for `cputemp` returns a full series —
  so the graph is declared and empty, not merely unqueried.
- Everything arrives in the unit the NAS uses natively. `truenas_meminfo.total`
  is 32720100000 (bytes); `truenas_arcstats.size` is ~900000000 (bytes); disk
  and NFS throughput are Kibibytes/s and interface throughput is Kilobits/s,
  per `/reporting/graphs` `vertical_label`. Nothing is pre-scaled, which is
  why the original mapping's `_gib` suffix was wrong on top of never matching.

Seven of the nine rules were therefore structurally silent. Only
`TrueNASMetricsStale` — which was correctly firing, and was the only reason
anyone noticed — and `TrueNASGraphiteExporterDown` could ever evaluate.

Cardinality never was the constraint the original design treated it as:
mapping *every* path pushed would be 476 series against a 20GB retention cap.

## Decision

A hybrid, because neither transport covers the requirement alone.

### 1. Keep the push for what it genuinely delivers

The mapping is rewritten against the captured path strings. Disk I/O
(`truenas_disk_stats`), ARC (`truenas_arcstats`), CPU (`truenas_cpu_usage`,
`cputemp`), memory (`truenas_meminfo`), NFS (`nfsd`), load/uptime (`system`)
and per-interface throughput and drops (`net`, `net_drops`) are real
operational telemetry that the NAS is already sending for free. 75 series.

Metrics are named for the unit that is actually on the wire. `_bytes` where
the source is bytes; `_kibibytes_per_second` and `_kilobits_per_second` where
it is not. graphite-exporter has no scaling facility, so the alternative to an
awkward name is a false one.

The remaining 401 paths stay unmapped by choice — per-service cgroup
accounting and the 76-way NFSv4 per-operation breakdown answer no question
anyone asks of this NAS. That means `graphite_dropped_samples_total` climbs
continuously on a healthy pipeline, which is why nothing alerts on it.

### 2. Alert only on series that exist

Every rule keyed on `truenas_zfs_pool_state`, `truenas_disk_space_*_gib` or
`truenas_disk_temperature_celsius` is deleted rather than left inert. A rule
whose series cannot exist renders as coverage and is worse than no rule.

`TrueNASMetricsStale` is rewritten onto
`graphite_last_processed_timestamp_seconds`, which advances only when a sample
is *accepted*. This was verified against the broken live exporter: 455,447
dropped samples with that timestamp still reading exactly `0`. An `absent()`
rule over a mapped series cannot distinguish "the NAS stopped pushing" from
"the mapping stopped matching"; this one can, by reading
`graphite_dropped_samples_total` alongside it. It is the rule that would have
caught this defect on day one.

`TrueNASDiskTemperatureSignalMissing` is deleted outright. It was guarded by
`and on() (count(truenas_zfs_pool_state) > 0)`, a guard that could never be
satisfied, so it was doubly dead.

### 3. Pool state, capacity and SMART must come from the API

They are confirmed present there and confirmed absent from the push:

| Data | API source | Verified |
|---|---|---|
| Pool state | `pool.query` | `storage` → `status=ONLINE, healthy=true`, `size=79989470920704`, `allocated=173041852416` |
| Pool/dataset capacity | `pool.dataset.query` | `storage` → `used=83935377792`, `available=38523525645952`; children enumerated per dataset |
| Disk temperature | `disk.temperatures` | `{sdb: 36.0, sdd: 37.0, sdc: 38.0, sda: 36.0, nvme0n1: 37.85}` |

The last row is worth stressing: **disk temperature is unavailable through
the reporting/Graphite path but is available through `disk.temperatures`**,
which reads SMART directly and is cached NAS-side for five minutes. The
JDWLABS-367 SMART gap is narrower than the reporting graphs make it look.

There is no native Prometheus export to avoid this. `reporting.exporters`
advertises exactly one type — verified live against
`/reporting/exporters/exporter_schemas`, which returns a single `GRAPHITE`
key, and confirmed in `truenas/middleware` where
`plugins/reporting/exporters/factory.py` registers
`for exporter_type in [GraphiteExporter,]` on both the 25.10 branch and
master.

## Why the API half is not deployed by this change

Three findings, in increasing order of how much they block:

**No third-party exporter is fit to hold this credential.** Of everything
published, only two poll the API over JSON-RPC rather than the REST endpoint
that TrueNAS 26.04 removes (see ADR 0024). `scottrus/truenas-scale-exporter`
uses the versioned `/api/current` endpoint and covers pool state, per-vdev
error counters and `disk.temperatures` — but it is three weeks old, has zero
stars, self-declares as AI-authored, and has no dataset-level metrics (its
issue #11). `bodhispace-xyz/truenas-exporter-rs` does cover datasets but sits
on the legacy `/websocket` endpoint, has been untouched for three months, and
publishes its image under a different org than the repository. Everything
else is REST-only, dead, or TrueNAS CORE. Nothing here is a dependency worth
handing a NAS credential to without reading it end to end first.

**The existing `truenas-csi` key cannot authenticate over WebSocket.** Reusing
it was the intended route, and it does not work: against `/api/current`,
`auth.login_with_api_key` returns `false` and `auth.login_ex` with
`API_KEY_PLAIN` returns `AUTH_ERR` for every candidate username, while the
same key was at that moment serving REST requests normally. The key also
cannot read `/user` or `/api_key`, so it is scoped rather than
full-privilege. Whatever polls the API needs its own user-linked, read-only
key — which reverses JDWLABS-335's original "no credential" constraint,
deliberately, because the push path cannot deliver this ticket's core
deliverable and so the trade-off had to change.

**That key must be minted by a human.** It cannot be created from here: the
credential this cluster holds is precisely the one that cannot read or write
the `api_key` endpoint.

> **Do not probe the `truenas-csi` key against WebSocket auth.** Establishing
> the finding above cost the key itself. Four failed
> `auth.login_with_api_key` / `auth.login_ex` attempts against `/api/current`
> were enough: REST calls were succeeding immediately before them, and
> immediately after them the key stopped being accepted on REST too —
> returning the same
> `401 Invalid API key` as a fabricated key, from inside the cluster as well
> as outside, so it is the key that was invalidated and not an IP throttle.
> democratic-csi began logging `401 Invalid API key` immediately afterwards.
> Mounted volumes keep serving because the NFS and iSCSI data paths do not
> touch the API, but provisioning, deletion, expansion and snapshots on both
> TrueNAS storage classes fail until the key is regenerated in the TrueNAS UI
> and rewritten to Vault `kv/truenas-csi`. Anyone testing API access should
> use a throwaway key, not the one production depends on.

So the API half is specified, not shipped. Shipping manifests that could not
be run against the NAS would repeat the exact failure this ADR exists to
correct — plausible YAML, no proof, silent in production.

## Consequences

- Pool health, pool and dataset capacity, and SMART remain unalerted. This is
  now recorded rather than implied by rules that cannot fire. A pool failing
  is still visible: `HardwareHostUnreachable` covers the NAS disappearing, and
  TrueNAS's own alert mail covers pool degradation, but neither reaches
  Alertmanager.
- The NAS keeps pushing on its own cadence and the mapping keeps ignoring 401
  of 476 paths, so `graphite_dropped_samples_total` is not a health signal on
  this pipeline and must not be turned into one.
- When the read-only key exists, the follow-up is small: an ExternalSecret on
  a new Vault path, a Deployment and ServiceMonitor for whichever exporter is
  chosen after a source read, and the four rules deleted here reinstated
  against its metric names.

## Verification performed

- `promtool check rules` over all 14 PrometheusRule documents in the repo.
- The mapping run under the pinned `prom/graphite-exporter:v0.17.0` image with
  `--graphite.mapping-strict-match`, fed one complete push cycle of all 476
  real captured paths: 75 samples accepted, 401 dropped, zero collisions,
  `graphite_last_processed_timestamp_seconds` advancing.
- That local run is also what caught a defect in the first draft: mapping
  regexes are anchored at the start but not the end, so
  `…truenas_cpu_usage\.cpu\.cpu` swallowed `cpu0`–`cpu15` and collapsed
  seventeen series into one. Every pattern in the mapping now carries explicit
  `^` and `$`.
