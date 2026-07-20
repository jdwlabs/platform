# Ranked Hit List & Reduction Target — Phase 1 Exit Gate

Synthesis of [01-baseline.md](01-baseline.md), [02-qos-offenders.md](02-qos-offenders.md), and [03-chart-inventory.md](03-chart-inventory.md). This sets what Phase 2 executes against.

## Headline verdict: placement-first, not trim-first

The cluster is 24.2% request-committed with ~54 GiB idle on pve5 while three 4 Gi workers run at 60–77% live and all three CP nodes run 90–103%. No trim fixes that — scheduling pressure onto pve5 and making requests honest does. Trims still pay (they shrink the small-node footprint the scheduler must place), but the epic's win condition is *honest declaration + placement*, and the arithmetic below shows why: gross trims ≈ gross raises.

## Ranked hit list (by planned request delta, biggest first)

| # | Workload | Current req | Planned req | Δ Mi | Action | Phase |
|---|---|---|---|---|---|---|
| 1 | longhorn-manager (DS ×5) | 1024/node | 512/node | **−2560** | trim req, keep 2 Gi limit (churn-OOM history) | 2a |
| 2 | loki-results-cache | 1229 | 128 | **−1101** | trim (50× over) | 2a |
| 3 | usersrole non+prd | 1024 ea | 512 ea | **−1024** | **startupProbe FIRST**, then trim (prior outage) | 2b |
| 4 | holmes | 2048 | 1536 | −512 | trim to burst ceiling | 2a |
| 5 | tempo | 512 | 128 | −384 | trim (9× over) | 2a |
| 6 | postgresql non+prd (×6 pods) | 256 ea | 192 ea | −384 | mild trim; 192 Mi is the floor | 2b |
| 7 | argocd server + repo-server | 256 ea | 128 ea | −256 | trim | 2a |
| 8 | vault-config-operator | 250 | 64 | −186 | trim + add limit | 2a |
| 9 | metrics-server duplicate | 200 | 0 | −200 | delete platform release **or** Talos copy — decide in 2e | 2e |
| — | **Gross trims** | | | **≈ −6.6 GiB** | | |
| 10 | litellm | 512 | 2048 | +1536 | raise to reality | 2e |
| 11 | longhorn instance-managers (×5) | 0 | ~400 ea | +2000 | add requests | 2c |
| 12 | democratic-csi node+controller | 0 | ~640 total | +640 | add floors | 2c |
| 13 | prometheus (kps) | 1024 | 1536 | +512 | raise req | 2e |
| 14 | nginx-gateway (DS+deploy) | 0 | ~512 total | +512 | add floors | 2c |
| 15 | adminer → common-chart Deployment | 0 | 256 | +256 | replace chart, bound it | 2d |
| 16 | remaining BestEffort floors (~20 workloads × 64 Mi) | 0 | ~1280 | +1280 | add floors | 2c |
| — | **Gross raises** | | | **≈ +6.7 GiB** | | |

CP static-pod under-declaration (~1 GiB/node) is infrastructure-repo territory (CP resize runbook) and excluded from this repo's ledger.

## The numeric target (epic metric)

Requests:allocatable is the wrong single victory metric — trims and raises net to ~zero **by design**. The epic target is therefore a triplet, all measured against this baseline:

1. **Cluster req:alloc stays within 24% ± 3%** after Phase 2 (raises fully funded by trims — no net inflation).
2. **QoS hygiene reaches zero**: BestEffort 32 → 0, missing-request 45 → 0, missing-limit 50 → Talos-managed allowlist only (kube-proxy, flannel, CP static pods, kube-system metrics-server).
3. **Small-node relief via placement**: each 4 Gi worker ≤ 65% req:alloc and ≤ 70% use:alloc; pve5 ≥ 30% req:alloc (from 11.9%).

Conservative on purpose: the usersrole lesson (right-size without startupProbe → ~10 min prod outage) applies to every trim touching a JVM or slow-boot workload — probes land before cuts, every time.

## Phase 2 task mapping

- **2a — low-risk trims**: rows 1, 2, 4, 5, 7, 8 (no boot-sensitivity, generous limits stay)
- **2b — probe-gated right-sizing**: rows 3, 6 (startupProbe first, then trim, one env at a time, non before prd)
- **2c — QoS floors**: rows 11, 12, 14, 16 (platform add-ons get requests; limits where sane)
- **2d — adminer replacement**: row 15 (common-chart Deployment, bounded)
- **2e — raises + dedup**: rows 9, 10, 13 (honest requests up; kill duplicate metrics-server)

Execution order: 2c/2e raises land **before or with** 2a trims in any single ArgoCD sync wave that touches the same node pool — never cut visible requests while real usage stays invisible, or the scheduler packs nodes that are actually full (the CNPG/hugepages failure mode).
