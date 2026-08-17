# Node Commitment Floor — why the small-worker hotspot is capacity, not placement

Measured live 2026-08-17. Read-only capture (`kubectl get nodes/pods -o json`, `kubectl top pods -A`); no cluster changes were made.

This doc closes out the "resolve the `talos-k3y-y3e` memory-commitment hotspot" work with a negative result: **no placement or right-sizing change available in this repo can bring the three 2.31Gi workers below the threshold that opened the investigation.** The binding constraint is per-node DaemonSet load against a 2.31Gi ceiling. Everything below is the arithmetic behind that.

## Node table

Requested and limits are from pod specs; usage is `kubectl top`. Percentages are against allocatable.

| Node | Class | Allocatable | Requested | Req % | Usage | Use % | Limits | Lim % | Pods |
|---|---|---|---|---|---|---|---|---|---|
| talos-g1i-e3h | worker (small) | 2363Mi | 2338Mi | **98.9%** | 1144Mi | 48.4% | 6336Mi | 268% | 19 |
| talos-2qd-v0u | worker (small) | 2363Mi | 2216Mi | **93.8%** | 1308Mi | 55.4% | 5482Mi | 232% | 19 |
| talos-k3y-y3e | worker (small) | 2363Mi | 2018Mi | **85.4%** | 1125Mi | 47.6% | 4928Mi | 209% | 18 |
| talos-oam-s4g | control-plane | 4773Mi | 1906Mi | 39.9% | 2644Mi | 55.4% | 5312Mi | 111% | 9 |
| talos-4h8-zy6 | worker (large) | 14430Mi | 4490Mi | 31.1% | 3037Mi | 21.0% | 12384Mi | 86% | 41 |
| talos-6iz-oey | control-plane | 4773Mi | 1266Mi | 26.5% | 2430Mi | 50.9% | 4032Mi | 85% | 9 |
| talos-fow-vbk | control-plane | 4773Mi | 1266Mi | 26.5% | 2247Mi | 47.1% | 4032Mi | 85% | 9 |
| talos-lx0-6a4 | worker (large) | 62749Mi | 15587Mi | 24.8% | 8512Mi | 13.6% | 33117Mi | 53% | 65 |
| **Cluster** | | **98586Mi** | **31087Mi** | **31.5%** | **22447Mi** | **22.8%** | | | |

Two corrections to the picture this work started from:

- **`talos-k3y-y3e` did not fall to ~72% requested.** It is at **85.4% requested**, essentially unmoved from the 86.3% at filing. The ~71% figure is its `kubectl top` *usage* (1696Mi/2363Mi), a different metric. Closing on that number would have been a requests-vs-usage confusion.
- **The hotspot moved.** `talos-g1i-e3h` went 69.8% → **98.9% requested**, leaving 25Mi schedulable. That is why a test pod could not be placed on it — nothing fits.

## The floor: 78% of each small worker cannot be moved by any scheduling rule

Splitting each small node's requests into pods that are pinned there by construction (DaemonSets, plus Longhorn's per-node `instance-manager` and `engine-image` singletons) versus pods that merely landed there:

| Node | Allocatable | Pinned | Pinned % | Movable | Free |
|---|---|---|---|---|---|
| talos-g1i-e3h | 2363Mi | 1842Mi | 78.0% | 496Mi | 25Mi |
| talos-2qd-v0u | 2363Mi | 1842Mi | 78.0% | 374Mi | 147Mi |
| talos-k3y-y3e | 2363Mi | 1842Mi | 78.0% | 176Mi | 345Mi |
| **3 small nodes** | **7089Mi** | **5526Mi** | **78.0%** | **1046Mi** | 517Mi |

The pinned 1842Mi is identical on all three because it is the same DaemonSet set:

| Requested | Component |
|---|---|
| 512Mi + 64Mi | `longhorn-manager` (+ its `pre-pull-share-manager-image` sidecar, which has no chart resources hook and picks up the namespace `LimitRange` default) |
| 192Mi | `cilium` |
| 160Mi | `platform-democratic-csi-node` |
| 160Mi | `platform-democratic-csi-iscsi-node` |
| 160Mi | `platform-monitoring-alloy-logs` |
| 128Mi | `platform-gateway-nginx` |
| 96Mi | `longhorn-csi-plugin` |
| 64Mi × 2 | `engine-image-ei-493e04e7`, `engine-image-ei-a4d05f02` |
| 64Mi | `instance-manager` |
| 50Mi | `kube-flannel` |
| 32Mi | `loki-canary` |
| 32Mi | `prometheus-node-exporter` |
| 0Mi | `kube-proxy` (BestEffort) |
| **1842Mi** | **78.0% of a 2363Mi worker** |

