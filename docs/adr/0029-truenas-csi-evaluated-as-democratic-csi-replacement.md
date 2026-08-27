# ADR: truenas-csi evaluated as democratic-csi replacement — adopt as target, migration not yet executed

Status: accepted, no cluster change deployed. This is a research/design record for
JDWLABS-400. It recommends a target driver and sketches a migration path; it
does not migrate any PVC, install any CSI driver, or touch live cluster state.

## Problem

ADR-0024 recorded that `democratic-csi` — the driver behind both TrueNAS-backed
storage classes, `truenas-nfs` and `truenas-iscsi` — cannot speak TrueNAS's
replacement WebSocket/JSON-RPC API on any released or unreleased build, and
gated the NAS at 25.10.x until upstream ships one. As of that record (2026-08-17)
no such code existed on any democratic-csi branch and no open PR mentioned it.

JDWLABS-400 asks whether `truenas/truenas-csi` — iXsystems' own driver,
first published 2025-12-15 — is a viable replacement now that it exists and
already speaks the WebSocket API. If it is, `democratic-csi`'s REST dependency
stops being a standing gate on the NAS's upgrade path; if it isn't, the gate
from ADR-0024 stands and this records why.

This is a driver swap, not a config change. `PersistentVolume.spec.csi.driver`
is immutable, so an existing PVC cannot be repointed at a new provisioner —
whatever the decision, cutting over means new PVs, not edited ones.

## What democratic-csi is actually configured to do here

Read directly from `tenants/platform/services/democratic-csi/` (NFS) and
`tenants/platform/services/democratic-csi-iscsi/` (iSCSI) — chart
`democratic-csi` rev `0.15.1`, two releases, one driver each:

| | `truenas-nfs` | `truenas-iscsi` |
|---|---|---|
| Driver | `freenas-api-nfs` | `freenas-api-iscsi` |
| Dataset root | `storage/k8s/vols` | `storage/k8s/iscsi/vols` |
| Quotas/reservation | quotas on, reservation off | zvol reservation off, `zvolBlocksize: 16K` |
| Access | `shareAllowedNetworks: 192.168.1.0/24`, mapall root/root | `namePrefix csi-`/`nameSuffix -cluster`, `targetGroupAuthType: None` (no CHAP), portal `192.168.1.205:3260` |
| Expansion | `allowVolumeExpansion: true` | `allowVolumeExpansion: true` |
| Snapshots | sidecar deployed (`externalSnapshotter`), **no `VolumeSnapshotClass` exists anywhere in this repo** | same |
| Talos accommodation | none needed | `hostPID: true` + `ISCSIADM_HOST_STRATEGY: nsenter` — Talos's kubelet mount namespace doesn't resolve the chart's default `/etc/iscsi` hostPath, and iSCSI needs the host's own `iscsiadm`, not one shipped in the container |
| Known live mitigation | controller `livenessProbe` disabled — the chart hardcodes it straight through to the driver's `Probe()` RPC, which fails on any transient NAS blip and had produced 189 restarts in 19 days | forced `fsck -f -p` before every `ext4` stage — a session drop mid-write aborts the journal and latches the mount read-only, and nothing else in the attach path replays it |

Both classes use `reclaimPolicy: Retain`, so a deleted PVC never deletes the
NAS object — `platformctl cluster volumes truenas` exists specifically to find
and reclaim what's left behind. Neither class is the cluster default; Longhorn
is, and carries 14 of 17 PVCs per ADR-0022.

**Snapshots are not actually usable today by either driver.** No
`VolumeSnapshotClass` exists in this repo, and no `snapshot-controller` or
`snapshot.storage.k8s.io` CRDs are installed anywhere in `bootstrap/` — checked
across the whole tree. The `externalSnapshotter` sidecar in both democratic-csi
releases has nothing to talk to. This is a cluster-wide prerequisite gap, not a
driver-specific one, and it applies identically to whichever CSI driver runs.

## truenas-csi: what it is and what it supports

Read from the driver's own repository (`truenas/truenas-csi`, GPLv3, created
2025-12-15, 91 stars, four releases through `v1.2.0` on 2026-08-24 — two days
before this evaluation) — `README.md`, `examples/*.yaml`, and
`deploy/truenas-csi-driver.yaml` read directly, not paraphrased from search
summaries.

