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
| The job ran and completed, but produced nothing | nothing, unless the job says so | this document's contract (not yet implemented) |

Nothing kube-state-metrics knows can distinguish a backup that wrote a 4 GB
archive from one that wrote an empty file, because success is defined by the
process exit code and the process exited 0. **The producing job is the only
thing that can report on its own output.** A rule cannot be written for a
signal nobody emits, so the second row of that table is a contract for
producers to adopt, not a rule shipped in advance.

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
itself.

### Rules to add once a producer emits these

These are deliberately **not** in `rules-job-freshness.yaml`. Until a producer
exists they would match nothing, and a rule that silently matches nothing
looks exactly like a rule that is passing — the same class of false assurance
that caused the original incident. Add them in the PR that adds the first
producer.

```yaml
- alert: BackupArtifactStale
  expr: |
    (time() - max by (backup_job, artifact) (backup_last_success_timestamp_seconds))
    > max by (backup_job, artifact) (backup_max_age_seconds)
  for: 30m
  labels:
    severity: critical
  annotations:
    summary: "Backup {{ $labels.backup_job }} has produced no fresh artifact"
    description: "Last verified artifact for {{ $labels.backup_job }} is {{ $value | humanizeDuration }} old, past the freshness budget the job publishes for itself."
    runbook_url: "https://github.com/jdwlabs/platform/blob/main/docs/OPERATIONS.md#5-troubleshooting-symptom--fix"

- alert: BackupArtifactEmpty
  expr: max by (backup_job, artifact) (backup_last_artifact_bytes) == 0
  for: 15m
  labels:
    severity: critical
  annotations:
    summary: "Backup {{ $labels.backup_job }} produced an empty artifact"
    description: "The most recent artifact for {{ $labels.backup_job }} is zero bytes. The job completed successfully and wrote nothing."
    runbook_url: "https://github.com/jdwlabs/platform/blob/main/docs/OPERATIONS.md#5-troubleshooting-symptom--fix"

# Without this, deleting the exporter that publishes the two metrics above
# silently disables both — the same disappearing-series problem the alerts
# themselves exist to catch, moved up one layer.
- alert: BackupFreshnessSignalMissing
  expr: absent(backup_last_success_timestamp_seconds)
  for: 1h
  labels:
    severity: warning
  annotations:
    summary: "No backup freshness signal is being published"
    description: "backup_last_success_timestamp_seconds is absent for 1h, so BackupArtifactStale and BackupArtifactEmpty cannot fire."
    runbook_url: "https://github.com/jdwlabs/platform/blob/main/docs/OPERATIONS.md#8-observability-quick-refs"
```

## Adopting this

1. Make the job verify its own output — size, and ideally that the archive
   opens — before recording success.
2. Emit the three gauges by one of the transports above.
3. Copy the three rules into `rules-job-freshness.yaml` in the same PR.
4. Prove the alert fires: point the producer at an empty source, confirm
   `BackupArtifactEmpty` goes pending, then restore it. An alert that has
   never been seen to fire is an assumption.
