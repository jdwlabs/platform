# ADR: Bounding containers that chart values cannot reach

Status: accepted, implemented.

## Problem

The QoS sweep across the platform stack capped every container reachable
through Helm values. What remained were containers whose chart template
renders no `resources:` field on any code path, so a values entry for them is
silently discarded — the failure mode that had already cost this work once,
when `longhornDriver`/`longhornUI` resources blocks sat in
`tenants/platform/services/longhorn/values.yaml` looking effective while the
live pods ran BestEffort.

A live scan found 14 such containers, not the 9 previously recorded. The gap
was init containers, which the earlier scans skipped. That omission matters:
pod QoS class is computed across init containers too, so a single unbounded
init container drops the whole pod to BestEffort no matter how carefully the
containers beside it were sized. BestEffort pods are the first thing the
kubelet evicts under node memory pressure — the mechanism that took out ESO
and cert-manager during the hugepages incident.

## What the scan actually found

Three distinct classes, not one:

| Class | Containers | Reachable? |
|---|---|---|
| Values reach it; nobody had set it | `loki-sc-rules`, grafana `init-chown-data`, holmes `talosctl-install`, litellm `db-ready` | yes |
| Template renders no `resources` field | longhorn ×21 (instance-manager, engine-image, ui, driver-deployer, pre-pull), nginx-gateway data-plane `init` | no |
| Not Helm at all | `kube-flannel`, `kube-proxy`, `install-config` | Talos machine config |

The first row is the correction worth recording. `loki-sc-rules` was
previously documented as a chart gap requiring an upstream PR to
grafana/loki. It is not: loki 6.55.0 renders `sidecar.resources` onto that
container, confirmed by rendering the chart with a sentinel value and finding
it in `single-binary/statefulset.yaml`. The claim had been made by reading
the template rather than rendering it.

This cuts both ways, and it is the general lesson: `helm show values`
advertises a `resources` key for all four charts involved here. Loki exposes
it and uses it; nginx-gateway-fabric exposes `nginx.container.resources` and
applies it to the data-plane container while rendering nothing for the init
container beside it. The values file cannot distinguish those two cases.
**Only a rendered manifest or a live pod spec can.**

## Options considered

**Upstream PRs.** Correct for genuine chart gaps, unbounded in timeline, and
after the loki correction there was exactly one upstream gap left worth
filing against. Does nothing for the cluster in the meantime.

**Kyverno mutating policy.** Per-container precision, and would cover chart
gaps not yet discovered. Rejected on cost: it adds an admission controller
(~500Mi across three pods) to a cluster whose control-plane nodes run at
90–110% memory, in service of an epic whose purpose is reducing memory
footprint. It also puts a webhook in the pod-creation path, where a
misconfigured `failurePolicy` can block scheduling cluster-wide.

**LimitRange with `default` (a limit).** Achieves a literal zero-unbounded
count. Rejected because it puts a hard memory ceiling on Longhorn's
`instance-manager`, whose footprint scales with attached volumes and rebuild
activity (151–601Mi across nodes at rest). An OOM kill there breaks volume
I/O and triggers replica rebuilds. Worse, the ceiling would take effect
whenever `longhorn-manager` next recreates the pod — plausibly mid-rebuild,
the moment it is least survivable.

**LimitRange with `defaultRequest` only.** Chosen. It separates the two
hazards that "unbounded" conflates: a missing *request* makes a container
BestEffort and evicted first; a missing *limit* lets it starve neighbours.
Setting only `defaultRequest` fixes the first without imposing the second.

Verified empirically before adoption rather than assumed from the API docs: a
`defaultRequest`-only LimitRange in a scratch namespace, with a pod submitted
via server-side dry-run (which runs the LimitRanger admission plugin),
injected `requests.memory` into both the init and main containers, set no
limits, and moved the pod from BestEffort to Burstable.

LimitRange is also already the mechanism in use here — `tenant-limits` ships
in all six tenant namespaces via the `tenant-envelope` chart — so this
introduces no new concept, only a second tier of it for platform namespaces.

## Decision

1. Where values reach the container, set them. Sized from live usage, request
   at roughly 1.5× observed and limit at 2–3×, consistent with the rest of
   this work.
2. Where the template renders nothing, add a `defaultRequest`-only LimitRange
   to the namespace. Applied to `longhorn-system` and `nginx-gateway`.
3. Where the workload is not Helm-managed, accept the risk and record it
   below rather than reaching into Talos machine config.

## Accepted risk register

**`kube-flannel`, `kube-proxy`, `install-config`** (kube-system, Talos-managed).
Left unbounded deliberately, not merely out of reach. These are
`system-node-critical` daemons on which every other workload's networking
depends; a memory limit converts a slow leak into a hard kill of cluster
networking, which is strictly worse than the growth it would prevent.
Upstream Kubernetes ships kube-proxy without limits for the same reason.
`kube-flannel` already carries a 50Mi request (observed 65–69Mi, so the
request is low but present) and is therefore Burstable, not BestEffort.
Changing these requires a machine-config patch in the infrastructure repo and
carries the VXLAN-fault history as precedent for how badly flannel changes
can go.

**Longhorn `instance-manager` memory ceiling.** Now has a request but
deliberately no limit, per the reasoning above. Revisit if a node is ever
observed under memory pressure attributable to instance-manager growth
rather than to volume count.

## Consequences

The epic's hygiene metric reads "zero BestEffort or memory-unbounded
workloads". After this change the BestEffort half is fully met. The
memory-unbounded half is met except for the register above, which the
metric's own "explicitly recorded accepted-risk list" clause allows for.

A LimitRange defaults containers only at admission, so the effect on
longhorn-system and nginx-gateway arrives as pods are recreated, not at merge
time. Existing pods keep their current (absent) requests until they next
restart. This is deliberate — forcing a roll of the storage data plane to
apply a scheduling hint would be a poor trade.

Two follow-ups belong upstream rather than here: nginx-gateway-fabric's
provisioner rendering no resources for the data-plane init container, and the
vendored litellm chart's `image.dbReadyImage`/`image.dbReadyTag` defaults
sitting under a key its own template does not read (`db.dbReadyImage`), which
would render a broken image reference for anyone relying on chart defaults.
