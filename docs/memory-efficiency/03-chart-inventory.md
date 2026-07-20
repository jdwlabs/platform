# Helm Chart Memory Sizing Inventory — Phase 1

Reserves = chart-effective memory requests/limits × replicas from the GitOps source (`tenants/platform/services/*/values.yaml`, app charts in the deployments repo on the `common` library chart). Usage = live capture 2026-07-20 17:31 UTC (see [01-baseline.md](01-baseline.md)). Verdicts here are preliminary; Phase 2 finalizes them.

## Per-chart table

| Chart / release | Namespace | Pods | reqMi | limMi | useMi | Oversized? | Why / note |
|---|---|---|---|---|---|---|---|
| longhorn (manager DS) | longhorn-system | 5 | 5120 | 10240 | 837 | **Y** | 1 Gi/node req vs ~165 Mi actual; 1 Gi was set deliberately after churn-OOM — trim req, keep limit |
| longhorn (instance-managers) | longhorn-system | 5 | 0 | 0 | 1758 | **Y (inverse)** | no requests at all — scheduler-invisible ~350 Mi/node |
| longhorn (csi-*, ui, engine-image) | longhorn-system | ~20 | 0 | 0 | 642 | **Y (inverse)** | all BestEffort |
| kube-prometheus-stack (Prometheus) | monitoring | 1 | 1024 | 2048 | 1521 | **N — under** | healthy heavy user; req below live use, raise req |
| kube-prometheus-stack (alertmanager, operator, ksm, node-exporter) | monitoring | 11 | 200 | 0 | 288 | **Y (inverse)** | mostly BestEffort/no-limit small pods |
| loki (results-cache) | monitoring | 1 | 1229 | 1229 | 24 | **Y** | default memcached over-provision, 50× actual |
| loki (main + chunks-cache + canary + gateway) | monitoring | 8 | 1382 | 2150 | 1306 | N | roughly honest; add missing limits |
| tempo | monitoring | 1 | 512 | 1024 | 55 | **Y** | 9× over-provision, default values |
| monitoring (alloy logs/singleton/operator) | monitoring | 10 | 1474 | 2048 | 1558 | N | honest |
| grafana | monitoring | 1 | 256 | 512 | 277 | N | at req; missing limit on one container |
| holmes | ai-sre | 1 | 2048 | 2048 | 853 | **Y (mild)** | bursts to ~1.5 Gi during investigations — trim to 1.5 Gi req, not further |
| litellm | ai-sre | 1 | 512 | 2048 | 1988 | **N — under** | uses 4× its request; raise req to ~2 Gi |
| litellm-db / litellm-redis | ai-sre | 2 | 288 | 576 | 182 | N | honest |
| postgresql-cluster-non/prd (CNPG) | database | 6 | 1536 | 4608 | 461 | **Y (mild)** | 256 Mi/pod is the BestEffort-OOM floor learned the hard way — floor is 192 Mi, no lower |
| db-ui (adminer upstream chart) | database | 1 | 0 | 0 | 601 | **Y — replace** | full upstream chart for a 1-container tool, empty values, BestEffort, 601 Mi unbounded |
| postgres-backup | database | cron | 0 | 0 | — | N | short-lived jobs |
| argo-cd (server + repo-server) | argocd | 2 | 512 | 1024 | 160 | **Y (mild)** | 3× over |
| argo-cd (app-controller + others) | argocd | 4 | 960 | 2048 | 882 | N | controller legitimately heavy |
| vault | vault | 1 | 256 | 512 | 345 | **N — under** | raise req to ~384 Mi |
| vault-config-operator | vault-config-operator | 1 | 250 | 0 | 26 | **Y** | 10× over + no limit |
| cert-manager (3 pods) | cert-manager | 3 | 0 | 0 | 135 | **Y (inverse)** | all BestEffort |
| external-secrets (3 pods) | external-secrets | 3 | 0 | 0 | 183 | **Y (inverse)** | all BestEffort |
| cnpg-operator | cnpg-system | 1 | 0 | 0 | 54 | **Y (inverse)** | BestEffort operator with OOM history in its workloads |
| democratic-csi (node DS + controller) | democratic-csi | 6 | 0 | 0 | 571 | **Y (inverse)** | BestEffort, 571 Mi invisible |
| nginx-gateway-fabric (+ gateway DS) | nginx-gateway | 7 | 0 | 0 | 423 | **Y (inverse)** | BestEffort data plane |
| metrics-server (platform release) | metrics-server | 1 | 200 | 0 | 49 | **Y — dedup** | duplicates Talos kube-system metrics-server; one is free RAM |
| headlamp, arc-systems, porkbun-webhook, local-path, cert-approver | various | ~8 | 64 | 128 | 220 | **Y (inverse)** | small BestEffort tools; need floors only |
| usersrole non+prd (app, deployments repo) | jdwlabs-* | 2 | 2048 | 4096 | 555 | **Y — gated** | JVM; right-sizing without startupProbe caused a ~10 min prod outage — probe first, then trim |
| authui/other UI apps (deployments repo) | jdwlabs-*, dotablaze-* | ~6 | 832 | 1664 | 142 | Y (minor) | 32–256 Mi each, low stakes |

## Heavy-hitter coverage checklist

Longhorn ✓ · kube-prometheus-stack ✓ · Vault ✓ · CNPG ✓ · ESO ✓ · cert-manager ✓ · ArgoCD ✓ · Loki ✓ · app charts ✓ — none skipped.

## Replace-with-own candidates

Only **db-ui/adminer** clears the bar: one container, empty upstream values, BestEffort and unbounded at 601 Mi live. Replace with a plain Deployment on the in-house `common` library chart (bounded requests/limits, no upstream subchart baggage). Every other chart earns its keep — the problems are values, not the charts themselves.

## Reading the oversized column

- **Y** — reserves ≫ actual: trim requests (Phase 2a/2b).
- **Y (inverse)** — reserves *nothing* while using real memory: add floors (Phase 2c). Same hygiene failure, opposite direction; both distort scheduling.
- **N — under** — request below live use: raise (Phase 2e).
