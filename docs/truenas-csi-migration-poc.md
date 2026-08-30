# truenas-csi migration proof of concept — runbook

Human-supervised procedure for validating the official `truenas-csi` driver
and migrating **one** low-risk PVC off democratic-csi, per
[ADR-0029](adr/0029-truenas-csi-evaluated-as-democratic-csi-replacement.md).
Nothing here is executed by merging the manifests; every step below is a
deliberate, human-run action. This touches live storage backing real data —
treat it with the same care as any storage-provisioner change.

Some steps use raw `kubectl` for one-off reads and scratch resources. That is
sanctioned here because this is a human-run procedure; agents remain bound to
`platformctl` per AGENTS.md.

## What the GitOps change delivers, and where it stands

- `helm-charts/truenas-csi/` — vendored chart wrapping upstream
  `deploy/truenas-csi-driver.yaml` at v1.2.0 (see that chart's README for the
  deliberate departures: digest pins, Talos iSCSI hostPath redirect, no
  Secret/Namespace, snapshotter off).
- Platform tenant service `truenas-csi` (wave 1), namespace `truenas-csi`,
  running **in parallel** with both democratic-csi releases. democratic-csi
  and its `truenas-nfs`/`truenas-iscsi` classes are untouched and stay
  authoritative.
- PoC StorageClasses `truenas-csi-nfs-poc` and `truenas-csi-iscsi-poc`
  (provisioner `csi.truenas.io`, `Retain`, non-default), pointed at dataset
  parents disjoint from democratic-csi's:
  `storage/k8s/truenas-csi/nfs` and `storage/k8s/truenas-csi/iscsi`.
- ExternalSecret `truenas-csi-driver-api-key` reading Vault path
  `kv/truenas-csi-driver`. Until a human seeds it (step 1) the ExternalSecret
  reports `SecretSyncedError` and the driver pods wait on the missing Secret —
  the expected pre-seed state, not a rollout failure.

### Live status (read 2026-08-30, read-only)

| Check | State |
|---|---|
| `CSIDriver` `csi.truenas.io` | present, `attachRequired`, `podInfoOnMount`, modes `Persistent,Ephemeral` |
| StorageClasses `truenas-csi-nfs-poc`, `truenas-csi-iscsi-poc` | present, `Retain`, expansion on, non-default |
| `truenas-csi-controller` Deployment | 1/1 available, pod 5/5 Running, 0 restarts |
| `truenas-csi-node` DaemonSet | 5/5 ready on all five Talos nodes, 3/3 containers, 0 restarts — the `/etc/iscsi` hostPath redirect holds |
| ExternalSecret `truenas-csi-driver-api-key` | `SecretSynced`, Ready — step 1 has already been done |
| ArgoCD `platform-truenas-csi` | **OutOfSync / sync Failed**: the controller Deployment manifest carries `strategy.type: Recreate` together with a `rollingUpdate` block, which the API server rejects. Pods stay Healthy on the last good revision. A chart fix is in flight separately; the Application must be Synced before step 3 starts, otherwise a values change made during the PoC cannot land |
| democratic-csi | both Applications Synced/Healthy; `truenas-nfs` and `truenas-iscsi` keep serving every existing PVC |

No PVC binds to either `-poc` class yet.

## 1. Seed the credential (human, one-time)

`kv/truenas-csi` is democratic-csi's key — do not reuse or touch it.

1. TrueNAS UI → profile → API Keys → create a new key. Name it so its
   consumer is obvious (e.g. `truenas-csi-driver-k8s`); keep it distinct from
   the democratic-csi key so either can be revoked alone.
2. Save the key to a file and seed it (the seed spec ships in the same PR;
   rebuild `platformctl` from `main` first if the installed binary predates
   it):

   ```bash
   platformctl bootstrap seed truenas-csi-driver --from-file ./truenas-csi-driver.key
   rm ./truenas-csi-driver.key
   ```

3. Within the ExternalSecret's 1m refresh the Secret renders and the
   controller Deployment and node DaemonSet in `truenas-csi` go Ready.
   Remember ADR-0025's lesson: an authentication attempt is a mutation —
   if the driver logs auth rejections, stop and check the key, never let it
   retry-loop.

## 2. Live PVC inventory (completed 2026-08-30, read-only)

