# ADR: Minecraft Bedrock on Kubernetes — storage, exposure, and backup mechanism

Status: accepted, not yet implemented. Scoped to the `jdwillmsen` tenant workload.
Records three decisions that outlive the migration itself; the phased delivery plan
lives in the tracker, not here.

## Problem

A Minecraft Bedrock server runs on an unmanaged Proxmox VM (`vmid 1000`, pve1) and is
to become a GitOps-managed workload under a new tenant. The backlog described that
work, but re-deriving its premises against live state on 2026-08-13 found most of them
wrong, and the corrections change the design rather than merely the ticket text.

What the investigation established:

- The server is **Bedrock**, not Java: `itzg/minecraft-bedrock-server`, binary
  `bedrock_server-1.26.36.1`, bound to **19132/UDP**. The backlog assumed Java on
  25565/TCP.
- It is already containerized — three Compose stacks under `/home/minecraft/`, of which
  only `bedrock/fwb` (1.2 G world) runs.
- Its backup sidecar has produced **no archives at all**. Every run logs
  `Docker process didn't start successfully, or has died`; the backups directory holds
  only a `config.yml` last modified 2023-11-19. Container health is `unhealthy` while
  container status reads `Up`, which is why this went unnoticed for months.
- No players have used the server since it moved to this VM, so there is no downtime
  constraint and no client-saved address to preserve.

Three questions follow that the tickets do not answer, and each independently
determines whether the workload is safe to run in-cluster.

## Decision

### 1. World data on TrueNAS **block** storage, not NFS

The world PVC uses a new `truenas-iscsi` class — a ZFS zvol exported over iSCSI and
formatted by the consuming node — rather than the existing `truenas-nfs` file class or
`longhorn`.

**Block rather than file is a correctness requirement, not a preference.** A Bedrock
world is a LevelDB database. LevelDB relies on byte-range file locking — it takes locks
on its own `LOCK` file specifically to stop concurrent access corrupting the store — and
on `mmap`, whose log can corrupt when flushes land on page-aligned boundaries. Neither
behaves dependably over a network filesystem. With iSCSI the node's own kernel owns the
filesystem, so locking, `mmap` and `fsync` behave as they do on a local disk.

The risk is observed rather than inferred. `itzg/docker-minecraft-server` issue 1033
reports a Kubernetes deployment on NFS storage throwing `java.io.IOException: Stale file
handle` during world chunk reads, with the operator's users finding that *"all buildings
had disappeared through the night"*; it recurred within two hours of each restart,
degraded over time, and was closed unresolved. That report is Java Edition, so it is not
a like-for-like reproduction — but `ESTALE` during world I/O is a property of the
filesystem, not the edition, and LevelDB is more exposed than region files rather than
less.

**Keeping the data on TrueNAS is a deliberate acceptance of a dependency the workload
does not have today.** The VM's disk is `local-lvm` on pve1 — node-local, no NAS
involvement — so this migration *introduces* the NAS to the workload's failure path.
Live state makes the asymmetry concrete: of 15 PVCs in the cluster, 14 are Longhorn and
exactly one (`monitoring/storage-platform-loki-0`) is on TrueNAS. Longhorn is the default
class, backed by node-local LVM on the Talos workers (`storage_pool = "local-lvm"`), and
is independent of TrueNAS. The 2026-08-11 unplanned power-off showed the cost: the
NAS-backed Loki PVC threw ingest 500s and the democratic-csi controller crash-looped,
while Longhorn-backed workloads did not appear in the damage set.

That is accepted on the basis that TrueNAS resilience is being addressed directly —
health and capacity alerting into Prometheus, plus remote power control and
unreachability alerts, both separately tracked — and that consolidating stateful data on
the NAS with ZFS snapshots and pool-level integrity is the intended long-term direction.

Three consequences are not optional:

- **Block storage fixes correctness, not availability.** A NAS outage takes an iSCSI
  volume down as surely as an NFS mount, and arguably harder: the filesystem is local
  while its journal sits across the broken link, so the volume can go read-only or
  require `fsck` rather than simply hanging. Longhorn is the only replicated class that
  survives the NAS being unavailable, and remains the default; `local-path` survives it
  too but pins a volume to one node with no redundancy, which is not a serious candidate
  for a world worth keeping.
- **The democratic-csi API migration is a hard predecessor.** The iSCSI driver reaches
  TrueNAS over the same REST API removed in 26.04. After removal, provisioning, expansion
  and deletion fail on this class exactly as on the NFS one. Alerting does not mitigate a
  dated external deadline.
- **TrueNAS alerting and remote power control land before the VM is decommissioned**,
  since they are the resilience this decision assumes. Decommissioning first would remove
  the fallback while the justification is still outstanding.

