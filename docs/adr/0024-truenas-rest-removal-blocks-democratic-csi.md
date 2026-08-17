# ADR: TrueNAS REST removal — democratic-csi has no replacement transport, so the NAS is gated at 25.10.x

Status: accepted, no change deployed. Records a blocked migration and the
upgrade gate that stands in for it until upstream ships a WebSocket client.

## Problem

TrueNAS deprecated its REST API in 25.04 and removes it in 26. The NAS
(192.168.1.205, currently 25.10.4) raises one standing alert: the deprecated
REST API authenticated ~4,800 times in 24 hours from 192.168.1.87, the node
running the democratic-csi controllers. Both TrueNAS-backed storage classes
reach the NAS over that API.

The obvious reading is "swap the transport, then upgrade the NAS". That
reading is wrong, and this record exists because the reason is not visible
from the config file: **no released or unreleased build of democratic-csi
can speak the replacement API.** The blocker is the driver, not this repo's
configuration and not the chart revision.

## What is deployed, verified live 2026-08-17

Both driver configs are rendered by External Secrets from `kv/truenas-csi`
and read identically on the transport:

| Release | Driver | Transport | Endpoint |
|---|---|---|---|
| `democratic-csi` (`org.democratic-csi.nfs`) | `freenas-api-nfs` | `protocol: http` | `192.168.1.205:80` |
| `democratic-csi-iscsi` (`org.democratic-csi.iscsi`) | `freenas-api-iscsi` | `protocol: http` | `192.168.1.205:80` |

Both releases run chart `democratic-csi` rev `0.15.1`. Three PVCs depend on
this path today — two on `truenas-nfs`, one on `truenas-iscsi` — plus every
future provision, expansion, snapshot and delete on either class.

## democratic-csi cannot speak JSON-RPC over WebSocket, at any version

This was checked against source rather than release notes, because the
release notes do not discuss it.

**No WebSocket client exists on any ref.** Searching the driver source for
`websocket`, `jsonrpc` or `json-rpc`, case-insensitively, returns nothing on
the newest tag `v1.9.5` (2026-01-07), nothing on `next` (which is the same
commit as `v1.9.5`), nothing on `master`, and nothing on `dev` (last touched
in 2021). `ws` and `reconnecting-websocket` appear in `package.json` but no
source file requires either — they are dependencies with no caller.

**The FreeNAS HTTP client is REST-only by construction.** Its base URL is
assembled as `<scheme>://<host>:<port>/api/v2.0`, where the scheme can only
become `http` or `https`; there is no `ws`/`wss` branch and no JSON-RPC
envelope anywhere in the request path. The `apiVersion` option selects
between `/api/v1.0` and `/api/v2.0` — both REST.

**Every TrueNAS driver name resolves to one of two classes.** The driver
factory maps `freenas-api-*` and `truenas-api-*` onto the REST client, and
`freenas-*` / `truenas-*` onto the SSH driver. There is no third option and
no undocumented `-ws` variant.

**The SSH drivers are not an escape hatch.** They are frequently suggested
as the workaround, and they do not work as one: the SSH driver issues on the
order of thirty REST calls of its own for share, iSCSI target, extent and
target-to-extent management. Upstream states the same thing directly — all
ZFS operations move to SSH, but share management still goes through the API.
Switching to SSH would cut the REST call *rate* while leaving the cluster
just as broken at 26.

**Upstream has announced the work but not started it.** The maintainer
committed to WebSocket support on 2026-04-20 and said the code would land on
the `next` tag first, on 2026-05-12. As of 2026-08-17 no such code exists on
any branch, and no open pull request in the repository mentions WebSocket.
The same announcement carries a second warning that matters more than the
first, and it is dealt with under *The opposite hazard* below: the version
that adds WebSocket support is intended to **remove the HTTP API code
entirely and require TrueNAS 26.x**.

### A chart bump is not the fix

Chart `0.15.1` is the newest published revision — there is nothing to bump
to. This is incidental, because the chart is not the blocker in any case:
the chart only chooses which driver image runs and mounts the config secret.
The missing capability is in the driver image, which is versioned
independently of the chart.

## The deadline binds at upgrade time, not on a calendar date

Nothing is broken today and nothing breaks on a date. 25.10.4 still serves
REST and only warns; the alert text is the deprecation notice, not a
failure. TrueNAS 26 has not reached stable — it stands at 26-BETA.2
(2026-06-17) — while the 25.10 line is in maintenance and has moved on to
25.10.6 (2026-08-12).

So the sequencing is forced, and it is the reverse of the ticket's framing:

1. Upstream ships a WebSocket-capable driver.
2. This repo cuts both releases over and proves provisioning on both classes.
3. **Only then** the NAS moves to 26.x.

Step 3 before step 2 is the outage. At that moment both CSI control planes
lose the NAS: `CreateVolume`, `DeleteVolume`, `ControllerExpandVolume` and
snapshot operations all fail. Volumes that are already bound and mounted
keep serving — NFS and iSCSI data paths do not touch the API — so this is a
provisioning outage rather than a data outage, but it is unbounded in time
because the only fix is downgrading the NAS.

## The opposite hazard: the driver image is an unpinned rolling tag

The gate above protects against moving the NAS too early. The reverse
failure is currently unprotected, and it needs no action by anyone here to
fire.

