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

## What the GitOps change delivers (state after merge)

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
  `kv/truenas-csi-driver` — **which does not exist until a human seeds it**
  (step 1). Until then the ExternalSecret reports `SecretSyncedError` and the
  driver pods wait on the missing Secret. That is the expected pre-seed
  state, not a rollout failure.

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

## 2. Complete the live PVC inventory (ticket deliverable, read-only)

ADR-0029's research pass accounted for only 2 of the 3 PVCs ADR-0024
recorded on these classes. Establish the full set before selecting anything:

```bash
platformctl cluster volumes truenas list --storage-class truenas-nfs --full
platformctl cluster volumes truenas list --storage-class truenas-iscsi --full
```

(Requires `PLATFORMCTL_TRUENAS_API_KEY` — use a throwaway read-only key, and
`--truenas-ca-file`/`--truenas-insecure-skip-tls-verify` for the appliance's
self-signed certificate.)

Known so far: Loki's `singleBinary` PVC (`truenas-nfs`, 30Gi,
`tenants/platform/services/loki/values.yaml`) and the Minecraft Bedrock world
PVC (`truenas-iscsi`, via the `jdwillmsen` tenant's external deploymentRepo).
The third — a second `truenas-nfs` consumer — must be identified here and its
owning repo located. Record the complete inventory in the PoC findings.

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
   the Minecraft PVC — not this PoC's target, but cheapest to answer now):
   with writes in flight on the ext4 iSCSI scratch volume, force a NAS-side
   session drop (restart the iSCSI service on the NAS), then delete the pod
   so the volume unstages and restages. democratic-csi force-fscks before
   every ext4 stage to recover the aborted journal; truenas-csi documents no
   equivalent. Confirm the filesystem recovers writable — a wedged read-only
   mount here is a disqualifying finding for iSCSI migrations.
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

Per ADR-0029's risk ordering: **Loki's `singleBinary` PVC** — disposable log
history, lowest consequence, NFS (the transport with no fsck question).
Explicitly **not** the Minecraft world PVC (live game state, gated on the
fsck probe and on its own backup standard per ADR-0022), and not the
yet-to-be-identified third PVC until the inventory names it.

## 5. Copy-based migration (Loki)

`PersistentVolume.spec.csi.driver` is immutable and truenas-csi has no import
path — data moves by copying, never by relabeling. Sources are `Retain`, so
every step below is reversible until the final reclaim.

1. **Pre-checks**: Loki healthy; note the bound PV name and its NAS dataset
   (`storage/k8s/vols/<pvc-...>`); confirm current usage fits 30Gi.
2. **Quiesce**: stop writes. Loki is ArgoCD-managed, so disable auto-sync (or
   pause) on the `platform-loki` Application first, then scale the
   `loki` StatefulSet to 0. Log ingestion drops for the duration —
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
   `storage-loki-0` PVC binds the copied volume (pre-create it with the
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
