# ADR: Cilium enforcement is real but partial — coverage is the qualifier

Status: accepted. Extends
[chained-cilium-rollout-corrections](0017-chained-cilium-rollout-corrections.md)
and blocks its decision-1 step 8 until coverage is resolved. The engine choice
(0012), the sizing (0013) and 0017's five-step sequence all stand; what this
record adds is the property none of them measured — how many pods the engine
can actually resolve a policy against.

## Problem

Every signal the rollout was verified against reports healthy. All 8
`cilium-agent` pods are Ready and reconciled, `policy-audit-mode: false` is in
`cilium-config`, 140 NetworkPolicy objects are applied across 25 namespaces,
and no node has logged a `Policy denied` flow. From those, "enforcement is
live" is a reasonable conclusion, and it is the conclusion the repo recorded —
in `tenants/platform/services/cilium/values.yaml` and, implicitly, in 0017's
step 7.

It is true of the engine and only partly true of the workloads. Cilium manages
a pod whose sandbox it created, and nothing else. A pod already running when
the agent on its node started never received a `cilium-cni` ADD, so it has no
`CiliumEndpoint`, no security identity, and every policy selecting it resolves
against nothing. The pod is not merely unprotected — it is invisible to the
mechanism that would protect it, while the namespace containing it reports its
policies applied.

Nothing in the rollout's verification distinguishes those two states, and
nothing alerts on the difference.

## Finding 1 — the premise moved, in the useful direction

JDWLABS-355 was raised on measurements from 2026-08-17: 18 of 125 pod-network
Running pods managed, 14%. Re-measuring on 2026-08-19 before acting on it:

```
coverage: 84/136 pods managed (61%) / 52 unmanaged, cluster-wide
```

Coverage is 61%, not 14%. The mechanism the ticket describes is exactly right;
its numbers are two days stale, and in those two days ordinary churn —
ArgoCD syncs, image bumps, evictions — enrolled 66 more pods. The per-node
spread the ticket used as its proof still holds and is still the clearest
evidence of the mechanism:

| Node | Managed / total | Coverage |
|---|---|---|
| `talos-lx0-6a4` | 27 / 54 | 50% |
| `talos-4h8-zy6` | 23 / 40 | 57% |
| `talos-2qd-v0u` | 9 / 13 | 69% |
| `talos-g1i-e3h` | 9 / 13 | 69% |
| `talos-k3y-y3e` | 12 / 12 | 100% |
| `talos-6iz-oey`, `talos-fow-vbk`, `talos-oam-s4g` | 4 / 4 | 100% |

The ticket's other specific claim does not survive: it sampled a `jdwlabs-prd`
pod as `reserved:unmanaged`, and `jdwlabs-prd` is now 6/6. Both tenant
namespaces that would be isolated first are fully enrolled today.

This is the argument against remediating on a schedule. Churn is closing the
gap on its own, and every workload it closes costs nothing and risks nothing —
whereas restarting 52 pods to reach the same place spends real availability.

## Finding 2 — cilium-agent metrics cannot express this

The obvious deliverable is a Prometheus alert. It cannot be written from
Cilium's own metrics, for a reason that is structural rather than a gap in the
exporter: the agent exports counts of endpoints it manages, and a pod it never
learned about has no endpoint to count. The quantity wanted is the difference
between an API-server pod count and a Cilium endpoint count, which no single
exporter holds.

It is further out of reach here than that. This cluster scrapes no `cilium_*`
series at all — `tenants/platform/services/cilium/values.yaml` sets no
`prometheus` or `serviceMonitor` block, and the chart defaults them off. There
are no Cilium dashboards under `observability/` and no Cilium rules under
`kube-prometheus-stack/postInstall/`. An alert expression referencing
`cilium_endpoint_state` or similar would be a plausible-looking PromQL string
matching nothing, which is worse than no alert: it would appear to be the
regression gate this ticket asked for while being permanently silent.

## Finding 3 — the gap is measurable from outside, and cheaply

The join the exporter cannot do is trivial from the API server: list
`CiliumEndpoint`s, list Running pod-network pods, subtract. Two exclusions keep
the denominator honest — host-network pods can never carry an endpoint, and a
pod that is not Running has no sandbox, so counting either reports a gap no
remediation closes.

