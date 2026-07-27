# Runbook: Longhorn engine upgrade (per-volume, live)

Status: PLANNED — every mutation in this runbook (engine upgrade trigger,
EngineImage deletion, volume deletion) is executed by a human. The agent
contract forbids autonomous cluster mutation of the storage layer; agents may
run the read-only inspection commands here (`kubectl get`, `kubectl describe`)
but never the mutating steps.

Scope: upgrading the **Longhorn data-plane engine** on existing volumes from
v1.11.1 to v1.12.0, in place, without detaching. Upgrading the **Longhorn
chart/manager** is a separate, already-completed operation (chart moved
1.11.1 → 1.12.0 in the platform dependency-bump wave) — see
[Where the target comes from](#where-the-target-comes-from). Deleting the two
detached orphan volumes is in scope only as a *decision point*, not an
execution step — see [Orphan volumes](#orphan-volumes-and-the-vault-coupling).

## Why

Longhorn manager and engine versions are allowed to skew by one minor version
(manager ahead of engine is explicitly supported), but not by two. This
cluster's manager moved to v1.12.0 while every volume's engine stayed on
v1.11.1 — a legal one-minor gap today, but the *next* chart bump (1.13.0)
would open a two-minor gap that Longhorn does not support, and would risk
volume-attachment failures across the whole cluster. Renovate will raise that
1.13.0 bump on its own schedule regardless of whether the engines have been
closed up, so the gap needs to close deliberately, in a chosen window, not
be discovered the day a volume fails to attach.

The automatic engine-upgrade path
(`concurrent-automatic-engine-upgrade-per-node-limit`) is deliberately 0 on
this cluster. Longhorn's instance-manager footprint scales with attached
volumes and rebuild activity, and this cluster has a documented history of
instance-manager cascade failures. An unattended, cluster-wide automatic
engine upgrade is exactly the kind of surprise that history warns against —
which is why this runbook is a manual, batched, human-executed procedure
instead of flipping that limit up.

## Live state (verified 2026-07-26)

**Chart / control plane** — already at target, no action needed here:

- `longhorn-manager`, `longhorn-ui`, `longhorn-driver-deployer`,
  `longhorn-csi-plugin` DaemonSet/Deployment images: `v1.12.0`
- Longhorn setting `current-longhorn-version`: `v1.12.0`
- `tenants/platform/tenant.yaml` pins `revision: 1.12.0` for the `longhorn`
  service entry (bumped from `1.11.1` in a dependency-bump commit) — this is
  already merged and live, not part of this runbook's execution
- `helm template` of the chart at 1.12.0 against the current
  `tenants/platform/services/longhorn/values.yaml` renders cleanly (5235
  lines, no schema errors) — confirms no values-schema drift between the
  pinned chart version and what's actually deployed

**Engine images** (`kubectl -n longhorn-system get engineimages.longhorn.io`):

| Name | Image | State | Incompatible | Refcount |
|---|---|---|---|---|
| `ei-75a03ec3` | `longhorn-engine:v1.11.1` | deployed | false | 68 (in use) |
| `ei-a4d05f02` | `longhorn-engine:v1.12.0` | deployed | false | 0 (unused) |

Both engine-image DaemonSets are fully rolled out (5/5 on worker nodes). The
v1.12.0 image has been available cluster-wide for ~19h with zero volumes on
it yet — automatic engine upgrade is disabled
(`concurrent-automatic-engine-upgrade-per-node-limit: 0`, chart default, not
overridden in values.yaml), so nothing will move without a manual trigger.

**Volumes** (`kubectl -n longhorn-system get volumes.longhorn.io`): 14 total,
all reporting `.spec.image` / `.status.currentImage` =
`longhorn-engine:v1.11.1`.

- 12 attached, `robustness: healthy`, no volume rebuilding or degraded
- 2 detached, `robustness: unknown` (expected for detached volumes — not a
  fault) — these are the two orphan volumes, see below

Data engine in use cluster-wide is v1 only (`v1-data-engine: true`,
`v2-data-engine: false`) — the v1.12.0 release's V2-specific breaking changes
(V2 backing-image removal, V2 volumes needing detach-before-upgrade) do not
apply here.

**Volume → workload map** (attached volumes only, orphans excluded):

| Volume | Size | Namespace / PVC | Workload |
|---|---|---|---|
| `pvc-0e353eaf` | 25Gi | monitoring / `prometheus-...-0` | Prometheus TSDB |
| `pvc-35ea9ba2` | 20Gi | monitoring / `storage-platform-tempo-0` | Tempo |
| `pvc-883daf08` | 2Gi | monitoring / `platform-grafana` | Grafana |
| `pvc-ab5fca39` | 2Gi | monitoring / `alertmanager-...-0` | Alertmanager |
| `pvc-163cb5b7` | 8Gi | database / `platform-postgresql-cluster-non-1` | CNPG non-prod |
| `pvc-7ea0eda1` | 8Gi | database / `platform-postgresql-cluster-non-6` | CNPG non-prod |
| `pvc-970b2582` | 8Gi | database / `platform-postgresql-cluster-non-3` | CNPG non-prod |
| `pvc-416b365f` | 8Gi | database / `platform-postgresql-cluster-prd-11` | CNPG prod |
| `pvc-9f82dad9` | 8Gi | database / `platform-postgresql-cluster-prd-6` | CNPG prod |
| `pvc-b146781b` | 8Gi | database / `platform-postgresql-cluster-prd-10` | CNPG prod |
| `pvc-8743f797` | 5Gi | ai-sre / `platform-litellm-db-cluster-1` | litellm DB |
| `pvc-ae4ffd70` | 10Gi | vault / `data-platform-vault-0` | Vault (sole active replica) |

Re-derive this table live before executing — `kubectl -n longhorn-system get
volumes.longhorn.io` and `kubectl get pvc -A -o
custom-columns=NAMESPACE:.metadata.namespace,PVC:.metadata.name,VOLUME:.spec.volumeName`
— and stop if anything disagrees.

## Orphan volumes and the Vault coupling

The ticket's "two orphan volumes" are:

- `pvc-a0152d45-b0d2-4d14-b19a-7c5fe91e5ae5` (10Gi, detached) — bound to PVC
  `vault/data-platform-vault-2`
- `pvc-df6c5715-e351-4553-8e44-1a87a9519e3c` (10Gi, detached) — bound to PVC
  `vault/data-platform-vault-1`

**These are confirmed to be the same two volumes tracked for deletion under
the Vault raft-migration runbook** (leftovers of a reverted Vault HA attempt,
when the StatefulSet ran 3 replicas; it is now `replicas: 1` with only
`platform-vault-0` running).

**Critical finding — the ticket's own precondition is not currently met.**
The ticket says these volumes are "safe to delete once `kubectl get pvc -A`
shows nothing bound to them — check before, not after." Checked: both PVCs
(`data-platform-vault-1`, `data-platform-vault-2`, created 2026-06-08) are
still present in the `vault` namespace with `status.phase: Bound`, each still
`claimRef`-linked to its PV. Kubernetes StatefulSets do not delete PVCs on
scale-down by default, so these PVC objects outlived the pods that created
them. The Longhorn volumes look "orphaned" from Longhorn's point of view
(detached, no active workload), but from Kubernetes' point of view they are
still claimed.

**Consequence: do not delete these volumes as part of this ticket.** Deleting
a Longhorn volume out from under a `Bound` PVC would desync the PV/PVC
objects and is exactly the kind of independent action the Vault raft
migration runbook needs to own, sequenced with its own PVC-deletion step
first. This ticket should:

1. Upgrade the engine on the 12 attached volumes (below).
2. Leave the two orphan volumes and their PVCs untouched — do not attach
   them just to bump their engine version, and do not delete them here.
3. Record in this ticket that the "orphan volumes" deliverable is **blocked
   on / superseded by** the Vault raft migration runbook, not independently
   actionable, and hand the actual deletion to that runbook (delete
   `data-platform-vault-1`/`-2` PVCs first, confirm the PVs go `Released`,
   then delete the PVs/volumes).
4. If the Vault raft migration is deprioritized or abandoned outright, that
   decision — not this ticket — is what should trigger cleanup of these two
   PVCs/volumes.

This means the engine-upgrade DoD ("all 14 volumes at v1.12.0") cannot be
literally satisfied by this ticket alone without either forcing an
unnecessary attach cycle on dormant Vault volumes or deleting them out of
sequence. Recommend revising the DoD to: **12 attached volumes upgraded to
v1.12.0, engine**; the 2 detached orphan volumes explicitly deferred to the
Vault raft migration runbook, tracked there rather than silently dropped.

## Preconditions — hard gates, all must pass

1. **Chart/manager already at target.** Confirmed above — no chart action
   needed, this runbook is engine-only.
2. **Live upgrade is supported for this version pair.** v1.11.x → v1.12.0
   live engine upgrade is supported for V1 Data Engine volumes per upstream
   docs ([longhorn.io/docs/1.12.0/deploy/upgrade/upgrade-engine](https://longhorn.io/docs/1.12.0/deploy/upgrade/upgrade-engine/)).
   This is a single-minor bump — no intermediate version hop required. V2
   Data Engine restrictions in 1.12.0 do not apply (cluster is V1-only).
3. **All volumes to be upgraded are `Healthy`, attached, not rebuilding.**
   `kubectl -n longhorn-system get volumes.longhorn.io -o
   custom-columns=NAME:.metadata.name,STATE:.status.state,ROBUSTNESS:.status.robustness`
   — re-run between every batch, not just once at the start.
4. **ArgoCD quiescent for `longhorn`.** `kubectl -n longhorn-system get
   application longhorn -n argocd` (or `platformctl cluster status`) shows
   Synced+Healthy — an in-flight sync fighting a live engine-upgrade patch is
   how this gets messy.
5. **Low-traffic window**, per the ticket's own guidance — batch a few
   volumes at a time, not all 12 at once.
6. **`concurrent-automatic-engine-upgrade-per-node-limit` stays at 0** for
   this runbook. Do not raise it as part of this work — that's a separate
   decision with its own blast radius (an unattended limit bump could kick
   off engine upgrades on every volume at once across all nodes).

## Decision to record: `concurrentAutomaticEngineUpgradePerNodeLimit`

Recommend: **leave at 0**, and make the choice explicit in
`tenants/platform/services/longhorn/values.yaml` (currently relies on the
chart's implicit default rather than stating the decision) so a future
Renovate bump or values refactor doesn't silently change it:

```yaml
defaultSettings:
  # ... existing keys ...
  # Automatic engine upgrade stays disabled: this cluster has a documented
  # history of instance-manager cascade failures, and instance-manager
  # memory footprint scales with attached volumes and rebuild activity.
  # Engine upgrades are done manually, in a batched window, per the
  # engine-upgrade runbook — an unattended cluster-wide upgrade is the
  # opposite of that.
  concurrentAutomaticEngineUpgradePerNodeLimit: 0
```

This is a documentation-of-decision change, not a behavior change (0 is
already the effective, chart-default value) — safe to land independently of
the engine-upgrade batches below.

## Sequence — batched, least-critical first

Order chosen so the procedure proves itself on lower-stakes volumes before
touching CNPG prod and Vault. Confirm `robustness: healthy` on every volume
in the *next* batch immediately before triggering it, and again on the whole
fleet after it settles, before starting the next batch.

Per-volume trigger (this is what the Longhorn UI's "Upgrade Engine" button
does under the hood — a human performs this, via UI or the equivalent
`kubectl patch`, not an agent):

```
kubectl -n longhorn-system patch volumes.longhorn.io <volume-name> \
  --type merge -p '{"spec":{"image":"docker.io/longhornio/longhorn-engine:v1.12.0"}}'
```

Per-volume verification after triggering: watch
`status.currentImage` converge to v1.12.0 and `status.state` stay `attached`
throughout (a live upgrade replaces the engine process without detaching);
watch `status.robustness` — a live engine upgrade should not itself trigger a
replica rebuild. If it goes `degraded`, stop (see
[Abort criteria](#abort-criteria)).

### Batch 1 — monitoring (4 volumes, lowest stakes)

`pvc-0e353eaf` (Prometheus), `pvc-35ea9ba2` (Tempo), `pvc-883daf08`
(Grafana), `pvc-ab5fca39` (Alertmanager).

### Batch 2 — CNPG non-prod (3 volumes)

`pvc-163cb5b7`, `pvc-7ea0eda1`, `pvc-970b2582`.

### Batch 3 — CNPG prod + litellm DB (4 volumes)

`pvc-416b365f`, `pvc-9f82dad9`, `pvc-b146781b`, `pvc-8743f797`.

### Batch 4 — Vault (1 volume, highest stakes, alone)

`pvc-ae4ffd70` (`data-platform-vault-0`) — the sole active Vault replica,
backing the cluster secret store every ExternalSecret depends on. Run this
batch by itself, with nothing else in flight, and confirm Vault is unsealed
and serving reads/writes immediately after (`vault status` via the existing
auto-unseal CronJob logs, or `platformctl cluster status` Layer 2).

## Cleaning up the old engine image (clears JDWLABS-196's BestEffort pods)

**Explicit answer: the engine upgrade does not "recreate" the five BestEffort
`engine-image-ei-75a03ec3-*` pods as Burstable pods. It deletes them
outright — but only after one more explicit step.**

Verified:

- The `engine-image-ei-75a03ec3` DaemonSet (5 pods, all `BestEffort`, ages
  4d20h–27d — all predating the `longhorn-defaults` LimitRange created
  2026-07-26T00:19:44Z) is owned by the `EngineImage` custom resource
  `ei-75a03ec3` (`ownerReferences[0].blockOwnerDeletion: true`).
- By contrast, the new `engine-image-ei-a4d05f02` DaemonSet's 5 pods (ages
  ~19h, created *after* the LimitRange existed) are already `Burstable` —
  confirming the LimitRange's `defaultRequest: memory: 64Mi` does correctly
  admit new engine-image pods out of BestEffort; it just can't retroactively
  fix pods that already exist.
- Longhorn has no setting that auto-garbage-collects an unused, non-default
  `EngineImage` CR — `orphan-resource-auto-deletion` only covers
  `replica-data`. `ei-75a03ec3`'s refcount will drop to 0 once every volume
  above is upgraded, but the CR (and its owned DaemonSet/pods) will sit there
  until someone deletes it.

So the actual path to clearing the five BestEffort pods is:

1. Complete all 4 batches above; confirm `kubectl -n longhorn-system get
   engineimages.longhorn.io ei-75a03ec3` shows refcount 0 (note: the two
   deferred orphan volumes still reference `v1.11.1`, so refcount will not
   reach exactly 0 until those are also resolved — see
   [Orphan volumes](#orphan-volumes-and-the-vault-coupling). If the orphan
   volumes are still present and undeleted, `ei-75a03ec3` cannot be safely
   deleted yet, and the five BestEffort pods will persist until that
   dependency clears too.)
2. `kubectl -n longhorn-system delete engineimages.longhorn.io ei-75a03ec3`
   — this cascades (owner reference) to delete the `engine-image-ei-75a03ec3`
   DaemonSet and its 5 pods.
3. Verify: `kubectl -n longhorn-system get pods -l
   longhorn.io/component=engine-image` shows only `ei-a4d05f02-*` pods
   remaining, all `Burstable`.

This means JDWLABS-196's five BestEffort pods are coupled to the *same*
orphan-volume dependency as the "reclaim two volumes" deliverable: full
resolution of both requires the orphan volumes to be dealt with (via the
Vault raft migration runbook), not just the 12-volume engine batch above.
Partial credit is real, though — batches 1–4 plus this cleanup step get
`ei-75a03ec3`'s refcount down to just the 2 orphan volumes' replicas, at
which point deleting it is a decision explicitly tied to the Vault runbook's
timeline, not an open-ended wait.

## Abort criteria

- Any volume goes `degraded` or shows a rebuilding replica during or after a
  batch → stop before the next batch; investigate the specific replica
  before retrying.
- A volume detaches unexpectedly during its engine-upgrade trigger → stop;
  do not proceed to the next volume in the batch.
- Any CNPG cluster reports `Cluster` status not-healthy after its batch →
  stop; CNPG on Longhorn has a documented `input/output error` failure mode
  on remount (see `docs/OPERATIONS.md` §5) — investigate before continuing.
- Vault fails to auto-unseal or serve reads within a few minutes of its
  batch → stop immediately; this is the cluster's secret store.
- `longhorn-manager` pods restart or crash-loop during any batch → stop,
  this is the JDWLABS-22 cascade-failure signature.

## Rollback and recovery

- **Single volume, engine upgrade misbehaving**: Longhorn supports reverting
  a volume's `spec.image` back to the prior engine image
  (`docker.io/longhornio/longhorn-engine:v1.11.1`) the same way it was
  raised — patch it back. Confirm the volume returns to `healthy` before
  deciding whether to retry.
- **Volume stuck mid-upgrade (engine replica mismatch)**: per upstream engine
  upgrade docs, a stuck live upgrade is resolved by checking
  `status.currentImage` vs `spec.image` per replica; do not force-delete
  replicas without first reading Longhorn's own guidance for the specific
  stuck state.
- **Whole batch needs backing out**: this is why batches are small (3-4
  volumes) — the blast radius of a bad batch is bounded to that batch's
  workloads, not the whole fleet.

## Follow-up: close the detection gap

The ticket asks for "engine version added to whatever check would have
caught this skew." `platformctl cluster status` already has a Longhorn check
(`cli/internal/cluster/checks.go`, `operatorChecks`, Group "Operators", Name
"longhorn") but it only verifies the StorageClass exists
(`checkStorageClassExists(ctx, kube, "longhorn")`) — it says nothing about
engine version.

Recommend adding a sibling check (same file, same `operatorChecks` list)
that reads the `current-longhorn-version` setting and the distinct set of
`.spec.image` versions across `volumes.longhorn.io`, and:

- passes if all volumes match the manager's engine version,
- warns if there is a one-minor gap (today's supported-but-lagging state),
- fails if there is a two-minor-or-greater gap (the unsupported state this
  ticket exists to prevent).

This is a Go code change against `cli/internal/cluster/checks.go` — out of
scope for this investigation/plan, filed here as the concrete next step for
whoever implements it.

## Out of scope

- The Longhorn chart/manager upgrade itself — already done, live at 1.12.0.
- Raising `concurrent-automatic-engine-upgrade-per-node-limit` — explicitly
  rejected, see [Decision to record](#decision-to-record-concurrentautomaticengineupgradepernodelimit).
- Deleting the two orphan volumes/PVCs — owned by the Vault raft migration
  runbook, see [Orphan volumes](#orphan-volumes-and-the-vault-coupling).
- Implementing the `platformctl cluster status` engine-version check — see
  [Follow-up](#follow-up-close-the-detection-gap).

## Unverified — confirm before relying on these

- Whether a live engine upgrade on a CNPG-backed volume can coincide with an
  in-progress CNPG backup job (`postgres-backup` service) — recommend
  checking `platform-postgres-backup` CronJob schedule and avoiding overlap,
  not verified here.
- The exact behavior of `kubectl delete engineimages.longhorn.io` when
  refcount is not exactly 0 (expected: webhook rejects it) — not tested
  against this cluster's admission webhook; verify with a `--dry-run=server`
  first, per this repo's own documented Windows/kubectl gotcha that
  `--dry-run=server` still runs admission plugins.