The node prerequisites already exist: Talos carries the `siderolabs/iscsi-tools` system
extension in its Image Factory schematic, and Longhorn — which attaches its own volumes
over iSCSI — reports `RequiredPackages` and `Multipathd` healthy on every node. No
schematic migration is required.

### 2. Exposure via NodePort UDP in the default range

The Service is `type: NodePort`, `protocol: UDP`, node port in the default
30000–32767 range (31132), targeting container port 19132. Clients connect to
`<node-ip>:31132` on the LAN.

The alternatives are each closed off by something already true of the cluster:

- **Extending the existing ingress path is impossible.** HAProxy terminates every
  frontend in `mode tcp` (`infrastructure/bootstrap/internal/haproxy/config.go`) and
  HAProxy does not proxy UDP. The generated config carries exactly four frontends —
  6443, 50000, 80, 443 — and Bedrock fits none of them.
- **`hostPort` is forbidden.** Tenant namespaces enforce Pod Security `baseline`
  (`helm-charts/tenant-envelope/templates/namespaces.yaml`), which disallows host ports.
- **A LoadBalancer has nothing to satisfy it.** No MetalLB or equivalent is deployed;
  the only non-ClusterIP Service in the cluster is the nginx gateway's NodePort.

Keeping the original 19132 would require widening `--service-node-port-range` on the
API server, a control-plane change affecting every tenant. Since access is LAN-only and
no player has a saved server entry, the port change costs nothing and the control-plane
change is not justified.

The pod is scheduled by affinity onto `talos-lx0-6a4` or `talos-4h8-zy6`, the only nodes
with room: 61.3 GiB and 14.1 GiB allocatable respectively, against roughly 12 GiB and
3 GiB in use when this was written. Every other node sits far tighter — see
`infrastructure/docs/ram-expansion-decision.md` for the fleet's memory picture, which is
the durable record; utilisation snapshots here would only go stale.

### 3. No third-party backup sidecar; a self-contained CronJob instead

Backups are taken by a CronJob in this repo that quiesces the server, copies `worlds/`,
resumes it, and writes a timestamped archive to TrueNAS. No third-party backup image is
used.

This is forced rather than preferred. There is no maintained first-party option:

- `itzg/mc-backup`, from the same maintainer as the server image, **does not support
  Bedrock**. It coordinates through RCON, and Bedrock has no RCON.
- `kaiede/minecraft-bedrock-backup` — the image currently failing on the VM — is
  single-maintainer, and its upstream repository has been merged into Bedrockifier and
  archived. The local image was last built 2025-03-29 against a server image rebuilt
  2025-12-18, and upstream carries an open issue describing exactly the observed
  symptom.
- Decisively, Bedrockifier requires mounting `/var/run/docker.sock`. **Talos runs
  containerd and exposes no Docker socket**, so the approach cannot work on this cluster
  at all, irrespective of its maintenance status.

**Quiescing has an exact protocol, and getting it wrong corrupts the world silently.**
A live Bedrock backup is `save hold`, then `save query` until it reports ready, then
copying the files it names — and **truncating each copy to the exact byte length
`save query` reports** — then `save resume`. The truncation step is not optional:
LevelDB continues appending to files while the server runs, so a copy taken without it
contains a partial trailing record and triggers LevelDB repair on restore. CubeCoders'
AMP shipped a live-backup implementation missing this and corrupted worlds with it.

Because that protocol is easy to implement subtly wrong, a cold stop, copy and start is
the safer default for a server with no players waiting on uptime, and is what was
validated by hand on 2026-08-13, when the world was archived off the VM to close the
backup gap described above. Live quiescing is an optimisation to
adopt only once the truncation behaviour is tested against a restore.

Three properties are acceptance criteria, not refinements, because they are precisely
what failed on the VM:

- **The job fails loudly.** A failed backup must reach Alertmanager like any other
  cluster alert. The existing failure was invisible because nothing watched it.
- **Emptiness is a failure.** The VM's backup ran on schedule for months, exited without
  crashing, and produced no archives. "The job ran" is not the signal; "the job produced
  a plausibly-sized artefact" is.
- **A restore is tested** into a scratch PVC and the result booted. A backup that has
  never been restored is an assumption. The chart's restore mode recovers data without
  an active server process and is the intended vehicle.

### 4. The backup degrades rather than aborts when the server is unreachable

Added after the mechanism above failed in exactly the situation it was built for. On
2026-08-17 the Bedrock pod crashlooped when its ext4 volume latched read-only after an
iSCSI session drop, and the scheduled backup failed with
`unable to upgrade connection: container not found`. The world was unprotected during
the one incident that threatened it, and survived only because the filesystem failed
read-only rather than corrupt.

