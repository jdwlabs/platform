# Drain Feasibility — three of eight nodes could not be drained, and one still cannot

Measured live 2026-08-18 with `platformctl cluster drain-check`, the command this
work adds. Read-only: five list calls and the metrics API, no cluster changes.

[06-node-commitment-floor.md](06-node-commitment-floor.md) closed the small-worker
hotspot as a capacity finding and noted, in passing, that two nodes could not be
drained. That aside is the more serious result, and this doc is its follow-up.
The headline: **a drain was infeasible on three of eight nodes, and after the
change in this branch it remains infeasible on one.**

## Why this is worth a command rather than a spreadsheet

Node drain is the mechanism behind Talos upgrades, Kubernetes upgrades, hardware
maintenance and incident response. It completes only if every pod it evicts can
be re-placed. Nothing in Kubernetes reports that in advance — a node looks
healthy right up to the moment it has to be emptied, and the discovery happens
mid-upgrade with the node already cordoned.

The arithmetic is also not something a human re-derives reliably. It is a bin
packing under six predicates against a cluster that moves: every number in doc
06 had drifted within a day, and one of its two named nodes was no longer the
problem while a third node had become one. A one-off measurement of this is a
snapshot with a short shelf life. `platformctl cluster drain-check` exits
non-zero when any node is blocked, so the answer stays current on its own.

## Measured state before the change

| Node | Role | Allocatable | Requested | Free | Pinned | Movable | Verdict |
|---|---|---|---|---|---|---|---|
| talos-lx0-6a4 | worker (large) | 61.3Gi | 15.0Gi | 46.3Gi | 1.7Gi | 13.3Gi | **blocked** |
| talos-4h8-zy6 | worker (large) | 14.1Gi | 5.1Gi | 9.0Gi | 1.7Gi | 3.4Gi | **blocked** |
| talos-g1i-e3h | worker (small) | 2.3Gi | 2.3Gi | 24Mi | 1.7Gi | 624Mi | **blocked** |
| talos-2qd-v0u | worker (small) | 2.3Gi | 2.2Gi | 146Mi | 1.7Gi | 502Mi | drainable |
| talos-k3y-y3e | worker (small) | 2.3Gi | 2.0Gi | 344Mi | 1.7Gi | 304Mi | drainable |
| talos-oam-s4g | control-plane | 4.7Gi | 1.9Gi | 2.8Gi | 1.1Gi | 768Mi | drainable |
| talos-6iz-oey | control-plane | 4.7Gi | 1.2Gi | 3.4Gi | 1.1Gi | 128Mi | drainable |
| talos-fow-vbk | control-plane | 4.7Gi | 1.2Gi | 3.4Gi | 1.1Gi | 128Mi | drainable |

Two corrections to doc 06's picture, one day later:

- **`talos-4h8-zy6` is blocked too.** Doc 06 named `talos-g1i-e3h` and
  `talos-lx0-6a4`. A CNPG primary and a Vault member have since landed on
  `4h8-zy6`, and both are excluded by their own anti-affinity from every node
  that could hold them.
- **`talos-g1i-e3h` now has one blocker, not two.** `postgresql-cluster-non-1`
  moved off it. The remaining blocker is `postgresql-cluster-prd-11`.

Free memory per worker (24Mi / 146Mi / 344Mi on the small three) is otherwise
unchanged from doc 06, so the commitment floor it established still holds.

## Why each node was blocked

`talos-g1i-e3h` and `talos-4h8-zy6` were blocked by the same mechanism.
Three HA workloads run three replicas under `required` hostname anti-affinity:

| Workload | Request/replica | Placement |
|---|---|---|
| `database/platform-postgresql-cluster-prd` | 192Mi | lx0-6a4, 4h8-zy6, g1i-e3h |
| `database/platform-postgresql-cluster-non` | 192Mi | lx0-6a4, 4h8-zy6, 2qd-v0u |
| `vault/platform-vault` | 192Mi | lx0-6a4, 4h8-zy6, 2qd-v0u |

