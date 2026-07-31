# ADR: LimitRange coverage for the platform namespaces

Status: accepted, implemented. Extends
[0005](0005-bounding-containers-charts-cannot-reach.md) and
[0008](0008-limitrange-convergence-and-remaining-besteffort.md); supersedes
neither.

## Problem

Eight of the cluster's twenty-seven namespaces carried a LimitRange. The six
tenant workload namespaces got one from the `tenant-envelope` chart;
`longhorn-system` and `nginx-gateway` got one from a hand-written manifest
under the owning service's `postInstall/`. Every platform namespace besides
those two had none.

The gap was structural rather than an oversight. `tenant.yaml` marks all
eighteen platform namespaces `quotaTier: platform`, and neither `_quotaTiers`
nor `_limitRangeTiers` defines a `platform` key — so both templates' `hasKey`
guard skips them. That is correct for `ResourceQuota`, which should not cap
the layer everything else depends on, and it was silently inherited by
`LimitRange`, which is a different instrument with a different failure mode.

## What the live scan actually found

0008 closes by naming the scope any future check should use: every pod whose
`status.qosClass` is BestEffort, rather than a scope defined by mechanism.
Applying that scope to the nineteen uncovered namespaces, and then tightening
it to the container level, produced a result worth recording because it
contradicts the assumption the work started from.

At the pod level, every uncovered namespace is already fully Burstable. The
only BestEffort pods anywhere are the eight `kube-proxy` pods in `kube-system`
— the accepted risk 0005 registered, unchanged.

Pod-level QoS is a weak instrument, though: a pod is Burstable if *any* one
container carries a request, so a Burstable pod can still contain containers
that reserve nothing and therefore contribute nothing to what the scheduler
holds for it. Scanning every container and init container individually for a
missing `resources.requests.memory` across all nineteen namespaces returns:

| Namespace | Containers with no memory request |
|---|---|
| `kube-system` | `kube-proxy`, flannel's `install-config` init |
| all eighteen others | none |

So the premise that `database`, `vault` and `monitoring` were exposed does not
hold. Every container in those namespaces — and in the other fifteen — already
carries an explicit memory request today.

This changes what the work is, and the distinction is worth stating plainly
rather than letting the change read as a fix. **Adding these LimitRanges
corrects nothing that is currently broken.** It installs a floor for the case
0005 was written about: a chart whose template renders no `resources:` field
on any code path, whose containers therefore admit with nothing, and which is
discovered only after the fact by a live scan. That is not hypothetical here —
it is exactly how `longhorn-system` and `nginx-gateway` came to need one, and
in both cases the gap appeared through a chart upgrade rather than a
deliberate choice. The floor is cheap; noticing the next occurrence in time is
not.

## Decision

**1. A `platform` limit-range tier, `defaultRequest`-only and memory-only.**
The tier follows 0005's reasoning without restating it: a missing request
makes a container BestEffort and evicted first, a missing limit lets it starve
neighbours, and `defaultRequest` alone addresses the first without imposing
the second. `default` and `max` are both omitted — `max` in particular rejects
pods outright rather than capping them, so it converts a chart upgrade that
raises a request into a namespace that cannot schedule.

Only memory is defaulted. CPU is compressible; its absence throttles rather
than evicts, so a CPU default would reserve capacity on every node against a
failure mode that does not exist here.

The value is 64Mi, matching `longhorn-defaults`, `nginx-gateway-defaults` and
the `small`/`standard` workload tiers. There is no per-namespace sizing to do,
because the tier never applies to a container that sets its own request — it
is a floor for containers that set nothing, not a correction for sized ones.

Applied to seventeen namespaces: `ai-sre`, `arc-systems`, `argocd`,
`cert-manager`, `cnpg-system`, `database`, `democratic-csi`,
`external-secrets`, `headlamp`, `kubelet-serving-cert-approver`,
`longhorn-system`, `metrics-server`, `monitoring`, `nginx-gateway`,
`porkbun-webhook`, `vault`, `vault-config-operator`.

**2. `kube-system` is excluded, and this is the one call left open.** The
namespace is declared in `tenant.yaml`, so the tier would have covered it; it
is opted out explicitly instead of by accident.

The reason is ownership, not risk of the usual kind. The two unbounded
containers there come from Talos machine config in the infrastructure repo,
and defaulting their requests would change the admitted spec of components
this repo does not own. The change would also be partial: the control-plane
static pods are mirror pods and bypass admission entirely, so a LimitRange
reaches the DaemonSets beside them and nothing else.

What makes this a judgement rather than a conclusion is that 0005's stated
objection does not transfer. That register entry rejects a memory *limit*,
because "a memory limit converts a slow leak into a hard kill of cluster
networking". A request carries no such risk — it reserves, it does not kill —
and `kube-proxy` is the last genuinely BestEffort workload in the cluster. The
narrow case for including `kube-system` is therefore real and unrebutted by
the earlier ADR. It is left out here because it belongs with the machine
config that produces those pods, where the change can be made in one place
instead of two, and not because a request would be unsafe.

**3. `default`, `kube-node-lease` and `kube-public` are out of scope.** These
are Kubernetes' own bootstrap namespaces and appear in no `tenant.yaml`.
Covering them would mean declaring them tenant namespaces, which brings Pod
Security labels, NetworkPolicies and ArgoCD ownership — including pruning — to
namespaces this repo did not create. That is a large change to make in service
of a default that would never fire: `kube-node-lease` holds `Lease` objects,
`kube-public` holds a single ConfigMap, and `default` is empty and unused
here. All three run no pods, by design rather than by circumstance.

## Consequences

`longhorn-system` and `nginx-gateway` now carry two LimitRanges: the
service-local one and `tenant-limits`. Both set the same 64Mi memory
`defaultRequest`, and the admission plugin applies each in turn, so the second
finds the request already set and the effective default is unchanged. The
service-local manifests are kept rather than folded into the tier because they
document namespace-specific reasoning the tier cannot carry — why
`instance-manager` must not take a ceiling, why the gateway's init container
is deliberately uncapped. Consolidating them would trade that reasoning for
tidiness, and would replace two working objects across two independently
syncing Applications for no functional gain.

As 0005 records, a LimitRange defaults containers only at admission. Nothing
changes for a running pod. Since no container in the seventeen namespaces
currently lacks a memory request, the immediate effect on the cluster is
nil — the change is visible only the next time a chart stops rendering
`resources:` for something.

The rendering guard in `limit-ranges.yaml` is now per-block, so a tier may
define a request floor without a ceiling. Rendering both workload tenants
before and after the change produces byte-identical output, so the workload
tiers are unaffected.
