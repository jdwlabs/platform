# Job output freshness

## The failure this exists for

A Minecraft backup sidecar reported `Up` and ran on schedule twice daily for
months while producing zero backup archives. The container started, the
schedule fired, the process exited 0, the liveness probe passed. Every signal
the cluster collected said healthy. The only thing that was wrong was the one
thing nothing measured: whether any output appeared.

Scheduled work has two independent failure modes, and conflating them is why
this went unnoticed:

| Failure | Observable from | Covered by |
|---|---|---|
| The job stopped running or stopped completing | `kube-state-metrics` | `rules-job-freshness.yaml` (live today) |
| The job ran and completed, but produced nothing | nothing, unless something reports the artifact | `rules-job-freshness.yaml` (live since the first producer) |

Nothing kube-state-metrics knows can distinguish a backup that wrote a 4 GB
archive from one that wrote an empty file, because success is defined by the
process exit code and the process exited 0. **Only something that looks at the
artifact can report on the artifact.** A rule cannot be written for a signal
nobody emits, so the second row of that table stayed a contract for producers
to adopt until one adopted it.

## What is already alerting

`tenants/platform/services/kube-prometheus-stack/postInstall/rules-job-freshness.yaml`
covers every CronJob in the cluster with no opt-in:

- `CronJobHasNotSucceededRecently` — no successful completion in 24h
- `CronJobNeverSucceeded` — created over 24h ago, has never once completed
- `CronJobLastRunDidNotSucceed` — fired, and has not succeeded in the hour since

The 24h threshold is four times the interval of the slowest CronJob in the
repo (`postgres-backup`, every 6h). **A CronJob scheduled less frequently than
every 6h will false-positive against it** and needs either its own rule or a
schedule-aware threshold added when the first such job appears.

## The contract for output freshness

A job that produces an artifact publishes two gauges. Both carry a
`backup_job` label naming the producer and, where more than one artifact class
exists, an `artifact` label.

| Metric | Type | Meaning |
|---|---|---|
| `backup_last_success_timestamp_seconds` | gauge | Unix timestamp when an artifact was last **verified written and non-empty**. Not when the job started, not when it exited. |
| `backup_last_artifact_bytes` | gauge | Size of that artifact in bytes. |
| `backup_max_age_seconds` | gauge | The producer's own freshness SLO, published as a metric. |
| `backup_last_artifact_quiesced` | gauge | Optional. `1` if the newest artifact was taken with its source quiesced, `0` if taken cold. Omit it entirely where the distinction does not apply, or where it is not known — never guess a value, since the point of the gauge is to make a degraded run distinguishable from a clean one. |

Publishing the SLO as a metric rather than hard-coding it into a rule is what
makes this reusable: a new producer adopts the pattern by emitting three
numbers, and needs no change to any PrometheusRule.

The timestamp must be written **after** verifying the artifact, not after the
upload command returns. `rclone copy` of an empty directory exits 0. `tar` of
a missing path can too. The precedent to avoid is exactly this: an exit code
treated as proof of output.

### Getting the metrics out of a job

A CronJob pod is gone before Prometheus can scrape it, so it cannot be scraped
directly. Either:

- push to a Pushgateway at the end of the run (needs a Pushgateway; none is
  deployed in this cluster today), or
- write a `.prom` file to a volume that a long-lived node-exporter or
  textfile-collector sidecar exposes, or
- have a long-lived companion process expose the values, reading the artifact
  store on its own schedule.

The third option is the most faithful to the failure being prevented, because
it observes the artifact store rather than trusting the job's own account of
itself. It is also the only one that reports the worst case: a job that never
ran at all pushes nothing, and a series that was never created is not a series
that went stale.

**But it is often not available, and the first producer could not use it.**
A long-lived reader has to mount the artifact store, and where that store is a
`ReadWriteOnce` claim on a driver with `attachRequired: true` — which is every
democratic-csi class in this cluster — the volume can only be attached to one
node at a time. The Minecraft backup needs its archive claim *and* the world
claim in the same pod, so it runs wherever the server holds the world; a
permanent second mounter of the archive claim would have to be pinned to that
node and follow it whenever it moved. That is the same "only works while the
server is up" coupling the producer existed to remove, one layer down.

Check the access mode before reaching for a long-lived reader. Where it rules
the option out, the workable shape is a **hybrid**: the job scans the artifact
store — it already has it mounted — and hands the resulting numbers to a
separate serving pod through a ConfigMap, which has no node affinity at all.
The values stay artifact-derived, and the serving pod stays schedulable
anywhere. What is lost is only the ability to notice artifacts deleted between
runs; a job that stops producing is still caught by staleness, which is the
case that actually recurred here.

That is what the first producer does: the job writes a `metrics.txt` key into a
ConfigMap, and a busybox `httpd` serves it, fronted by a Service and a
ServiceMonitor. Three details are worth copying:

- **Match the scheduled artifact naming exactly, not a loose prefix.** A manual
  rescue archive sitting in the same directory will otherwise satisfy the
  freshness check and make a dead job look healthy for as long as it stays
  there.
- **Report zero, not nothing, for an empty artifact store.** Both gauges at `0`
  trip `BackupArtifactEmpty` and `BackupArtifactStale` together, so a freshly
  deployed producer alerts until it has actually produced something rather than
  starting out silently green. Mount the ConfigMap `optional: true` so the
  serving pod starts and says zero before the first run has ever happened.
- **Publish last, after every check that can still fail.** A freshness
  timestamp that advances for a run which then aborted is worse than no
  timestamp, because it reports success for an artifact that was never
  completed.

### The rules these feed

The four rules driven by these gauges are live in
`tenants/platform/services/kube-prometheus-stack/postInstall/rules-job-freshness.yaml`
and are not restated here — one copy, so the two cannot drift:

| Alert | Fires when |
|---|---|
| `BackupArtifactStale` | no verified artifact within the producer's own `backup_max_age_seconds` |
| `BackupArtifactEmpty` | the newest artifact is zero bytes |
| `BackupFreshnessSignalMissing` | nothing is publishing the gauges at all, so neither of the above can fire |
| `BackupArtifactNotQuiesced` | the newest artifact was taken without quiescing its source |

They were deliberately held back until a producer existed. Until then they
would have matched nothing, and a rule that silently matches nothing looks
exactly like a rule that is passing — the same class of false assurance that
caused the original incident.

`BackupArtifactNotQuiesced` is optional and reads `backup_last_artifact_quiesced`,
a fourth gauge for producers that can quiesce their source. A producer that
degrades to an unquiesced copy rather than abandoning the run should publish it,
so the fallback cannot silently become the permanent state. Omit the gauge and
the alert simply never fires.

## Adopting this

1. Make the job verify its own output — size, and ideally that the archive
   opens — before recording success.
2. Emit the three gauges by one of the transports above.
3. Land the rules alongside the producer. The rules now exist, so a second
   producer needs no rule change at all — it inherits them by emitting the
   gauges. Where a producer lives in another repository the two halves cannot
   be one PR, so open them together and reference each from the other; the
   pairing is what matters, not the single commit.
4. Prove the alert fires: point the producer at an empty source, confirm
   `BackupArtifactEmpty` goes pending, then restore it. An alert that has
   never been seen to fire is an assumption.
