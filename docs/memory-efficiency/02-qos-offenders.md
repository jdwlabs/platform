# QoS Offender Inventory — Phase 1

Captured 2026-07-20 17:31 UTC, 187 pods / 22 namespaces, 87 distinct workloads (deduped by ownerReference root + namespace).

**Counts: BestEffort 32 · missing memory-limit 50 · missing memory-request 45 (containers, non-deduped).**

All four tenant-app namespaces (jdwlabs-non/prd, dotablaze-tech-non/prd) and argocd are clean — zero offenders in every class. The debt is platform add-ons, operators, and Talos-managed kube-system daemons. Each offender's fix target is its GitOps source path — never `kubectl edit`.

## BestEffort workloads

| Namespace | Workload | Kind | reqMi | useMi | Source |
|---|---|---|---|---|---|
| arc-systems | platform-arc-systems-gha-rs-controller | ReplicaSet x1 | 0 | 35 | tenants/platform/services/arc-systems |
| arc-systems | ubuntu-dotablaze-tech-794c4f45-listener | AutoscalingListener x1 | 0 | 9 | tenants/platform/services/arc-systems |
| arc-systems | ubuntu-jdwlabs-6d45476f-listener | AutoscalingListener x1 | 0 | 9 | tenants/platform/services/arc-systems |
| cert-manager | platform-cert-manager | ReplicaSet x1 | 0 | 37 | tenants/platform/services/cert-manager |
| cert-manager | platform-cert-manager-cainjector | ReplicaSet x1 | 0 | 66 | tenants/platform/services/cert-manager |
| cert-manager | platform-cert-manager-webhook | ReplicaSet x1 | 0 | 32 | tenants/platform/services/cert-manager |
| cnpg-system | platform-cnpg-operator-cloudnative-pg | ReplicaSet x1 | 0 | 54 | tenants/platform/services/cnpg-operator |
| database | platform-db-ui-adminer | ReplicaSet x1 | 0 | 601 | tenants/platform/services/{postgresql-cluster-non,postgresql-cluster-prd,db-ui,postgres-backup} |
| democratic-csi | platform-democratic-csi-controller | ReplicaSet x1 | 0 | 143 | tenants/platform/services/democratic-csi |
| democratic-csi | platform-democratic-csi-node | DaemonSet x5 | 0 | 428 | tenants/platform/services/democratic-csi |
| external-secrets | platform-external-secrets | ReplicaSet x1 | 0 | 63 | tenants/platform/services/external-secrets |
| external-secrets | platform-external-secrets-cert-controller | ReplicaSet x1 | 0 | 38 | tenants/platform/services/external-secrets |
| external-secrets | platform-external-secrets-webhook | ReplicaSet x1 | 0 | 82 | tenants/platform/services/external-secrets |
| headlamp | platform-headlamp | ReplicaSet x1 | 0 | 48 | tenants/platform/services/headlamp |
| kube-system | kube-proxy | DaemonSet x8 | 0 | 277 | Talos-managed (machine config, infrastructure repo) |
| longhorn-system | csi-attacher | ReplicaSet x3 | 0 | 77 | tenants/platform/services/longhorn |
| longhorn-system | csi-provisioner | ReplicaSet x3 | 0 | 96 | tenants/platform/services/longhorn |
| longhorn-system | csi-resizer | ReplicaSet x3 | 0 | 81 | tenants/platform/services/longhorn |
| longhorn-system | csi-snapshotter | ReplicaSet x3 | 0 | 101 | tenants/platform/services/longhorn |
| longhorn-system | engine-image-ei-75a03ec3 | DaemonSet x5 | 0 | 16 | tenants/platform/services/longhorn |
| longhorn-system | longhorn-csi-plugin | DaemonSet x5 | 0 | 259 | tenants/platform/services/longhorn |
| longhorn-system | longhorn-driver-deployer | ReplicaSet x1 | 0 | 9 | tenants/platform/services/longhorn |
| longhorn-system | longhorn-ui | ReplicaSet x1 | 0 | 3 | tenants/platform/services/longhorn |
| monitoring | loki-canary | DaemonSet x5 | 0 | 96 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-kube-prometheus-s-operator | ReplicaSet x1 | 0 | 58 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-kube-prometheus-stack-kube-state-metrics | ReplicaSet x1 | 0 | 33 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-kube-prometheus-stack-prometheus-node-exporter | DaemonSet x8 | 0 | 151 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki-gateway | ReplicaSet x1 | 0 | 13 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-monitoring-alloy-operator | ReplicaSet x1 | 0 | 57 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| nginx-gateway | platform-gateway-nginx | DaemonSet x5 | 0 | 321 | tenants/platform/services/nginx-gateway-fabric |
| nginx-gateway | platform-nginx-gateway-fabric | ReplicaSet x2 | 0 | 102 | tenants/platform/services/nginx-gateway-fabric |
| porkbun-webhook | platform-porkbun-webhook | ReplicaSet x1 | 0 | 76 | tenants (ns porkbun-webhook) |