Evicting any one replica leaves only nodes that hold no sibling, which are
exactly the small workers — and none of them has 192Mi free. The three replicas
also compete with each other for the same scarce room, which is why relieving
one workload relieves the others (see below).

`talos-lx0-6a4` is blocked for an unrelated reason: **capacity**. It carries
13.3Gi of movable load across 48 pods. Every other node put together offers
about 10Gi, and the control-plane nodes are excluded by their `NoSchedule`
taint. No placement rule changes that.

## The anti-affinity decision

`required` guarantees a node loss cannot take two replicas of one database. That
is worth something, so `preferred` is not an obvious win. What settles it is
what `required` actually delivers *during a drain*.

Take `platform-postgresql-cluster-prd` and drain `g1i-e3h`:

- Under `required`, the replica is not relocated — it is stranded `Pending`. The
  cluster runs at 2 of 3, on `lx0-6a4` and `4h8-zy6`. Losing either leaves one.
- Under `preferred`, it lands on a node that already holds a sibling. The
  cluster runs at 3 of 3. Losing the doubled node leaves one; losing the other
  leaves two.

`preferred` is therefore no worse in the case `required` is sold on, and better
in the other. A hard rule cannot spread a replica onto a node that does not
exist; what it does instead is stop the replica running at all. And the drain
never completes, so the node cannot be patched either.

Outside a drain the two are equivalent here. CNPG weights its soft rule at 100,
which outranks the scheduler's default resource scoring, so placement is
unchanged whenever spreading is achievable — which it is today, and is how the
current spread arose.

**The real cost of `preferred` is that a co-location is never undone.** No
descheduler runs here, so a replica that doubles up during a drain stays doubled
up until something evicts it. That is a genuine regression in steady-state
redundancy, and it is why `drain-check` reports co-located replicas against an
anti-affinity rule: the risk is acceptable because it is now observable, and the
remedy (delete the pod, let the operator place it again) is routine.

### Vault keeps `required`

Relaxing only the two CNPG clusters restores exactly the same set of nodes as
relaxing all three. Removing two of the three competitors for small-worker room
leaves enough for Vault's replica to relocate under its hard rule.

Vault keeps the rule because the consequence of co-location differs. Two CNPG
instances on one node means a node loss leaves one instance, which the operator
promotes. Two raft members on one node means a node loss leaves one of three:
quorum lost, Vault sealed, and every secret downstream of it stale — Vault is a
wave-2 dependency for ESO, cert-manager and the rest.

The margin this relies on is thin: one small worker with about 152Mi to spare
after Vault's replica lands. `drain-check` fails the moment it stops holding,
which is the intended signal to revisit the decision rather than discover it
during an upgrade.

## Projection after the change

Same simulator, live state, with `podAntiAffinityType: preferred` on both CNPG
clusters and everything else untouched:

| Node | Before | After |
|---|---|---|
| talos-g1i-e3h | blocked | **drainable** |
| talos-4h8-zy6 | blocked | **drainable** |
| talos-2qd-v0u | drainable | drainable |
| talos-k3y-y3e | drainable | drainable |
| talos-lx0-6a4 | blocked | **blocked** |

**Not every worker can be drained after this change.** `talos-lx0-6a4` still
cannot. Adding 8Gi to `talos-4h8-zy6` takes its blocker count from 35 to 5, at
which point the binding constraint becomes CPU rather than memory. Draining the
64Gi worker needs capacity in the worker pool, not configuration — the same
conclusion doc 06 reached about the small workers, reached again from the other
end of the cluster.

## Longhorn `instance-manager` — a correctness bug that cannot be corrected here

The scheduler is placing `instance-manager` on a fiction. Measured live:

| Node | Declared (2-3 pods) | Observed | Busiest pod |
|---|---|---|---|
| talos-lx0-6a4 | 128Mi | 622Mi | 305Mi vs 64Mi |
| talos-4h8-zy6 | 128Mi | 477Mi | 265Mi vs 64Mi |
| talos-2qd-v0u | 128Mi | 288Mi | 230Mi vs 64Mi |
| talos-k3y-y3e | 128Mi | 288Mi | 248Mi vs 64Mi |
| talos-g1i-e3h | 128Mi | 179Mi | 160Mi vs 64Mi |