ADR-0024 counted three PVCs on the democratic-csi classes on 2026-08-17 and
ADR-0029's static search accounted for two of them. The live read shows
**six** — the Minecraft chart in the `jdwillmsen` tenant's external
deploymentRepo (`jdwillmsen/jdw-deployments`, `charts/minecraft-fwb`) has
grown three PVCs since ADR-0024, and the "third" PVC ADR-0029 could not place
is that chart's backup archive volume. Every PV on both classes is
`org.democratic-csi.*`, `Retain`; nothing binds to `csi.truenas.io`.

| Namespace | PVC | Class | Size | PV csi.driver | Consumer | Data | Criticality |
|---|---|---|---|---|---|---|---|
| `monitoring` | `storage-platform-loki-0` | `truenas-nfs` | 30Gi | `org.democratic-csi.nfs` | STS `platform-loki` (`tenants/platform/services/loki/values.yaml`) | log chunks, index, WAL | **low** — disposable history, re-ingested going forward |
| `jdwillmsen-prd` | `jdwillmsen-minecraft-fwb-prd-backup` | `truenas-nfs` | 20Gi | `org.democratic-csi.nfs` | CronJob `…-backup` (nightly, keep 14) + backup exporter | world archives (safety net per ADR-0022) | **medium** — recoverable by the next nightly run, but it is the only restore source |
| `jdwillmsen-prd` | `jdwillmsen-minecraft-fwb-prd-afk-bot` | `truenas-nfs` | 1Gi | `org.democratic-csi.nfs` | Deployment `…-afk-bot` | Xbox token cache (a few JSON files) | **medium** — loss costs a human interactive device-code login |
| `jdwillmsen-prd` | `jdwillmsen-minecraft-fwb-prd-afk-bot-2` | `truenas-nfs` | 1Gi | `org.democratic-csi.nfs` | Deployment `…-afk-bot-2` | second bot's token cache | **medium** — same as above |
| `jdwillmsen-prd` | `datadir-jdwillmsen-minecraft-fwb-prd-minecraft-bedrock-0` | `truenas-iscsi` | 5Gi (ext4) | `org.democratic-csi.iscsi` | STS `…-minecraft-bedrock` | live Bedrock world | **high** — live game state, out of scope for this PoC (ADR-0022, ADR-0029) |
| `jdwillmsen-prd` | `jdwillmsen-minecraft-fwb-prd-restore-scratch` | `truenas-iscsi` | 5Gi (ext4) | `org.democratic-csi.iscsi` | CronJob `…-restore` only (no pod mounts it between runs) | scratch restore target | **none** — throwaway by design |

NFS PVs export from `192.168.1.205:/mnt/storage/k8s/vols/<pv>`; iSCSI PVs are
`iqn.2005-10.org.freenas.ctl:csi-<pv>-cluster` on portal `192.168.1.205:3260`.
All six mount on `talos-lx0-6a4` today, which is the only node whose iSCSI
initiator has been exercised against the NAS.

The read was taken with the API server directly because no TrueNAS API key was
available to the reading session; re-verify the NAS side before cutover with
the driver-aware command, which also shows what each PV maps to on the NAS:

```bash
platformctl cluster volumes truenas list --storage-class truenas-nfs --full
platformctl cluster volumes truenas list --storage-class truenas-iscsi --full
```

(Requires `PLATFORMCTL_TRUENAS_API_KEY` — use a throwaway read-only key, and
`--truenas-ca-file`/`--truenas-insecure-skip-tls-verify` for the appliance's
self-signed certificate.) The tool reads the democratic-csi driver Secrets for
its dataset parents, so objects under `storage/k8s/truenas-csi/*` will not
appear in it until it learns the new parents — that gap is a PoC finding to
record, not a sign the objects are missing.

### Credential path (confirmed)

`kv/truenas-csi` is democratic-csi's: both democratic-csi ExternalSecrets read
`key: truenas-csi`, `property: api_key`, and `platformctl
--truenas-use-csi-api-key` reads the same path by name. The truenas-csi driver
therefore uses the distinct path `kv/truenas-csi-driver` (`property:
api_key`), seeded via the `truenas-csi-driver` seed spec and rendered by
`tenants/platform/services/truenas-csi/postInstall/external-secret.yaml` into
Secret `truenas-csi-driver-api-key` (`api-key`). That is what is live and
`SecretSynced` today; do not introduce a second name for it.

## 3. Scratch validation (no real data)