Two dependencies on a healthy server were at fault, and the scheduling one was the
worse of the two because it was silent:

- A **required** `podAffinity` on the server pod. It reads as "run beside the server"
  but means "run only if a server pod exists" — with the StatefulSet scaled to zero or
  its pod Terminating, nothing matched and the job never left `Pending`. It could not
  even start in order to report why.
- `save hold` driven over `kubectl exec` with no alternative path.

**Decision: when the server cannot be reached, take the copy anyway and record that it
was degraded. Do not abandon the run.**

The justification is that quiescing is a consistency optimisation and not a correctness
requirement. Bedrock's LevelDB replays its write-ahead log on open — the same recovery
path an unclean shutdown takes — so a copy taken without a hold is restorable, merely
slightly further behind. Weighing a marginally staler archive against no archive at all
is not a close call, and the failure being designed against is specifically the case
where the server is already in trouble.

Failing loudly and snapshotting anyway are not alternatives here, which is the part the
original framing got wrong. The run produces the artifact **and** publishes
`backup_last_artifact_quiesced=0`, which raises `BackupArtifactNotQuiesced`. The
operator learns the server was unreachable at backup time and still has a restorable
world. Silence in either direction is the only outcome ruled out.

A server deliberately scaled to zero is a separate case and publishes `1`, not `0`.
Nothing is writing to a stopped world, so that copy is as consistent as a held one —
the gauge reports whether the source was at rest, not whether a hold command was
issued. The distinction is drawn from the StatefulSet's desired replica count, so a
pod that is present but crashlooping still counts as meant-to-be-running and still
alerts. Without it a planned shutdown would file a ticket every four hours for its
whole duration.

### 5. CSI volume snapshots evaluated and rejected for now

A snapshot taken through the CSI driver is the one design that depends on nothing
inside the application: `truenas-iscsi` is a ZFS zvol, and a ZFS snapshot is atomic,
instant, and consistent regardless of what the server is doing. On the merits it is a
better primitive than any copy loop.

It is rejected for now on availability, not on design:

- **The cluster has no snapshot machinery at all.** There is no `VolumeSnapshotClass`,
  no `snapshot-controller`, and the `VolumeSnapshot` CRDs are not in `bootstrap/crds/`.
  This is a platform-level addition, not a chart change.
- **The path it would run over is broken.** `democratic-csi` drives `freenas-api-iscsi`,
  so `CreateSnapshot` goes through the TrueNAS API — the same API whose stored
  credential currently carries literal quotes and returns 401. That break is already
  why a PVC on this class sits `Terminating`. **The snapshot path therefore cannot be
  tested today**, and nothing here should be read as a claim that it works.
- **A snapshot is not an archive.** It lives in the same pool as the volume it was taken
  from, so it protects against deletion and corruption but not against losing the pool.
  It complements the off-volume tarball rather than replacing it.

The sequencing that follows: fix the TrueNAS API credential, then add the
snapshot-controller and a `VolumeSnapshotClass`, then take a snapshot immediately before
the copy so the copy has an atomic point-in-time source and the `save hold` protocol
becomes unnecessary rather than merely optional. Until the first of those lands, the
degrade-and-record behaviour above is what protects the world.

## Consequences

- The workload gains a dependency on TrueNAS that it does not have today, and the work
  gains the democratic-csi API migration as a blocking predecessor it did not previously
  acknowledge.
- A second democratic-csi release and a new block StorageClass become platform surface
  to operate and upgrade, and Minecraft is the first workload on that path. The class is
  worth having independently — ZFS snapshots and pool-level integrity apply to any future
  database workload — but its first exercise carries a live world.
- Sizing follows the workload rather than the old VM. Published guidance puts a small
  Bedrock server at 1–2 GiB, and the VM's 4 GiB covered a whole guest OS as well. The
  namespace's `standard` quota caps memory requests at 4 GiB in total, so a server
  requesting all of it would leave the backup CronJob unable to schedule.
- Bedrock backup logic becomes code this repo owns and must maintain, rather than an
  upstream image. Given that both upstream options are unusable, this is the cost of
  having backups at all.
- The server's address changes. Acceptable only because it currently has no users; the
  same decision would be wrong for a live server, where widening the NodePort range
  would be the better trade.
- Decommissioning the VM reclaims 4 GiB of RAM and 4 cores on pve1, which has roughly
  5 GiB free of 28.2 GiB. `infrastructure/docs/dev-vm-provisioning.md` cites
  `minecraft-server (4GB)` as part of that pressure and needs updating when this lands.

## Notes

The image must be pinned to a digest or explicit version. The VM runs `VERSION=LATEST`,
which silently upgrades the server binary underneath the world — tolerable for a
hand-run VM, incompatible with a GitOps workload whose state is the whole point.
