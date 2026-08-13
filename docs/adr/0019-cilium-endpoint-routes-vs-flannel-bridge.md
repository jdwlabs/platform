# ADR: Disable Cilium endpointRoutes — this Flannel runs bridge mode, not point-to-point

Status: accepted. Extends
[chained-cilium-rollout-corrections](0017-chained-cilium-rollout-corrections.md).
Written during and immediately after a live outage; the finding is corrected
in place rather than deferred to a further record, because the record and the
incident are the same event.

## Problem

Landing the chained-Cilium install (0017's step 1) took cluster DNS down for
roughly forty minutes. Two earlier bugs in the same change were found and
fixed live during the incident — a missing `chaining-mode` key in the CNI
plugin block, and an invented namespace the AppProject rejected — and each
looked, at the time, like it explained the remaining symptoms. Neither did.
After both were fixed, CoreDNS's pods still could not become ready.

## What was actually happening

CoreDNS's `health` plugin self-checks by querying itself with a random-name
HINFO record. Nothing in its Corefile answers HINFO, so the query falls
through to the terminal `forward` plugin and leaves the pod. Every forward
target tried — the Talos node's own loopback resolver, the LAN gateway, and
both `1.1.1.1` and `8.8.8.8` — timed out identically. That ruled out the
target: no address was going to work, because the query was never getting a
response delivered back into the pod at all.

The decisive test was a control this cluster already had, without meaning to:
`local-path-provisioner`, a pod running continuously since before Cilium was
ever installed, had no `CiliumEndpoint` — Cilium's endpoint-restore path
reattaches BPF programs to interfaces that already exist; it does not
run pods newly created after the install-through the same CNI ADD path a
new pod does. Real DNS queries from inside it, including a hand-built raw
HINFO packet, succeeded instantly to every target that had just timed out for
CoreDNS. Repeated from inside CoreDNS's own network namespace via an
ephemeral debug container, the identical query failed the identical way.
The pod's own outbound path was never the problem. Delivery *into* the pod —
of a DNS response, or of a kubelet health-check probe, which failed the same
way earlier in the incident with `no route to host` on one node and a bare
timeout on another — was.

`ip route show` on an affected node explained why. Cilium's chart defaults
`endpointRoutes.enabled` to `true`, and this repo's values file carried that
default forward without examining it. Enabled, it installs a host-scope `/32`
route straight to each new pod's veth:

```
10.244.7.0/24 dev cni0 proto kernel scope link src 10.244.7.1
10.244.7.160 dev veth90f4509b proto kernel scope link
```

The `/32` wins longest-prefix-match over the bridge's `/24`, so the host
stops delivering to that pod via `cni0` — the same mechanism every other pod
on the node, including `local-path-provisioner`, uses successfully — and
instead routes directly to the specific veth. `local-path-provisioner`'s
veth is a bridge port with no competing route, so bridge delivery works for
it exactly as it always has. Every pod Cilium created after the install got
the extra route and lost bridge delivery, with nothing to replace it.

Upstream's generic-veth chaining guide, which the sizing record and this
record's own predecessor both drew from, recommends `endpointRoutes.enabled:
true` as part of the standard chained configuration. That guidance is
correct for a point-to-point veth CNI, where there is no bridge and the host
route Cilium installs is the *only* way traffic is delivered to the pod. It
is not correct here. This cluster's Flannel runs in bridge mode —
`10-flannel.conflist`'s `delegate` block and the live `cni0` device with every
pod's veth attached as a `bridge_slave` confirm it — and a bridge-mode CNI
already has a working, bridge-native way to deliver to every pod. Cilium's
extra route does not cooperate with that; it silently replaces it, per pod,
the moment each pod is created.

## Why this was not caught before merge

The chart was rendered and inspected before landing 0017's change. The
render showed exactly what was asked of it: `cni-chaining-mode`,
`custom-cni-conf`, `policy-audit-mode`, and every other daemon-level key
matched intent, because `endpointRoutes.enabled` was never in question — it
was accepted from upstream's own recommended configuration without being
weighed against this cluster's specific Flannel backend. The rendered
manifest was correct against the values; the values were wrong against the
cluster.

Nothing in ADR 0013's step 3 verification would have caught it either. That
step's decisive check — scheduling before health, `cilium status` reporting
`CNI Chaining: generic-veth` — passed. It reports the daemon's own
configuration, which was correct throughout. The bridge conflict lives one
layer below anything either the chart render or `cilium status` inspects:
the host kernel's routing table, populated per-endpoint at pod-creation time,
diverging silently from what every prior pod on the same node already had.

## Decision

**`endpointRoutes.enabled: false`.** Every pod is delivered to the same way —
through `cni0`, the bridge Flannel already provides — regardless of whether
Cilium created it before or after this change. This is not a narrower or
degraded version of the generic-veth chaining setup; it is the correct
configuration for a bridge-mode Flannel backend specifically, which is what
this cluster runs.

**Applied live before this record merged.** `cilium-config`'s
`enable-endpoint-routes` key was patched to `false` and the DaemonSet rolled
during the incident, restoring pod-creation and DNS resolution before this
fix reached `main` — GitOps did not have a working repo→cluster path while
cluster DNS itself was down, so the live patch was the only path back to a
working cluster, and it predates the PR carrying this record by minutes.
Merging this closes the gap between the two: without it, the next ArgoCD
`selfHeal` reverts the live patch back to `true` and reintroduces the outage.

**A second live patch, to CoreDNS's own Corefile, is deliberately not
included here.** `coredns`'s ConfigMap carries
`config.k8s.io/owning-inventory: talos-bootstrap-manifests-inventory` — it is
owned by Talos's bootstrap manifests, not by this repo or by any ArgoCD
Application. There is no PR in this repository that represents it. The
forward target was changed live, from the node's own unreachable loopback
resolver to public resolvers, restoring the health self-check once endpoint
delivery itself was fixed. That change is real and currently load-bearing,
and it needs a home — most likely a record in `infrastructure`, which is
where Talos's bootstrap manifests are actually managed — but it is out of
this repository's scope to carry.

## Consequences

**The generic-veth guidance this cluster follows needs a caveat, not a
retraction.** Chained Cilium over bridge-mode Flannel is still the right
choice — ADR 0012's reasoning for chaining over replacing Flannel is
untouched by any of this — but `endpointRoutes` is exactly the kind of
setting that upstream examples carry as a default because it is usually
right, and is wrong here because the assumption underneath it — no bridge in
the way — does not hold. Anyone touching this values file who reintroduces
`endpointRoutes: true` reintroduces tonight's outage, on the next pod any
namespace creates.

**The failure mode is silent and per-pod, not cluster-wide at flip time.**
Enabling this setting breaks nothing at the moment it is applied — the
DaemonSet rolls, agents report healthy, `cilium status` is clean. It breaks
the *next* pod that gets created, and every one after, one at a time, each
looking like an unrelated readiness failure until enough of them accumulate
to be recognizable as a pattern. `local-path-provisioner` illustrates the
other edge of the same silence: a pod that already existed before the
setting changes is never affected by it at all, which is exactly what made
it the control that solved this.

**This finding came from a control the cluster had by accident.** Nothing
about this rollout deliberately preserved a pre-Cilium pod to compare
against; `local-path-provisioner` simply hadn't been rescheduled recently.
The rollout runbook this cluster follows for its next CNI-adjacent change
should not rely on that being true again.