Prove the driver against throwaway PVCs before any migration. This answers
the two risks ADR-0029 names plus one open packaging question.

1. **Talos hostPath fix**: confirm every `truenas-csi-node` pod is Running —
   no `hostPath type check failed` events (the chart redirects `/etc/iscsi`
   to `/var/etc/truenas-csi/iscsi`; this validates the redirect).
2. **NFS provision/mount**: create a 1Gi PVC on `truenas-csi-nfs-poc`, mount
   it from a scratch pod, write/read data. Confirm the dataset appears at
   `storage/k8s/truenas-csi/nfs/<pvc>` on the NAS. **Open question to settle
   here**: whether the driver creates the intermediate parent datasets
   (`k8s/truenas-csi/nfs`) itself — if provisioning fails on a missing
   parent, create the parents once in the TrueNAS UI and note it in the
   findings.
3. **iSCSI provision/mount**: same with `truenas-csi-iscsi-poc` (ext4 by
   default). Confirm the zvol lands under `storage/k8s/truenas-csi/iscsi`.
4. **Expansion**: grow both scratch PVCs; NFS should resize online, iSCSI may
   need a pod restart to see the new size (documented upstream behavior).
5. **fsck-on-stage probe** (gates any future `truenas-iscsi` migration, i.e.
   the Minecraft PVC — not this PoC's target, but cheapest to answer now).
   Source read since ADR-0029 narrows the question: truenas-csi does **not**
   run its own fsck, but its `NodeStageVolume` for iSCSI (`pkg/driver/iscsi.go`,
   v1.2.0) calls `k8s.io/mount-utils` (v0.34.1) `SafeFormatAndMount.FormatAndMount`,
   whose `formatAndMountSensitive` runs `fsck -a <device>` on every
   already-formatted device mounted read-write — the same Go mount helper the
   democratic-csi config comment notes is *not* on that driver's path. The
   pinned node image has the binaries (`/sbin/fsck` from util-linux 2.39.3,
   `e2fsck`, confirmed by exec into a running `truenas-csi-node` pod). Two
   differences from democratic-csi's `fsck -f -p`, and the reason this probe
   still runs: the check is not forced (`-f`), so it relies on ext4 having
   marked the superblock unclean or errored after the journal abort; and a
   "corrected" exit (1) is logged and the mount proceeds in the same stage
   call, whereas democratic-csi fails that stage and mounts clean on retry.
   Probe: with writes in flight on the ext4 iSCSI scratch volume, force a
   NAS-side session drop (restart the iSCSI service on the NAS), then delete
   the pod so the volume unstages and restages. Confirm the filesystem
   recovers writable and the node log shows the `fsck` line — a wedged
   read-only mount here is a disqualifying finding for iSCSI migrations.
6. **Controller behavior on NAS blips**: note `truenas-csi-controller`
   restart counts across the window (the probe shape that churned
   democratic-csi is toggleable at `controller.livenessProbe.enabled` in the
   chart values).
7. Delete the scratch PVCs. Both classes are `Retain`, so also clean the
   scratch datasets/zvols on the NAS (once truenas-csi objects are confirmed
   visible to `platformctl cluster volumes truenas`, use its `reclaim`;
   otherwise clean them in the TrueNAS UI — the tool reads democratic-csi's
   driver Secrets, so coverage of the new parents is itself a finding).

Any disqualifying finding: stop, record it on the ticket and as an ADR-0029
follow-up, and consider ADR-0024's Longhorn escape route instead.

## 4. PVC selection

**Loki's `storage-platform-loki-0`** (`monitoring`, `truenas-nfs`, 30Gi),
unchanged from ADR-0029's risk ordering and confirmed against the full
inventory above:

- Its data is the only set on either class that costs nothing to lose — log
  history is re-ingested from the next scrape onward. Every `jdwillmsen-prd`
  candidate carries a human cost on loss (interactive bot re-login) or is the
  world's only restore source.
- It is owned by this repo, so the cutover is a single reviewed values change
  here; the tenant PVCs need a change in the external deploymentRepo plus a
  tenant sync, doubling the moving parts for a first migration.
- NFS, so the fsck question does not apply to it.
- Verification is unambiguous: queries spanning the cutover window either
  return both halves or they do not.

Explicitly **not** the Minecraft world PVC (live game state, gated on the fsck
probe and on its own backup standard per ADR-0022). The
`…-restore-scratch` PVC holds nothing and is not a meaningful "real PVC"
migration, but it is a free **dress rehearsal for the iSCSI copy path**
(ext4 block to ext4 block through a helper pod) if the world PVC is ever
scheduled — run it after this PoC, not instead of it.

Cost of the Loki window: no log ingestion while the StatefulSet is at zero
(agents buffer and drop on their own limits). Budget one hour end to end for
30Gi over NFS-to-NFS rsync on the same NAS; the copy itself is bounded by the
NAS, not the cluster.

## 5. Copy-based migration (Loki)

`PersistentVolume.spec.csi.driver` is immutable and truenas-csi has no import
path — data moves by copying, never by relabeling. Sources are `Retain`, so
every step below is reversible until the final reclaim.

1. **Pre-checks**: `platform-truenas-csi` Synced (see live status above);
   Loki healthy; note the bound PV (`pvc-d3e23e89-…` at the time of the
   inventory) and its NAS dataset (`storage/k8s/vols/<pv>`); confirm current
   usage fits 30Gi (`df` inside `platform-loki-0`).
2. **Quiesce**: stop writes. Loki is ArgoCD-managed, so disable auto-sync (or
   pause) on the `platform-loki` Application first, then scale the
   `platform-loki` StatefulSet to 0. Log ingestion drops for the duration —
   acceptable for the PoC, note the window.
3. **Provision the target**: create a 30Gi PVC on `truenas-csi-nfs-poc` in
   `monitoring` (a plain PVC manifest, applied by hand for the PoC window —
   it exists to receive the copy and will be adopted via the values change
   below).
4. **Copy**: run a helper pod mounting both PVCs and
   `rsync -a --delete /old/ /new/`. Verify with a second dry-run
   (`rsync -anc`) reporting zero differences; spot-check file counts and
   sizes.
5. **Cutover**: in `tenants/platform/services/loki/values.yaml` change
   `singleBinary.persistence.storageClass` to `truenas-csi-nfs-poc` (PR, not
   a live edit — this is still GitOps). A StatefulSet's
   `volumeClaimTemplates` are immutable, so the STS must be deleted
   (`--cascade=orphan` is unnecessary at zero replicas) for ArgoCD to
   recreate it against the new class; make sure the recreated
   `storage-platform-loki-0` PVC binds the copied volume (pre-create it with the
   template's exact expected name in step 3, or bind the copied PV to it by
   `claimRef` before re-enabling sync).
6. **Restart and verify**: re-enable sync, scale back up. Verification bar:
   pod mounts successfully; Loki ready; queries return log ranges spanning
   the cutover window (old data readable, new ingestion landing); ArgoCD
   fully Synced/Healthy with no drift on both `platform-loki` and
   `platform-truenas-csi`.

## 6. Rollback

At any point before the old objects are reclaimed:

- Revert the Loki values change (revert PR), delete the new-class PVC/STS the
  same way as the forward cutover, and let ArgoCD recreate against
  `truenas-nfs`. The old PV is `Retain` and its dataset untouched; if the PV
  object was deleted, its dataset still exists on the NAS and a `Released` PV
  can be re-bound by clearing `spec.claimRef`.
- Driver-level rollback: the parallel install means democratic-csi never
  stopped serving its classes; removing the `truenas-csi` service entry (and
  its namespace entry) from `tenants/platform/tenant.yaml` de-installs the
  new driver without touching any democratic-csi object. PVs already created
  on `csi.truenas.io` become unmanageable when the driver is removed — clean
  them first.

## 7. Burn-in and reclaim

Leave the old Loki PV and dataset in place through a burn-in period (suggest
≥2 weeks of clean operation). Then reclaim the orphaned dataset with
`platformctl cluster volumes truenas reclaim` (`--dry-run` first, then
`--confirm`), subject to the tool's own refusal rules for `truenas-nfs`.

## 8. Record the findings

Definition of done for the PoC, beyond the migrated PVC: write the results —
full PVC inventory, fsck probe verdict, parent-dataset behavior, controller
restart behavior, migration timings — as a new ADR referencing ADR-0029
(records are append-only; do not edit ADR-0029). That record informs the
go/no-go on migrating the remaining PVCs and the eventual democratic-csi
decommission, which is what finally lifts ADR-0024's TrueNAS-26 gate.
