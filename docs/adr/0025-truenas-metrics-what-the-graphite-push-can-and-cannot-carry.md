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

## Addendum 2026-08-28: the API half, credentialed and shipped

The blocker above is cleared. A dedicated local TrueNAS user
(`prometheus-readonly`) was created with the built-in **Readonly Admin**
role — least-privilege, and never the `truenas-csi` key, whose blast radius
this ADR already documented. Its key authenticated successfully on its one
test read, seeded to Vault `kv/truenas-prometheus` (field `api_key`,
`platformctl bootstrap seed truenas-prometheus`), and this ADR's own
warning was honored doing it: exactly one dial, no retry.

### The two third-party exporters were re-read, not re-assumed

Both had moved since the table above was written, so both were re-checked
live (`gh api repos/<owner>/<repo>`, commit history, and the actual source)
rather than trusting a several-week-old verdict:

- **`scottrus/truenas-scale-exporter`** is meaningfully more mature now —
  17+ merged PRs since, a real bugfix (a scrub-status misread), a Dependabot
  cadence, and a `boot.get_state`-based boot-pool collector added in 0.2.0.
  It still has 0 stars and 0 forks after that work, and its own issue #11
  ("Dataset-level metrics: quota exhaustion is invisible behind a healthy
  pool") is still open — confirmed by reading the issue directly, not the
  repo's summary. That gap is exactly this ticket's pool/dataset capacity
  requirement, so adopting it would still mean writing and maintaining that
  metric family, on someone else's unaudited codebase, for a credential this
  cluster's provisioning already depends on staying scoped correctly.
- **`bodhispace-xyz/truenas-exporter-rs`** also has recent commits, but they
  are CI and release-tooling churn (`release-please` target-branch fixes,
  build speedups) — reading `src/truenas/connection.rs` directly shows
  `websocket_url()` still formats `{protocol}://{host}/websocket`, the same
  legacy, pre-JSON-RPC-2.0 endpoint flagged below. It has not moved to
  `/api/current`.

Neither changes the original conclusion. Handing either project a live NAS
credential is still adopting an unaudited dependency to solve a problem this
repo already has the audited half of a solution for.

### Decision: truenas-prometheus-exporter, built on platformctl's own transport

`cli/internal/truenas/transport.go`'s `WSClient`/`Dial` is the one piece of
code in this repo that has actually authenticated against this NAS over
`/api/current` — reusing it for a second consumer costs one new package
(`cli/internal/truenasexporter`) and one new `cmd/`, not a second WebSocket/
JSON-RPC/auth implementation to get right and re-audit. The exporter dials
once, authenticates once, and never retries for the life of the process —
see `cmd/truenas-prometheus-exporter/main.go`'s comment for why a
crash-and-restart-on-failure exporter would turn every `CrashLoopBackOff`
cycle into exactly the kind of repeated `auth.login_with_api_key` attempt
that invalidated the `truenas-csi` key above. A failed or rejected dial is
terminal for that pod's ability to scrape (`truenas_exporter_dial_ok 0`,
`TrueNASPrometheusExporterAuthFailed`); a human fixing the credential and
redeploying is the only path to a second attempt.

Coverage against this ADR's own evidence table:

| Data | Method | Verified how |
|---|---|---|
| Pool state | `pool.query` | Unit-tested against this ADR's live `status=ONLINE` capture |
| Pool/dataset capacity | `pool.dataset.query` (pool-root dataset) | Unit-tested against this ADR's live `used=83935377792`/`available=38523525645952` capture |
| Disk temperature | `disk.temperatures` | Unit-tested against this ADR's live `{sdb: 36.0, sdd: 37.0, sdc: 38.0, sda: 36.0, nvme0n1: 37.85}` capture, including that a null reading is skipped, not zeroed |
| SMART self-test pass/fail | `smart.test.results.query` | **Not live-verified** — no network path from where this exporter was written to the NAS (confirmed by a direct TCP connect attempt failing). Implemented against TrueNAS's documented method signature, classifying only `SUCCESS`/`FAILED`-family statuses and leaving anything else (including a disk with no test history) unemitted rather than guessed |
| SMART reallocated-sector count | — | **Not implemented.** No TrueNAS API method for this was identified with enough confidence to ship unverified field-parsing code; a wrong field name would silently return zero forever, which is worse than the metric's absence. Left for a follow-up that can read the live response shape |

The Go build, its unit test suite (`go test ./internal/truenasexporter/...`),
a real `docker build` of `cmd/truenas-prometheus-exporter/Dockerfile`, and a
container run of the built image against an unreachable address all passed
locally — proving the one-dial-no-retry degrade path actually holds at the
container level, not only in source. What could not be verified locally: the
image is not yet published (no Docker registry push happened from this
session — see `tools/image-pin-allowlist.yaml`'s `pending-first-release`
entry for the reference this repo ships ahead of the artifact, and why a
fabricated digest was rejected instead), and the exact live shape of
`smart.test.results.query` and pool/dataset field names beyond what this
ADR's own table already captured. `platformctl tenants verify-secrets` and
`platformctl bootstrap seed` were both run for real against live Vault
(`kv/truenas-prometheus` created), and `promtool`/`amtool` both confirm the
new rules compile, unit-test correctly, and route through the same
severity-based `ai-sre`/`discord` path every other untenanted TrueNAS alert
uses.