That is `platformctl cluster netpol coverage`, added by this change, plus the
same join registered as the `cilium-endpoint-coverage` check in
`platformctl cluster status` so a regression surfaces in the routine health
report rather than only when someone remembers to look. The command exits
non-zero below `--min-coverage` (default 100), which is what makes it a gate
rather than a report.

## Decision

**1. Ship the measurement, the correction and the runbook. Do not perform the
restart.** A controlled per-namespace restart is the honest remediation, and it
is an operational action with an availability cost — not something a merge
should trigger. This change makes the gap visible, states it correctly
everywhere it was previously overstated, and sequences the restart with abort
criteria in [OPERATIONS.md §9](../OPERATIONS.md). Executing that sequence is a
separate, human-initiated action.

**2. Prefer natural churn and node drains over a restart campaign.** Coverage
moved 14% → 61% in two days with no intervention, and a rolling node drain —
which a Talos upgrade already performs — is the only mechanism that reaches
Longhorn's `instance-manager` pods, which must never be restarted directly.
Restart a namespace when it is about to be isolated, not to move a number.

**3. Enrollment gates enforcement, per namespace.** A namespace may only be
opted in with `enforce: true` once `platformctl cluster netpol coverage -n <ns>`
reports 100%. Enforcing an unenrolled namespace is the worst of both states: it
protects nothing today, and it starts enforcing an unverified allow-set at the
first pod restart, with no event marking the transition.

**4. 0017 decision-1 step 8 is blocked**, not cancelled. Its ordering —
`jdwlabs-non`, then `dotablaze-tech-non`, then the `prd` namespaces, then the
`ci` runner namespaces — is unchanged and still correct. What is added is
condition 3 above as a per-namespace precondition. `jdwlabs-non` and
`jdwlabs-prd` already satisfy it; `dotablaze-tech-non` and
`dotablaze-tech-prd` are at 0/1 each and do not.

**5. No Prometheus alert is claimed.** If one is wanted later, the prerequisite
is scraping Cilium at all — a `prometheus`/`serviceMonitor` block in the values
file, with the resource and cardinality cost that implies — and the expression
still has to join against kube-state-metrics. That is a separate change with a
separate decision to make.

## Consequences

**0017 is extended, not edited.** Records here are append-only, and 0017 itself
pays that cost explicitly: "Anyone reading it for the rollout gets a sequence
that cannot be executed, and only reaches this correction by following the
reference forward." Leaving step 8 unmarked repeats exactly that failure on a
step whose execution is now unsafe — so 0017 gains an appended correction
section carrying the block and the forward reference, and not one existing line
of it changes. Its step 8 still reads as it did when it landed. That is the
same shape ADR 0022 was extended in, and it removes the trap without relaxing
the rule to do it: a reader who stops at step 8 has not yet reached the
correction, which is the residual cost append-only always carries.

**Coverage becomes a number someone has to look at.** `cluster status` reports
it as a warning, not a failure, because partial coverage is the expected state
during a rollout and a check that is always red is a check nobody reads. The
failure signal lives in `netpol coverage`'s exit code, where it can be pointed
at one namespace and one threshold.

**The `cilium-endpoint-coverage` check reads every pod in the cluster.** That
is two list calls on each `cluster status`, unfiltered. At 136 pods it is not
worth optimising; at ten times that it would want a field selector or a
resourceVersion cache, and the check would need revisiting rather than
silently getting slower.

**Nothing here closes the second gap.** As TENANT-MODEL.md already records, no
pod is subject to a restrictive policy today, because the only namespaces with
`enforce: true` have no pods. Full endpoint coverage would still leave that
true. Coverage is a precondition for enforcement mattering, not a substitute
for turning it on.

**This record was written as 0027 and renumbered.** A concurrently open branch
had already taken that number for an unrelated decision. ADR 0017's
consequences claim the collision race "now fails a required check instead of
landing"; it does not. `tools/check-adr-numbering.py` scans one working tree,
so two branches that each take the next free number both pass, and the
collision only becomes visible when the second one merges — or, as here, when
a human reads both open PRs. The check closes the duplicate-on-`main` case,
not the concurrent-allocation case that produces it.