- **Transport**: WebSocket, `wss://<host>/api/current` — the API TrueNAS is not
  removing. Requires **TrueNAS SCALE 25.10.0+**, already satisfied (NAS runs
  25.10.4 per ADR-0024, 25.10.6 is current).
- **Protocols**: NFS (RWX), iSCSI (RWO, RWX only with a cluster filesystem this
  cluster doesn't run), and NVMe-oF/TCP (new capability, unused today).
- **NFS parameters**: `nfs.hosts`/`nfs.networks` (≈ `shareAllowedNetworks`),
  `nfs.mapAllUser`/`nfs.mapAllGroup` (≈ `shareMaprootUser`/`shareMaprootGroup`),
  plus `nfs.rootSquash: false` for `no_root_squash` — a capability democratic-csi's
  config here doesn't use but that this repo's own README notes is needed for
  ownership-sensitive non-root workloads (PostgreSQL/CNPG). Parity or better.
- **iSCSI parameters**: `volblocksize` (≈ `zvolBlocksize`), `iscsi.blocksize`
  (≈ `extentBlocksize`), plus CHAP/mutual-CHAP, multipath and persistent-session
  options this cluster's config doesn't currently turn on
  (`targetGroupAuthType: None`). Parity or better. **iSCSI portals must be
  IPv4** — a real, documented driver limitation, but this cluster's portal
  (`192.168.1.205:3260`) already is.
- **Expansion**: `allowVolumeExpansion: true`, documented as functional online
  for NFS; iSCSI may need a pod restart for the filesystem to see the new size.
  Parity.
- **Snapshots**: full `VolumeSnapshotClass`/`VolumeSnapshot` support, **plus**
  StorageClass-native scheduled snapshot tasks (`snapshot.schedule` cron,
  retention count/unit, recursive) that democratic-csi has no equivalent of in
  this cluster's config. Requires the same external `snapshot-controller` +
  CRDs this cluster doesn't have installed for *either* driver today — not a
  gap specific to truenas-csi.
- **Encryption**: ZFS dataset-level encryption via StorageClass parameters
  (passphrase, generated key, or hex key). Not something democratic-csi's
  config here uses; a genuinely new capability if adopted.
- **No fsck-before-mount equivalent is documented.** democratic-csi's
  forced-preen-before-stage mitigation for the ext4 journal-abort-on-session-drop
  failure mode (see table above) has no stated counterpart in truenas-csi's
  README or examples. This is the one concrete regression risk identified —
  not confirmed absent, just not documented, and it directly matters for
  `truenas-iscsi`'s only live consumer (see below).
- **No Helm chart** — deployment is a single raw manifest
  (`deploy/truenas-csi-driver.yaml`) or an OpenShift operator/OperatorHub
  bundle. This repo's GitOps model (`tenants/<t>/tenant.yaml` → Helm chart +
  repo + revision) has nothing to point at for a chartless driver; adopting it
  means either vendoring a thin local chart under `helm-charts/` (the pattern
  this repo already uses for `kubelet-serving-cert-approver`) or driving it via
  Kustomize, so that image-digest pinning and ExternalSecret-sourced config
  keep working the way every other platform service here does. This is
  implementation work, not evaluation, and is unstarted.