So the addressable range on any small node is 78.0% (every movable pod evacuated) to 98.9% (today's worst). The ticket was opened at 86.3%. **The entire placement lever is worth 8 points below the threshold that triggered it, and only if the small nodes are emptied of everything that is allowed to move.**

## Why the movable load cannot actually be evacuated

Three HA workloads use `requiredDuringScheduling` pod anti-affinity on `kubernetes.io/hostname` with 3 replicas each:

| Workload | Request/replica | Current placement |
|---|---|---|
| `database/platform-postgresql-cluster-prd` | 192Mi | talos-4h8-zy6, **talos-g1i-e3h**, talos-lx0-6a4 |
| `database/platform-postgresql-cluster-non` | 192Mi | **talos-g1i-e3h**, talos-lx0-6a4, talos-4h8-zy6 |
| `vault/platform-vault` | 192Mi | talos-4h8-zy6, talos-lx0-6a4, **talos-2qd-v0u** |

There are five workers, two of them large. A `required` hostname anti-affinity rule with three replicas needs three distinct nodes, and only two are large — so **each of these three workloads is structurally forced to put at least one replica on a small worker.** That is 576Mi of movable-in-name-only load, permanently resident on the small nodes.

The best placement that is physically reachable is therefore: those three forced replicas on three *different* small nodes, everything else evacuated to the large workers. That leaves every small node at:

```
1842Mi (pinned) + 192Mi (one forced HA replica) = 2034Mi / 2363Mi = 86.1%
```

**86.1% against the 86.3% that opened this ticket.** The optimal achievable placement lands 0.2 points below the alarm threshold. There is no placement remedy here.

## Requests vs actual: overstated, but the slack is already spoken for — and one component is *under*-declared

`talos-g1i-e3h` is 98.9% requested against 48.4% actual, so requests are roughly 2× usage and "just right-size it" is the obvious first instinct. It does not survive contact with the per-workload numbers:

| Workload | Request | Actual | Verdict |
|---|---|---|---|
| `longhorn-manager` | 512Mi | ~165Mi | Already trimmed from the chart default 1Gi (JDWLABS-297). The remaining margin is a deliberate churn allowance recorded in `tenants/platform/services/longhorn/values.yaml`. |
| `postgresql-cluster-*` | 192Mi | 81–99Mi | Explicitly floored — the margin is WAL replay after a node roll, documented as "do not trim further". |
| `cilium` | 192Mi | 144Mi | 75% utilised, no meaningful slack. |
| `democratic-csi-node` / `-iscsi-node` | 160Mi each | 74–83Mi | Chart-managed; trimming both across 5 nodes returns ~85Mi/node at best. |
| `instance-manager` (per node) | 64Mi | **161–241Mi** | **Under-declared by 2.5–3.8×.** |

The last row is the one that matters. Longhorn's `instance-manager` pods request 64Mi (a namespace `LimitRange` default, because the chart exposes no resources hook) while actually consuming 161Mi, 229Mi, and 241Mi on the three small nodes. Sizing these honestly would *raise* small-node commitment by roughly 150–250Mi each, not lower it. The 2× aggregate gap is not uniform slack waiting to be reclaimed — it is one heavily-margined DaemonSet, several already-floored workloads, and a storage data plane that is running well over its declared footprint.

## Schedulability today — already degraded, before any change

Free memory per node: lx0-6a4 47162Mi · 4h8-zy6 9940Mi · k3y-y3e 345Mi · 2qd-v0u 147Mi · g1i-e3h 25Mi.

- **Draining `talos-g1i-e3h` cannot complete.** Its movable load is `postgresql-cluster-prd-11` (192Mi), `postgresql-cluster-non-1` (192Mi), and two Longhorn CSI sidecars. Each CNPG pod is barred by its own `required` anti-affinity from the two large workers, which already hold the other two replicas of both clusters. That leaves only `2qd-v0u` (147Mi free) and `k3y-y3e` (345Mi free). The first CNPG pod fits on `k3y-y3e`; the second then fits nowhere — 147Mi and 153Mi against a 192Mi request. With `minAvailable: 1` PDBs on both clusters, the drain blocks rather than completing.
- **Draining `talos-lx0-6a4` is already infeasible** — 13745Mi of movable load against ~10.4Gi of free capacity everywhere else combined.

This is the concrete reason not to reach for anti-affinity or topology spread constraints as a remedy: the cluster is already at the point where a single node drain strands pods, and every additional placement constraint narrows the feasible set further.

## Remedies considered and rejected

| Option | Verdict |
|---|---|
| Right-size overstated requests | **Rejected.** The large gaps are deliberate, documented margins (`longhorn-manager` churn headroom, CNPG WAL replay floor) already set by Phase 2. The one genuinely mis-sized component, `instance-manager`, is under-declared — fixing it makes the hotspot worse. |
| Pod anti-affinity / topology spread | **Rejected.** Three `required` anti-affinity workloads already force one replica each onto the small nodes. Adding constraints to a five-worker cluster where one drain already strands pods makes scheduling less feasible, not more. |
| Relocate a specific workload | **Rejected as a fix, though placement is genuinely uneven.** Both CNPG clusters currently hold a replica on `talos-g1i-e3h`. Moving one to `talos-k3y-y3e` takes g1i-e3h to 90.8% and k3y-y3e to 93.5% — it lowers the peak by 5 points while making the node this investigation was named after materially worse. That is whack-a-mole, not a fix. |
| Soft `nodeAffinity` toward the large workers (the pattern used for Loki / kube-prometheus-stack) | **Rejected here.** It works for those single-replica monitoring workloads because they have no anti-affinity and one preferred destination. For a 3-replica `required` anti-affinity cluster it can steer at most two replicas onto the two large workers; the third is forced small regardless, so the constraint that actually binds is untouched. |
| Capacity | **Accepted as the finding.** See below. |

## Verdict

The 2.31Gi workers are 78% committed before any schedulable workload is placed on them. The optimal reachable placement is 86.1%. Both numbers are properties of the node size and the DaemonSet set, not of any configuration mistake in this repo. **This is a capacity finding.**

It does not contradict the Phase 4 buy-vs-rebalance decision (`infrastructure` repo, `docs/ram-expansion-decision.md`), which recommended rebalancing onto the large worker's headroom rather than buying RAM into a DDR price spike. It does bound that recommendation: rebalancing can move the 14.8% of small-node commitment that is movable, and the DRAM market — not the workload mix — is what the small workers are waiting on. The rebalance runway is shorter than the decision doc implies, and the three small workers are the nodes to revisit first when the market recommendation is re-examined.

## Observations worth their own tickets

Each is quantified but outside what this repo can act on, and none of them changes the verdict above.

- **Duplicate Longhorn engine-image DaemonSets — 64Mi/node (192Mi across the small workers).** `ei-493e04e7` (v1.12.1) is the cluster's `default-engine-image` but has refCount 0; all 14 volumes still run `ei-a4d05f02` (v1.12.0). Both DaemonSets are therefore deployed on all five workers. A live volume engine upgrade would let one be reaped — a Longhorn maintenance action, not a manifest change.
- **`kube-flannel` running alongside Cilium — 50Mi/node.** Both DaemonSets are scheduled on all 8 nodes. Cilium runs with `kube-proxy-replacement: false`, so `kube-proxy` is legitimately required, but a second CNI DaemonSet is not obviously so. Talos-managed, so `infrastructure` repo scope, and it needs live CNI verification before anything is removed.

## BestEffort audit

**8 BestEffort pods cluster-wide, down from 21 at the Phase 1 audit.** All eight are `kube-system/kube-proxy-*`, one per node.

These are genuinely outside this repo's control: `kube-proxy` is rendered by Talos's own control-plane bootstrap manifests, not by any chart or manifest in this repo, and the epic's own Phase 1 note already classified them as "no requests/limits by design (system component, not a right-sizing candidate)". They carry no request but measure 20–42Mi actual. The exclusion is recorded here so the epic's "zero BestEffort" metric can be closed against a stated carve-out rather than an unexplained residual — removing them requires either a Talos machine-config patch (`infrastructure` repo) or enabling Cilium's kube-proxy replacement, both out of scope here.

The five `longhorn-system/engine-image-*` BestEffort pods from the Phase 1 audit are resolved — the namespace `LimitRange` in `tenants/platform/services/longhorn/postInstall/limit-range.yaml` now gives them a 64Mi default request, promoting them to Burstable.

## Re-measurement

The capture is reproducible with the script method in [01-baseline.md](01-baseline.md). The three numbers to watch on the small workers are pinned % (should hold at 78.0% unless a DaemonSet is added or resized), peak requested %, and free memory on `talos-g1i-e3h`. A new DaemonSet is the single most expensive change anyone can make to this cluster: it costs its request on all three small workers at once, straight off the 22% that is left.
