# Platform Architecture

## Overview

`jdwlabs/platform` is a tenant-centric GitOps repository managing Kubernetes applications via ArgoCD ApplicationSets with explicit tenant boundaries, namespace isolation, and resouce controls.

## Repository Layout

```
platform/
├── bootstrap/        # ArgoCD ApplicationSets and AppProjects
├── platform/         # Shared infrastructure apps (cluster-wide)
├── tenants/          # Per-tenant app configs and manifests
├── helm-charts/      # Custom Helm charts
└── docs/             # Documenation
```

## ArgoCD Model

Two ApplicationSets manage all deployments:

### Platform ApplicationSet (`bootstrap/platform-appset.yaml`)
- Reads `platform/config.yaml`
- Deploys shared infrastructure (MetalLB, cert-manager, Vault, etc.)
- Uses the `platform` ArgoCD AppProject
- App naming: `platform-<name>`

### Tenant ApplicationSet (`bootstrap/tenant-appset.yaml`)
- Reads `tenants/*/config.yaml`
- Deploys tenant-specific apps (ARC runners, database schemas)
- Uses `tenant-<name>` ArgoCD AppProjects
- App naming: `<tenant>-<name>`
- Matrix generator (git file + list)
- Multi-source (Helm chart + values ref + optional postInstall raw manifests)
- Automated sync with prune and self-heal

## Sync Wave Ordering

| Wave | Category | Apps |
|------|----------|------|
| 0 | Bare metal networking | metallb |
| 1 | Core infrastructure | cert-manager, ingress-nginx, longhorn |
| 2 | Platform services | vault, external-secrets, monitoring, grafana, etc. |
| 3 | Operators | cnpg-operator, atlas-operator, arc-systems |
| 4 | Shared databases | postgresql-cluster-non, postgres-cluster-prd, db-ui |
| 5 | Tenant workloads | ARC runner sets, Atlas schemas |

## Namespace Strategy

- Platform namespaces: original names (`vault`, `argocd`, `monitoring`, etc.)
- Tenant namespaces: `<tenant>-<purposes>` (e.g. `jdw-runners`, `dotablaze-tech-runners`)
- Database namespace: shared `database` (CNPG clusters platform-tier)

## Secret Management

- Vault at `http://vault.vault.svc.cluster.local:8200`
- ClusterSecretStore named `vault` for platform-wide access
- Vault KV paths: `kv/platform`, `kv/jdwlabs`, `kv/dotablaze-tech/`
- ExternalSecret CRs in each namespace pull from ClusterSecretStore

## Infrastructure Stack

| Component | Purpose | Namespace |
|-----------|---------|-----------|
| MetalLB | Layer2 load balancer (192.168.1.240-250) | metallb-system |
| cert-manager | TLS certificates via Let's Encrypt + Porkbun DNS01 | cert-manager |
| ingress-nginx | Ingress controller | ingress-nginx |
| Longhorn | Distributed block storage | lognhorn-system |
| Vault | Secret management | vault |
| ESO | External Secrets Operator | external-secrets |
| ArgoCD | GitOps continuous delivery | argocd |
| CNPG | CloudNativePG database operator | cnpg-system |
| Atlas | Database schema migration operator | atlas |
| ARC | GitHub Actions Runner Controller | arc-systems |
| Prometheus + Grafana + Loki | Observability stack | monitoring |

## Domain

All ingresses use `*.jdwlabs.com` with Let's Encrypt TLS via Porkbun DNS01 webhook.