- **The shipped manifest hardcodes `hostPath: /etc/iscsi`** for its node
  DaemonSet's iscsi-info mount (`deploy/truenas-csi-driver.yaml:453,529`) — the
  exact path this cluster's democratic-csi-iscsi values.yaml documents as
  failing under Talos ("`hostPath type check failed: /etc/iscsi is not a
  directory`"), which is why that release overrides it to
  `/var/etc/iscsi` with `DirectoryOrCreate`. truenas-csi's node DaemonSet does
  already run `hostPID: true` with an `nsenter`-based `iscsiadm` shim built in
  (not something this cluster would need to add, unlike the chart-level
  workaround democratic-csi needed) — but the hostPath itself would need the
  same kind of redirect here, this time as a manifest/Kustomize edit rather
  than a values knob. Untested against this cluster; flagged as a known day-one
  blocker to solve before any real deployment attempt.
- The node DaemonSet also sets `hostNetwork: true`. Host-network pods get no
  `CiliumEndpoint` and no NetworkPolicy ever resolves against them (the same
  fact ADR-0028 records for pre-agent pods) — worth a deliberate review against
  this cluster's chained-Cilium NetworkPolicy posture (ADR-0012/0013) before
  deployment, not a blocker on its own.

**No import, adoption, or migration path for pre-existing volumes exists.**
Checked the repository's file tree (`docs/`, `examples/`, `operator/`) and its
issue tracker (searched `migrat`, `import`, `democratic` against
`repo:truenas/truenas-csi`) — nothing. The driver's `CreateVolume` path always
creates a new dataset/zvol under `datasetPath`; there is no documented static-PV
/ pre-provisioned-volume convention publishing its `volumeHandle` format, and
reverse-engineering one against production data without an upstream contract
is not something to attempt. Whatever the decision, existing PVCs move by
copying data, not by relabeling them.

## Naming collision worth flagging now, before it causes a real mistake

Vault path `kv/truenas-csi` and its `api_key` field are **already in use** —
they hold democratic-csi's own TrueNAS API key (see both
`postInstall/external-secret.yaml` files), and `platformctl`'s
`--truenas-use-csi-api-key` flag reads that same key by name. If this driver
is adopted, its credential must live under a **differently named** Vault path
(e.g. `kv/truenas-csi-driver`) — reusing `kv/truenas-csi` would either silently
share a key across two unrelated CSI control planes or require renaming a path
`platformctl` already depends on. ADR-0025 also documents how narrow this
key's blast radius already is: four failed auth attempts against it took every
class of provisioning down at once and required a human to regenerate it in
the TrueNAS UI. A second consumer of the same key doubles that exposure for no
reason.

## Migration path — sketch only, not executed

**No clean import exists (see above), so the fallback is what the ticket
itself names**: provision new PVs on truenas-csi-backed classes, copy data at
the application layer, cut over, then reclaim the old objects. Concretely, per
workload:

1. **Identify every live consumer before touching anything.** Static search of
   this checkout (the `platform`, `apps`, `infrastructure` repos) found two:
   Loki's `singleBinary` PVC (`tenants/platform/services/loki/values.yaml`,
   `storageClass: truenas-nfs`, 30Gi) and, per ADR-0022, a Minecraft Bedrock
   world PVC on `truenas-iscsi` — the latter is deployed via the `jdwillmsen`
   tenant's external `deploymentRepo`, not checked out in any of the four
   sibling repos this search covered, so its manifest wasn't directly read.
   ADR-0024 recorded **three** PVCs live on these classes as of 2026-08-17 (two
   `truenas-nfs`, one `truenas-iscsi`); this pass accounts for the iSCSI one
   and one of the two NFS ones. **The second `truenas-nfs` consumer was not
   located in this search and must be found with
   `platformctl cluster volumes truenas list --storage-class truenas-nfs`
   (or `kubectl get pvc -A` filtered by storage class, if that's re-permitted
   for this one read) before any migration work starts** — this record does
   not claim full coverage of live state, only of what four repos' committed
   config shows.
2. **Stand up truenas-csi alongside democratic-csi, not instead of it**,
   pointed at a disjoint `datasetPath` (e.g. `storage/k8s/truenas-csi/nfs`) so
   the two control planes never touch each other's objects on the same NAS.
   Prove it first against a scratch PVC with no real data — verify the Talos
   hostPath fix, verify volume expansion, and specifically probe the
   fsck-on-stage question flagged above by forcing a simulated NAS-side session
   drop against an ext4-formatted iSCSI volume and confirming the filesystem
   recovers without a wedged read-only mount. This is the proof-of-concept
   step JDWLABS-400 itself scopes out of this pass; it is the necessary
   next step, not a nice-to-have, given the one identified regression risk.
3. **Per workload, in ascending order of risk (Loki before Minecraft — Loki's
   data is disposable log history, Minecraft's is a live game state with its
   own backup/consistency concerns per ADR-0022):**
   - Provision a new PVC on the truenas-csi StorageClass, sized at or above
     current usage.
   - Quiesce writes (scale the workload to zero, or pause it).
   - Copy data at the application layer — `rsync` between two mounted paths in
     a helper pod for the NFS case; mount both the old ext4 block device and
     the new one in a helper pod with the iSCSI initiator available and
     `rsync -a` the filesystem contents for the iSCSI case (not a ZFS
     send/receive of the zvol — the two drivers lay out datasets differently,
     and copying at the filesystem layer is what actually needs to survive).
   - Repoint the workload's `storageClassName` to the new class and let it
     restart against the new PVC.
   - Verify functionally per workload (Loki: query log ranges spanning the
     cutover; Minecraft: load the world and confirm state, per ADR-0022's own
     "a restore is tested into a scratch PVC and booted" standard).
   - Leave the old PV as `Retain` through a burn-in period rather than deleting
     it immediately — both classes already default to `Retain`, so nothing
     extra is needed here — then reclaim the orphaned zvol/dataset with the
     already-built `platformctl cluster volumes truenas reclaim` once confident.
4. **Only after every PVC on both classes has moved** do the two
   `democratic-csi`/`democratic-csi-iscsi` ArgoCD Applications get decommissioned
   and the REST-deprecation alert from ADR-0024 finally clears on its own (it
   self-clears once the calls stop, per that record).

None of this is executed by this change. Standing up truenas-csi, seeding its
credential, authoring its manifest/chart, and moving even one PVC are explicitly
out of scope for this research pass and are the next ticket(s).

## Options considered

**Adopt truenas-csi, migrate now, in this pass.** Rejected for this ticket —
the ticket's own scope excludes it ("touches live storage backing real data,
treat with the same care as any storage-provisioner change"), and no
proof-of-concept has been run yet to validate the one identified regression
risk (fsck-on-stage) or the Talos hostPath fix.

**Stay on democratic-csi indefinitely, revisit only if truenas-csi matures.**
Rejected as the standing position, though it's the safe default for *this*
pass. No feature gap was found that favors staying — every parameter this
cluster actually uses on either class has a truenas-csi equivalent, several
capabilities it doesn't use today (native encryption, scheduled snapshot
tasks, `no_root_squash` for non-root workloads) are strictly additive, and the
core problem ADR-0024 recorded — democratic-csi has *no* path to the API
TrueNAS is keeping — does not change by waiting. Every month on `democratic-csi`
is another month gated at TrueNAS 25.10.x with no upstream fix in sight, while
truenas-csi is the *official* driver and has shipped four releases (including
a real bug fix, the IPv6 iSCSI issue, filed and closed against its own tracker)
in under three months.

**Move the three affected workloads to Longhorn instead of any TrueNAS driver.**
Not evaluated in depth here — out of scope for this ticket, which asks
specifically about `truenas-csi` as the `democratic-csi` replacement — but
noted in ADR-0024 as an escape route that exists because Longhorn already
carries 14 of 17 cluster PVCs. Worth a look if the truenas-csi POC in step 2
above turns up a disqualifying finding.

## Decision

**Adopt `truenas-csi` as the target driver for both `truenas-nfs` and
`truenas-iscsi`.** No feature this cluster's democratic-csi configuration
relies on is missing; several are strictly improved; and the core blocker from
ADR-0024 — no path to TrueNAS's replacement API — is exactly what the new
driver already speaks.

**The migration itself is not authorized by this record.** It requires, in
order: a live inventory of every PVC on both classes (not fully established
here), a proof-of-concept deployment validated against the fsck-on-stage and
Talos-hostPath risks named above, a decision on how the chartless upstream
manifest fits this repo's Helm+ArgoCD model, a freshly seeded and
distinctly-named credential, and a human-reviewed cutover plan per workload —
each a separate, reviewed change given this touches live storage backing real
data.

**ADR-0024's gate stands unchanged in the meantime.** The NAS does not move
past 25.10.x, and the REST-deprecation alert stays live, until democratic-csi
is fully decommissioned — not merely until truenas-csi is deployed alongside
it — because both drivers would otherwise depend on the NAS staying below 26.

## Consequences

- This ticket's own proof-of-concept deliverable ("at least one real PVC
  successfully migrated and verified") is explicitly not attempted here. It is
  the clear next step, gated on human review of this record.
- The naming collision on `kv/truenas-csi` is flagged now specifically so the
  next implementation ticket doesn't reuse that path by accident.
- The fsck-on-stage gap is the one concrete open question a POC must answer
  before the Minecraft world PVC — the highest-consequence migration — is
  attempted.
- No chart exists to vendor yet; the next ticket needs to decide between a
  local Helm chart under `helm-charts/` and a Kustomize-driven Application
  before any manifest is written.

## Non-goals

This record does not migrate any PVC, install truenas-csi, seed any credential,
or change `docs/OPERATIONS.md`'s TrueNAS-26 gate. It does not evaluate moving
the three affected workloads to Longhorn in depth, and it does not resolve
which of the two NFS PVCs ADR-0024 counted was not located in this search —
that is a live-state read for the next step, not a design question this
record can settle from committed config alone.