## Missing memory limit (unbounded)

| Namespace | Workload | Kind | reqMi | useMi | Source |
|---|---|---|---|---|---|
| kube-system | kube-flannel | DaemonSet x8 | 400 | 219 | Talos-managed (machine config, infrastructure repo) |
| kube-system | metrics-server | ReplicaSet x1 | 200 | 41 | Talos-managed (machine config, infrastructure repo) |
| longhorn-system | instance-manager-0dd55633a873c28c711d5983ed1e613c | InstanceManager x1 | 0 | 403 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-2be1c9ddd99420211b3f57805148e5a6 | InstanceManager x1 | 0 | 322 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-6e592745aab39f836bdd203c337b46db | InstanceManager x1 | 0 | 291 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-abb8e4197009eb7715e4b927cf929f76 | InstanceManager x1 | 0 | 384 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-b3e0e06e43c6b95f26dca5953becc5ba | InstanceManager x1 | 0 | 358 | tenants/platform/services/longhorn |
| longhorn-system | longhorn-manager | DaemonSet x5 | 5120 | 837 | tenants/platform/services/longhorn |
| metrics-server | platform-metrics-server | ReplicaSet x1 | 200 | 49 | tenants/platform/services/metrics-server |
| monitoring | alertmanager-platform-kube-prometheus-s-alertmanager | StatefulSet x1 | 200 | 46 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-grafana | ReplicaSet x1 | 256 | 277 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki | StatefulSet x1 | 768 | 621 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki-chunks-cache | StatefulSet x1 | 614 | 552 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki-results-cache | StatefulSet x1 | 1229 | 24 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-monitoring-alloy-logs | DaemonSet x8 | 1424 | 1331 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-monitoring-alloy-singleton | ReplicaSet x1 | 50 | 170 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | prometheus-platform-kube-prometheus-s-prometheus | StatefulSet x1 | 1024 | 1521 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| vault-config-operator | platform-vault-config-operator | ReplicaSet x1 | 250 | 26 | tenants/platform/services/vault-config-operator |

## Missing memory request (scheduler-invisible, non-BestEffort)

| Namespace | Workload | Kind | reqMi | useMi | Source |
|---|---|---|---|---|---|
| longhorn-system | instance-manager-0dd55633a873c28c711d5983ed1e613c | InstanceManager x1 | 0 | 403 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-2be1c9ddd99420211b3f57805148e5a6 | InstanceManager x1 | 0 | 322 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-6e592745aab39f836bdd203c337b46db | InstanceManager x1 | 0 | 291 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-abb8e4197009eb7715e4b927cf929f76 | InstanceManager x1 | 0 | 384 | tenants/platform/services/longhorn |
| longhorn-system | instance-manager-b3e0e06e43c6b95f26dca5953becc5ba | InstanceManager x1 | 0 | 358 | tenants/platform/services/longhorn |
| longhorn-system | longhorn-manager | DaemonSet x5 | 5120 | 837 | tenants/platform/services/longhorn |
| monitoring | alertmanager-platform-kube-prometheus-s-alertmanager | StatefulSet x1 | 200 | 46 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-grafana | ReplicaSet x1 | 256 | 277 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki | StatefulSet x1 | 768 | 621 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki-chunks-cache | StatefulSet x1 | 614 | 552 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-loki-results-cache | StatefulSet x1 | 1229 | 24 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | platform-monitoring-alloy-singleton | ReplicaSet x1 | 50 | 170 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
| monitoring | prometheus-platform-kube-prometheus-s-prometheus | StatefulSet x1 | 1024 | 1521 | tenants/platform/services/{kube-prometheus-stack,loki,tempo,grafana,monitoring} |
## Definitive empty states

- jdwlabs-non, jdwlabs-prd, dotablaze-tech-non, dotablaze-tech-prd, argocd, kubelet-serving-cert-approver: **0 offenders** in all three classes.
- No BestEffort pods exist in vault, metrics-server, ai-sre, database (adminer aside — see hit list), or local-path-storage namespaces.

## Talos-managed allowlist (not fixable from this repo)

kube-proxy, kube-flannel, CP static pods (apiserver/etcd/controller-manager), kube-system metrics-server — machine-config territory in the infrastructure repo. Tracked by the epic, excluded from this repo's zero-offender goal. Note the kube-system metrics-server duplicates the platform `metrics-server` release (both ~200Mi req, ~45Mi use) — dedup is a hit-list item.
