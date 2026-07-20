# Cluster Memory Baseline — Phase 1

Captured live 2026-07-20 17:31 UTC, context `core`, 187 pods (160 reporting metrics), metrics-server available. This is the before-measurement the epic's reduction target is judged against.

## Methodology (repeatable each cycle)

```bash
kubectl top nodes
kubectl top pods -A --no-headers          # instantaneous usage
kubectl get nodes -o json                 # allocatable
kubectl get pods -A -o json               # declared requests/limits, QoS class
```

Requests/limits are summed per workload by collapsing pods to their ownerReference root (ReplicaSet hashes stripped to the Deployment name). Usage is a single metrics-server sample — instantaneous, not p95; capture during a quiet window and note that CI load swings worker usage.

Talos gotcha: kubelet reports `MemoryPressure=False` even on saturated nodes because Talos' OOMController fires before kubelet eviction — trust `kubectl top` actuals, never node conditions.

## Cluster totals

| Metric | Value | % of allocatable |
|---|---|---|
| Allocatable | 96.3 GiB | — |
| Sum requests | 23.3 GiB | **24.2%** |
| Live usage | 21.6 GiB | 22.4% |

The cluster is not starved — it is mis-declared and mis-placed. Requests under-declare real usage in exactly the places that hurt (control-plane static pods, litellm, Prometheus), while a handful of charts reserve multiples of what they use.

## Per-node commitment (`kubectl top nodes` 2026-07-20 17:31 UTC)

| Node | Role | Allocatable | req:alloc | use:alloc |
|---|---|---|---|---|
| talos-2qd-v0u | worker | 4373 Mi | 61.7% | 60% |
| talos-4h8-zy6 | worker | 14430 Mi | 34.5% | 36% |
| talos-6iz-oey | CP | 2763 Mi | 33.7% | **103%** |
| talos-fow-vbk | CP | 2763 Mi | 33.7% | 90% |
| talos-g1i-e3h | worker | 4373 Mi | 72.9% | 77% |
| talos-k3y-y3e | worker | 4373 Mi | 61.7% | 67% |
| talos-lx0-6a4 | worker (pve5) | 62749 Mi | **11.9%** | **11%** |
| talos-oam-s4g | CP | 2763 Mi | 33.7% | 92% |

Two structural findings, unchanged from the 2026-07-15 capture:

1. **All three control-plane nodes run 90–103% live** while the scheduler sees them one-third committed: each CP's static pods (`kube-apiserver`/`etcd`/controller-manager, surfaced as `kube-system/talos-*` mirror pods) request 704 Mi but use 1.6–2.0 GiB. These are Talos-managed machine-config territory (infrastructure repo CP-resize runbook), not Helm values — out of scope for platform-repo trims, in scope for the epic.
2. **pve5 (talos-lx0-6a4) idles ~54 GiB** while the three small 4 Gi workers sit at 60–77% use. Placement beats trimming: moving pressure to pve5 reclaims more headroom than any single chart trim.

## Per-namespace requests vs usage (Mi)

| Namespace | reqMi | useMi |
|---|---|---|
| monitoring | 6077 | 5005 |
| longhorn-system | 5120 | 3237 |
| kube-system | 2954 | 6016 |
| ai-sre | 2896 | 3023 |
| jdwlabs-prd | 1568 | 400 |
| database | 1536 | 1062 |
| argocd | 1472 | 1042 |
| jdwlabs-non | 1312 | 297 |
| vault | 320 | 394 |
| vault-config-operator | 250 | 26 |
| metrics-server | 200 | 49 |
| kubelet-serving-cert-approver | 64 | 14 |
| dotablaze-tech-prd | 32 | 6 |
| dotablaze-tech-non | 32 | 11 |
| nginx-gateway | 0 | 423 |
| cnpg-system | 0 | 54 |
| headlamp | 0 | 48 |
| cert-manager | 0 | 135 |
| arc-systems | 0 | 53 |
| external-secrets | 0 | 183 |
| democratic-csi | 0 | 571 |
| porkbun-webhook | 0 | 76 |

`kube-system` using 2× its requests is the CP static-pod under-declaration; the zero-request namespaces at the bottom are the QoS-hygiene debt inventoried in [02-qos-offenders.md](02-qos-offenders.md).

## Largest requests-vs-usage gaps (candidates, not verdicts)

| Workload | reqMi | useMi | gapMi |
|---|---|---|---|
| longhorn-system/longhorn-manager (DS ×5) | 5120 | 837 | 4283 |
| monitoring/platform-loki-results-cache | 1229 | 24 | 1205 |
| ai-sre/platform-holmes-holmes | 2048 | 853 | 1195 |
| jdwlabs-non/jdwlabs-usersrole-non | 1024 | 224 | 800 |
| jdwlabs-prd/jdwlabs-usersrole-prd | 1024 | 331 | 693 |
| database/platform-postgresql-cluster-non (×3) | 768 | 224 | 544 |
| database/platform-postgresql-cluster-prd (×3) | 768 | 237 | 531 |
| monitoring/platform-tempo | 512 | 55 | 457 |

## Counter-finding: under-declared workloads (requests must go UP)

| Workload | reqMi | useMi | QoS |
|---|---|---|---|
| ai-sre/platform-litellm | 512 | 1988 | Burstable |
| kube-system CP static pods (×3 nodes) | 704 ea | 1629–1962 ea | Burstable |
| database/platform-db-ui-adminer | 0 | 601 | BestEffort |
| monitoring/prometheus (kps) | 1024 | 1521 | Burstable |
| democratic-csi/node (DS ×5) | 0 | 428 | BestEffort |
| longhorn instance-managers (×5) | 0 | 291–403 | Burstable |

Any reduction target must be net of these raises — cutting requests cluster-wide while these stay invisible to the scheduler would repeat the CNPG/hugepages failure mode.

Full per-workload table: [appendix-workloads.md](appendix-workloads.md). Verdicts and the numeric target: [04-hit-list.md](04-hit-list.md).