The busiest instance-manager on each node runs 2.5-4.8x its declared request.
Worse, **two of them declare nothing at all**:
`instance-manager-2be1c9dd…` on `lx0-6a4` and `instance-manager-6e592745…` on
`4h8-zy6` request 0Mi while using 297Mi and 169Mi. They predate the namespace
`LimitRange` and were never recreated, so they are BestEffort — first in line
for kubelet eviction, for the storage data plane every other workload depends
on. `platformctl cluster status` already flags them via `limitrange-adoption`;
recreating those two pods is a Longhorn operation, not a manifest change, and it
is the one part of this finding that is free to fix.

The declared request cannot be corrected in this repo:

- Longhorn chart 1.12.1 exposes no per-component knob.
  `guaranteedInstanceManagerCPU` is CPU-only;
  `systemManagedCSIComponentsResourceLimits` does not list instance-manager. The
  namespace `LimitRange` default is the only lever.
- That lever is namespace-wide. It also governs both `engine-image` DaemonSets
  and the `longhorn-manager` pre-pull sidecar, which measure 1-3Mi and genuinely
  fit in 64Mi — five containers per worker charged for a correction two of them
  need.
- The small workers have 24Mi, 146Mi and 344Mi free. Even a +64Mi bump costs
  320Mi per worker and leaves those DaemonSets unable to schedule on their next
  recreate. Simulated: declaring instance-manager at 256Mi takes all three small
  workers past their allocatable and blocks four of five workers instead of one.

So an honest declaration exceeds the node. That is the same capacity finding as
doc 06, arrived at from a third direction: the 2.31Gi workers are already
over-committed in reality, and the 64Mi fiction is what lets them appear to fit.
The rationale is recorded at the lever itself, in
`tenants/platform/services/longhorn/postInstall/limit-range.yaml`.

One trap found alongside it: `longhorn-system` carries a **second** LimitRange
from the tenant-envelope chart, also at 64Mi. LimitRanger merges defaults
first-writer-wins over an unordered list, so raising one and not the other would
make the effective default nondeterministic. Change both or neither.

## What the check does and does not model

`drain-check` evaluates readiness and cordon state, taints and tolerations,
`nodeSelector` and required node affinity, required pod affinity and
anti-affinity, PersistentVolume node affinity, and memory/CPU/pod-count
capacity. DaemonSet and static pods are skipped, as `kubectl drain` skips them.

A `drainable` verdict is a witness — a concrete assignment that would work — so
it is sound. A `blocked` verdict of class `hard` is a proof, because no packing
order conjures a node the pod's own constraints exclude. A `blocked` verdict of
class `capacity` is order-dependent and so a strong signal rather than a proof.

Deliberately not modelled: preferred affinity and topology spread constraints,
which never make a placement impossible. Hard topology spread constraints are
not modelled either — there are none in this repo today, and any workload
carrying one is named in the report's `unmodelled` list rather than silently
assumed satisfied.

Two limits worth stating plainly. The check answers "could this node be drained
now", not "could every node be drained in sequence" — draining one node changes
where everything sits, and the next verdict is computed against the state before
that. And it does not model the eviction API's pacing: a node can be feasible
and still hang on a `PodDisruptionBudget` that currently allows no disruption.
Those are counted per node as `pdbAtZero`, and every Longhorn `instance-manager`
carries one at zero while its volumes are attached.

## Re-measurement

```
platformctl cluster drain-check                 # verdicts, exits non-zero if any node is blocked
platformctl cluster drain-check --full          # per-node commitment figures
platformctl cluster drain-check --pods --usage  # inventory with declared vs observed memory
platformctl cluster drain-check --node <n> --plan
```

The number to watch is the blocked count. It should be exactly one
(`talos-lx0-6a4`) until the worker pool gains capacity, and any second entry is
a regression worth chasing before the next upgrade rather than during it.
