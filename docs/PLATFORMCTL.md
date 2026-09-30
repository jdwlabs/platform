# platformctl — agent reference

Per-command detail for driving the cluster through `platformctl`. The contract
(`--json` events, exit codes) is in the root `AGENTS.md`; the event schema
and `--non-interactive` env-var table are in [OPERATIONS.md §6](OPERATIONS.md#6-non-interactive--ci-mode).

## Storage (Longhorn volumes)

- List volumes with their reclaim classification: `platformctl cluster volumes list` — TOON output, four default fields (`name,state,class,size`), widened by `--fields <csv>` or `--full` and narrowed by `--class orphaned|claimed|attached|other`
- Preview a reclaim: `platformctl cluster volumes reclaim --all-orphaned --dry-run` — lists exactly what would be deleted and mutates nothing
- Delete: `platformctl cluster volumes reclaim --all-orphaned --confirm`, or `--name <volume>` (repeatable) for specific ones. Reclaim refuses to run without `--confirm` or `--dry-run`, never reads stdin, and refuses any volume still claimed by a PVC or by a `Bound` PersistentVolume — a refusal is reported as a `refused` row and exits non-zero rather than being skipped quietly
- `--class` is the tool's verdict, not Longhorn's `state`. A claim is resolved from the PersistentVolumeClaim's `spec.volumeName`; the volume's own `status.kubernetesStatus.pvcName` is historical and repeats across generations of the same StatefulSet, so it never proves a volume is live. The `longhorn-single` class uses `Retain`, so detached volumes never age out on their own

## Storage (TrueNAS volumes)

`platformctl cluster volumes truenas` covers what Longhorn's backend cannot see: the objects the two democratic-csi drivers leave on the NAS. Both TrueNAS classes use `Retain`, so deleting a PVC deletes nothing there — one `truenas-iscsi` PVC leaks a zvol, an iSCSI extent, an iSCSI target and the target-extent mapping; one `truenas-nfs` PVC leaks a dataset and its NFS export. None of it is visible to the cluster.

- Same UX as the Longhorn backend: `list` emits TOON with four default fields (`name,kind,class,size`), widened by `--fields <csv>` or `--full`, narrowed by `--class orphaned|claimed|attached|other`. `--storage-class truenas-iscsi|truenas-nfs` narrows to one driver
- `platformctl cluster volumes truenas reclaim --all-orphaned --dry-run` previews; `--confirm` deletes; neither is assumed and stdin is never read. `--name <name>` is repeatable and is still checked against the same rules
- A refusal is a `refused` row and a non-zero exit, never a quiet skip. Under `--all-orphaned` that covers the `other` class — the classifier declining to conclude — while `claimed` and `attached` are simply not selected. A read that degraded the run, an unreadable session list above all, is a `warnings` line at the top of the output
- **A zvol's own name never proves it is live.** Provisioned objects are named for the PVC UID they were created for, and that name outlives the PV, the PVC and the workload — the same trap as Longhorn's recorded `pvcName`. Liveness is only ever read from the other side: a PersistentVolume whose CSI volume handle, volume attributes, NFS path or iSCSI IQN names the object (claims resolved from each PVC's `spec.volumeName`), or an open iSCSI session on a target that exports it. If the session list cannot be read, every zvol is refused — unknown liveness is not idle
- **The two liveness rungs are not both available.** The middleware keeps no client state for an NFS export, so `truenas-nfs` has no equivalent of the session rung and the PersistentVolume side is the whole of the evidence; a dataset an outside client mounts, or one whose PV a partial resync has not recreated, is indistinguishable from an idle one. The one NAS-side signal that survives is an export **above** the dataset, and a dataset covered by one is refused. Anything under the driver's `detachedSnapshotsDatasetParentName` is refused too. Because the gap cannot be closed from the NAS, it is disclosed at runtime: any selected set containing a `truenas-nfs` candidate gets a standing `warnings` entry saying so, under `--dry-run` and `--confirm` alike
- A reclaim that stops part-way is resumable. The candidate it stopped on is named on an `incomplete` line, deleting an already-absent object succeeds, and a re-run re-classifies from live state — so resuming is the same command again
- The iSCSI objects are joined by **numeric ID, not by name**: an extent's `disk` field is the only statement of which zvol it exports, and a target reaches its zvol only through a mapping row. A target named for volume A can be mapped to an extent exporting volume B, so every hop is resolved through IDs, and a target that also exports something else is refused
- Deletes run in dependency order (mapping → extent → target → export → dataset → `Released` PV) and each object is re-read and matched on its exact name immediately before the delete, because middleware row IDs are small and get reused
- The driver configs are read from the rendered `democratic-csi` Secrets, so the NAS address, dataset parents and iSCSI naming affixes follow the config rather than being hard-coded. `--truenas-ca-file` or `--truenas-insecure-skip-tls-verify` is required while the NAS presents its stock self-signed certificate
- The credential comes from `PLATFORMCTL_TRUENAS_API_KEY` and nowhere else by default. `--truenas-use-csi-api-key` opts into the key inside the driver-config Secret, which is the one democratic-csi provisions with — see the authentication note below

`platformctl` reaches the NAS over **JSON-RPC 2.0 on `wss://<host>/api/current`** — the API that replaces REST in TrueNAS 26, isolated behind one `Caller` interface. This is deliberately not the transport the CSI drivers use: they are stuck on REST and gate the NAS at 25.10.x (`docs/adr/0024-truenas-rest-removal-blocks-democratic-csi.md`), and every REST call also keeps the NAS's deprecation alert alive, which is the only live evidence that the drivers still depend on the removed transport.

**An authentication attempt is a mutation, not a read.** Repeated failures invalidate the `truenas-csi` key and take provisioning down for every class at once — four attempts did exactly that (`docs/adr/0025-truenas-metrics-what-the-graphite-push-can-and-cannot-carry.md`), and the key had to be regenerated in the TrueNAS UI. Never probe or retry auth in a loop — fail fast on a rejection and report it. The blast radius is what makes the `truenas-csi` key opt-in rather than the default, and it applies whether or not the attempt would succeed: do not read ADR-0025's "cannot authenticate over WebSocket" as a standing fact, it describes the pre-incident key and the regenerated one has since authenticated. That is also why a rejection prints its own help telling you **not** to re-run: export a throwaway read-only key as `PLATFORMCTL_TRUENAS_API_KEY` instead.

## NetworkPolicy coverage (Cilium managed endpoints)

Cilium only manages a pod whose sandbox it created, so a pod that predates the
agent on its node has no `CiliumEndpoint`, no identity, and no policy resolves
against it — while the namespace still reports its policies applied.

- Measure: `platformctl cluster netpol coverage` — TOON output, per-namespace by
  default; `--by node` shows which nodes still carry pre-agent pods, `--unmanaged`
  prints the pod-level restart worklist, `-n <ns>` scopes both sides of the join
- It exits non-zero below `--min-coverage` (default 100), so it is a gate, not
  only a report; `--min-coverage 0` reports without failing
- The same join runs as the `cilium-endpoint-coverage` check in
  `platformctl cluster status`, reported as a warning — partial coverage is the
  expected state mid-rollout, and a permanently red check is one nobody reads
- Host-network pods and pods that are not Running are excluded: neither can ever
  carry an endpoint, so counting them would report a gap no remediation closes
- No Prometheus alert exists and none can be written from cilium-agent metrics
  alone — the agent cannot count pods it never learned about. See
  [ADR 0028](adr/0028-cilium-managed-endpoint-coverage.md); the sequenced
  remediation is [OPERATIONS.md §9](OPERATIONS.md#9-closing-the-cilium-managed-endpoint-gap)

## Drain feasibility

- Ask whether every node could be drained right now: `platformctl cluster drain-check` — TOON output, five default fields (`node,verdict,movable,movableMem,blockers`), widened by `--fields <csv>` or `--full`, narrowed to one node by `--node <name>`. Read-only, and **exits non-zero when any node is blocked**, so it works as a gate before an upgrade
- A `blocked` node also prints a `blockers` table naming the pod and why nothing will take it. The `class` column is the part to read: `hard` means every surviving node was excluded by something no packing order can change — a taint, node or volume affinity, or an anti-affinity rule against a pod that was already resident — which makes it a proof. `capacity` means the refusal is order-dependent: nodes were full once the rest of the evacuation was packed, or were excluded only by what this same evacuation had already placed there, so it is a strong signal rather than a proof. `unmanaged` means the pod has no controller to recreate it
- `--plan` prints the pod-to-node assignment the simulation found; `--pods` prints every pod on the reported nodes with its drain classification and disruption-budget allowance; `--usage` reads metrics-server and puts observed memory and CPU beside declared requests (`used`/`usedCpu` columns on `--pods`)
- It answers "could this node be drained **now**", not "could every node be drained in sequence" — draining one node moves everything, and the next verdict is computed against the state before that. It also does not model eviction pacing: a node can be feasible and still hang on a `PodDisruptionBudget` currently allowing no disruption, which is what the `pdbAtZero` field counts
- Preferred affinity and topology spread constraints are not evaluated, because neither can make a placement impossible. Anything hard that the simulation cannot evaluate is named in the `unmodelled` list rather than assumed satisfied — an empty list is the claim that nothing was skipped
- Background and the current verdict: [docs/memory-efficiency/07-drain-feasibility.md](memory-efficiency/07-drain-feasibility.md)

## Grafana Git Sync

Connection and Repository live in Grafana's own API server — invisible to `kubectl` and to ArgoCD — so `platformctl gitsync` is the only sanctioned way to read or reset them.

- Diagnose: `platformctl gitsync status` — TOON output, four default fields (`kind,name,healthy,syncState`); the full health message is printed for anything not healthy, and `--full` adds it for everything. Exits non-zero when any resource is unhealthy, when a resource reports no health at all, or when no resources exist (credentialed but not connected)
- Change a definition: merging an edit to `gitsync-resources.yaml` alone does nothing, because the apply Job creates but never updates. `platformctl gitsync recreate --repository <n> --dry-run` then `--confirm` deletes the repository **before** the connection and asks ArgoCD to re-run the apply Job. `--repository` is required whenever more than one exists
- Adding is not changing: a repository that does not exist yet is created by the ordinary sync, so a **new** folder needs no `recreate` — only an edit to an existing definition does
- One connection serves many repositories, so `recreate` deletes the connection only when no other repository still binds to it, and reports the one it retained. Do not read a single-delete plan as a missed step
- Change the **connection** definition (rotated GitHub App key, edited `connection.json`): `platformctl gitsync recreate --repository <n> --with-connection --confirm`. It deletes every repository bound to that connection and then the connection, because a connection cannot be deleted underneath a repository that still references it, and the apply Job brings all of them back in one run. Without the flag the connection is never reached once a second repository exists
- Single resource: `platformctl gitsync delete --kind repository|connection --name <n> --confirm`
- Both delete paths refuse a repository that still owns dashboards (its remove-orphan-resources finalizer would collect them; override with `--allow-owned-dashboards`) and refuse a connection a repository still references — the refusal names every repository blocking it and the `--with-connection` command that clears them in order
- With `sync.target: folder` a repository's `metadata.name` **is** the created folder's UID and `spec.title` its title. Nothing aims a repository at an existing folder, and one whose name collides with a folder created outside provisioning cannot adopt it — it stops with an unmanaged-collision error and syncs nothing. `status` reports neither the path nor the folder, so this is not visible from the CLI
- A synced folder is created without folder-RBAC, which means readable by every Grafana user. A tenant's `<tenant>-dashboards` folder is granted to its team by tenant-envelope's `PostSync` hook (`tenant-<t>-grafana-gitsync-folder-rbac`) via `observability.grafana.gitSyncFolder` in `tenant.yaml`. Adding a tenant folder means all three of: that key, a `Repository` definition, and a `TENANT_REPOSITORIES` line — `tools/check-gitsync-tenant-folders.py` fails CI on any subset, because every missing piece is silent at runtime
- Deleting a tenant repository deletes its folder (the remove-orphan-resources finalizer owns it) and the folder that comes back is open until that tenant's hook re-runs. `recreate` starts a real **sync** of `governance-<tenant>` alongside the `platform-grafana` refresh and reports both — a refresh there would run no hooks at all, because nothing about that Application changed and an Application that compares `Synced` creates no sync operation; `gitsync delete` requests no sync, so it **refuses** a repository a tenant claims, and refuses the same way when the claim cannot be read at all, unless `--accept-open-folder` is passed
- Only `platform-dashboards` is gated by the apply Job, for creation and health alike. It is an ArgoCD `Sync` hook, so a hard gate on a tenant repository would fail the whole `platform-grafana` sync; tenant repositories are warned about and left to `gitsync status`
- Folder permissions on a provisioned folder need the `provisioningFolderMetadata` feature toggle, pinned in the grafana service's `values.yaml`. Without it Grafana answers the permissions write with 403 and every tenant folder stays open
- A health message never names its own cause: a connection reporting `GitHub App lacks required 'webhooks' permission` is describing a requirement derived from a bound repository's `write` workflow, not a missing grant on the App

## Alerting inspection

`cluster status` only asserts that the `alertmanager-config` Secret exists — that says nothing about whether an alert fires, where it routes, or whether the metric a rule references is ever scraped at all. `platformctl cluster alerts` reads that live state over Prometheus's and Alertmanager's own HTTP APIs, reached the same way `gitsync` reaches Grafana's: an in-cluster `.svc` address is not resolvable from a workstation, so it is reached through an automatic port-forward (`--prometheus-addr`/`--alertmanager-addr`, or `PLATFORMCTL_PROMETHEUS_ADDR`/`PLATFORMCTL_ALERTMANAGER_ADDR`). Neither API needs credentials internally.

- `platformctl cluster alerts list` — active Alertmanager alerts with the receivers each one reaches, the acceptance evidence "this alert reaches these receivers" needs. TOON output, four default fields (`alertname,severity,receivers,startsAt`), widened by `--fields`/`--full`. Excludes silenced and inhibited alerts by default; `--include-silenced`/`--include-inhibited` widen it, `--receiver <name>` narrows
- `platformctl cluster alerts targets` — Prometheus scrape target health and last-scrape time. Health alone is not the check: a target can report Up while serving an empty response body, which is what a target being Up and genuinely idle looked like before this existed. Unless `--skip-samples-check`, each Up target also gets its `scrape_samples_scraped` meta-metric read, and Up-with-zero-samples is reported unhealthy alongside Down. Exits non-zero when any reported target is unhealthy; `--job <name>` narrows
- `platformctl cluster alerts rules` — alerting rules (recording rules excluded) with `seriesStatus`: `found` when Prometheus currently has series for a metric name extracted from the rule's query, `missing` when none of the extracted candidates does, `unknown` when no candidate could be extracted at all. A rule referencing a metric nothing emits stays permanently inactive and reads as coverage, which is worse than no rule. `unknown` is excluded from the exit-code gate — it is a limit of the extraction, not a verdict. `--skip-series-check` reports rule state only; `--group <name>` narrows. Exits non-zero on a rule reporting `err` health or a `missing` series
- The series check is a left-to-right scan of the query text (`internal/monitoring.CandidateMetricNames`), not a PromQL parser — it excludes label names, `by`/`without`/`on`/`ignoring` label lists, and quoted string contents, but can still miss an unusual expression shape. Its false positives fail safe: a rule is only ever reported `missing` when every extracted candidate comes back with no series, so a spurious extra candidate can only push a verdict toward `found`, never invent a false `missing`

## Seeding one Vault field

- `platformctl bootstrap seed <spec> --field <name>` writes individual properties of one spec, so a new field can be added without re-supplying or being prompted for the credentials already at that path. Repeatable; requires exactly one spec argument; a field named explicitly is written even where the spec marks it optional
- An unknown spec key or field name is now an error listing the valid set. Previously an unrecognised key selected an empty spec, wrote nothing, and still reported success — which is how a binary older than the seed spec it is asked to write skips the field silently
- Seeding has no preview mode. `--dry-run` is accepted **only** by `cluster volumes reclaim`, `cluster volumes truenas reclaim`, `gitsync delete`, and `gitsync recreate` — the four commands that implement it. Every other command, `bootstrap seed` included, rejects the flag with an unknown-flag error rather than mutating while reporting a preview
- With no terminal attached (every agent), `--from-file <path>` or `--from-file -` (stdin) is the only seed path; there is deliberately no `--value` flag. Byte rules, the refusal message, and why: [OPERATIONS.md §6.1](OPERATIONS.md#61-seeding-one-credential-with-no-tty)

## Heal subcommand index (idempotent — safe to re-run)

| Subcommand                                                       | Effect                                       |
|------------------------------------------------------------------|----------------------------------------------|
| `bootstrap heal --stuck-finalizer --kind <kind> --name <name>`   | Strip metadata.finalizers                    |
| `bootstrap heal --default-project`                               | Apply bootstrap/argocd/projects/default.yaml |
| `bootstrap heal --cert-approver`                                 | Trigger ArgoCD refresh of cert-approver App  |
| `bootstrap heal --tls-reissue`                                   | Delete cert-manager-managed TLS secrets      |
| `bootstrap heal --orphan-namespaces`                             | Delete tenant-labeled ns with no tenant.yaml |
| `bootstrap heal --longhorn-fresh-install`                        | Create Longhorn SA + RBAC for pre-upgrade hook on fresh cluster |
| `bootstrap heal --stuck-sync --sync-app <name>`                  | Terminate stuck ArgoCD sync (Helm hook Job TTL race)           |
| `bootstrap heal --all`                                           | Run every healer in safe order               |
