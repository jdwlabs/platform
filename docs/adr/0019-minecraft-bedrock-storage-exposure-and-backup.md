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

### 1. World data on `truenas-nfs`, gated on the democratic-csi API migration

The world PVC uses the `truenas-nfs` democratic-csi class rather than `longhorn`.

This is a deliberate acceptance of a dependency the workload does not have today. The
VM's disk is `local-lvm` on pve1 — node-local, with no NAS involvement — so the
migration *introduces* the NAS to this workload's failure path. Live state makes the
asymmetry concrete: of 15 PVCs in the cluster, 14 are Longhorn and exactly one
(`monitoring/storage-platform-loki-0`) is on `truenas-nfs`. Longhorn is the default
class and is backed by node-local LVM on the Talos workers (`storage_pool = "local-lvm"`),
so it is independent of TrueNAS. The 2026-08-11 unplanned power-off showed what the
dependency costs: the `truenas-nfs`-backed Loki PVC threw ingest 500s and the
democratic-csi controller crash-looped, while Longhorn-backed workloads did not appear
in the damage set.

The decision is accepted anyway, on the basis that TrueNAS resilience is being addressed
directly — health and capacity alerting into Prometheus, plus remote power control and
unreachability alerts, both separately tracked — and that consolidating stateful data on
the NAS is the intended long-term direction.

Two consequences are not optional:

- **The democratic-csi API migration becomes a hard predecessor.** The driver reaches
  TrueNAS over the REST API, which is removed in TrueNAS 26.04. After removal,
  provisioning, expansion and deletion on this class fail. Alerting does not mitigate a
  dated external deadline.
- **TrueNAS alerting and remote power control land before the VM is decommissioned**,
  since they are the resilience this decision assumes. Decommissioning first would remove
  the fallback while the justification is still outstanding.

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
with room: they sit at 18% and 21% memory use against 64 GiB and 14.7 GiB allocatable,
while the remaining six run 71–91%.

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

Two properties are acceptance criteria, not refinements, because they are precisely what
failed on the VM:

- **The job fails loudly.** A failed backup must reach Alertmanager like any other
  cluster alert. The existing failure was invisible because nothing watched it.
- **A restore is tested** into a scratch PVC and the result booted. A backup that has
  never been restored is an assumption. The chart's restore mode recovers data without
  an active server process and is the intended vehicle.

## Consequences

- The workload gains a dependency on TrueNAS that it does not have today, and the work
  gains the democratic-csi API migration as a blocking predecessor it did not previously
  acknowledge.
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