Chart `0.15.1` defaults `controller.driver.image.tag` and
`node.driver.image.tag` to `latest`, and neither release's values file
overrides them. Live pods in all three democratic-csi Deployments run
`ghcr.io/democratic-csi/democratic-csi:latest`, resolving today to
`sha256:944d0e65077efbd9c1fdf23997eec8fac4b4bfb7c3de400f63e33a0a849c5ced`.
That digest matches no release tag between `v1.7.7` and `v1.9.5`, so `latest`
is a rolling build rather than an alias for the newest release.

Combine that with upstream's stated plan — the next version removes the HTTP
API code and requires TrueNAS 26.x — and the failure mode is: upstream
publishes, `latest` moves, the next CSI pod restart pulls a driver that
refuses to talk to 25.10, and provisioning on both classes breaks with no
commit in this repository. Pod restarts are not hypothetical here; the NFS
controller accumulated 189 restarts in a 19-day lifetime from liveness-probe
churn alone, before that probe was disabled.

`tools/check-image-pins.py` cannot catch this. It scans `tenants/` and
`helm-charts/`, and this tag lives in the defaults of a chart pulled from a
remote repository, so there is no allowlist entry recording it either.

The remedy is to set both `image.tag` keys in both releases to
`latest@sha256:944d0e65…` — the digest already running, so the pin changes
what a pod *resolves* to at zero change to what it *executes*. That is
deliberately **not** bundled into this record's change: it re-creates the
controller pods and both node DaemonSets, and a rollout of the storage data
plane should be its own reviewed change rather than a side effect of a
documentation commit. It is the highest-priority follow-up arising here.

## Options considered

**Change `protocol: http` to a WebSocket scheme.** Rejected — not a
configuration option. The driver would fail to parse it, and if it parsed
it, no code path speaks the protocol. This is the speculative change that
would take out provisioning on both classes.

**Switch both releases to the SSH drivers.** Rejected. Still REST for share
management, so it does not unblock the 26 upgrade; it also introduces SSH
credentials and root-equivalent shell access to the NAS as a new secret to
manage, in exchange for a partial reduction in call volume.

**Move the workloads off TrueNAS-backed classes.** Not taken now. Longhorn
already carries 14 of the 17 PVCs in the cluster, so the escape route
exists, but three volumes do not justify abandoning a storage tier while the
upstream fix is announced and the NAS is under no time pressure.

**Pin the NAS at 25.10.x, change nothing, and watch upstream.** Chosen.

## Decision

Leave both driver configs on the REST transport. Do not change
`httpConnection` in either ExternalSecret template, do not switch driver
names, and do not bump the chart.

**Gate: TrueNAS 192.168.1.205 does not go past 25.10.x** while either
democratic-csi release is deployed with a `freenas-api-*` driver. The gate
is recorded in `docs/OPERATIONS.md` alongside the other TrueNAS operational
facts, because that is what a person reads before touching the NAS, and
restated here as the decision it is.

**The standing REST-deprecation alert stays.** It is the only live signal
that the cluster still depends on the removed API, and it self-clears once
the calls stop. Dismissing it removes the evidence and changes nothing else.

Revisit when **any one** of the following is true, each checkable rather
than arguable:

**R1 — a democratic-csi build speaks the new API.** Searching the driver
source for `websocket` or `jsonrpc` returns a hit under `src/driver/freenas/`
on a published tag or on `next`. This is the trigger that actually unblocks
the migration; the rest are consequences of it or of it not arriving.

**R2 — that build's driver config shape is known.** The API key in
`kv/truenas-csi` is expected to carry over unchanged, since TrueNAS API keys
authenticate both transports, but the config key it lives under is not
knowable until the code exists. Whether the migration is an ExternalSecret
template edit or a re-seed is answered at R1, not before.

**R3 — TrueNAS 26 reaches Stable** on the software-status page. This does
not unblock anything; it converts the gate from precautionary to load
bearing, and is the point at which the "move off this storage tier" option
deserves a fresh look if R1 has still not fired.

**R4 — the `latest` digest moves off `sha256:944d0e65…`.** Fires the image
pin as urgent rather than planned, because the next published version is the
one that drops REST support.

## Consequences

**The cluster is byte-identical after this record.** No values file, no
ExternalSecret template, no chart revision and no ArgoCD Application
changes. The only edits are this document, the operational gate, and two
comments in manifests that do not reach the cluster.

**The NAS stays on a maintained release, not a stale one.** 25.10 is in
maintenance and still receiving patches, so the gate costs security updates
nothing today. That stops being true when 25.10 leaves maintenance, which is
why R3 exists.

**A ticket deliverable is knowingly unmet.** The migration itself cannot be
performed, and the alert cannot be dismissed. Both are blocked on R1, which
is outside this repository.

**One hazard is documented but left live.** The unpinned `latest` tag can
break provisioning on both classes without a commit here. It is written down
with the exact remedy and the exact digest so that closing it is mechanical.

## Non-goals

This record does not decide when TrueNAS 26 gets adopted — only that it
cannot be adopted before R1. It does not evaluate migrating the three
affected volumes to Longhorn, which is a separate question that only becomes
pressing if R3 fires without R1. It does not address the other unpinned
images in this chart's sidecar set, which carry ordinary version tags rather
than `latest` and are not subject to the failure mode described here.
