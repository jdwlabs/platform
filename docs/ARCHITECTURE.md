# Platform Architecture

## Overview

`jdwlabs/platform` is a tenant-centric GitOps repository managing Kubernetes applications via ArgoCD ApplicationSets
with explicit tenant boundaries, namespace isolation, and resource controls.

## Repository Layout

```
platform/
├── bootstrap/        # ArgoCD ApplicationSets and AppProjects
├── platform/         # Shared infrastructure apps (cluster-wide)
├── tenants/          # Per-tenant app configs and manifests
├── helm-charts/      # Custom Helm charts
└── docs/             # Documentation
```

## ArgoCD Model

### Governance ApplicationSet (`bootstrap/governance-appset.yaml`)

- Scans `tenants/*/tenant.yaml` via git file generator
- Renders `helm-charts/tenant-envelope` for each tenant
- Creates namespaces, quotas, limit ranges, network policies, AppProjects
- Generates per-tenant `<name>-services` and `<name>-deployments` ApplicationSets
- Services ApplicationSet deploys from Helm chart + values ref + optional postInstall raw manifests
- Deployments ApplicationSet (if `deploymentRepo.url` set) deploys from the tenant's deployment repo
- Automated sync with prune and self-heal

### Deployments ApplicationSet

When a tenant defines `deploymentRepo.url` in their `tenant.yaml`, the `tenant-envelope` chart generates a
`<tenant>-deployments` ApplicationSet. This enables tenants to manage their own application deployments in a separate
Git repository.

The ApplicationSet uses a **matrix generator** combining:

1. **Git file generator** scanning `argocd/*/config.yaml` in the deployment repo (one file per environment)
2. **List generator** expanding the `apps` array from each matched config file

Each entry in `apps` becomes an ArgoCD Application named `<tenant>-<name>`.

#### Deployment repo structure

```
<deploymentRepo>/
├── argocd/
│   ├── non/
│   │   └── config.yaml  # Defines apps for non environment
│   └── prd/
│       └── config.yaml  # Defines apps for prd environment
└── charts/
    └── <chart-name>/
        ├── Chart.yaml
        ├── templates/
        ├── values.yaml        # Base values
        ├── values-non.yaml    # Non-prod overrides
        └── values-prd.yaml    # Prod overrides
```

#### Config file schema (`argocd/<env>/config.yaml`)

```yaml
apps:
  - name: <name>                   # Used in Application name: <tenant>-<name>
    namespace: <target-ns>         # Must be a namespace the tenant owns
    chartPath: charts/<chart>      # Path to chart in the deployment repo
    syncWave: "0"                  # Default ordering (default: "0")
    valueFiles:                    # Helm value files relative to chartPath
      - values.yaml
      - values-<env>.yaml
```

Sync options applied to all deployment repo apps: `CreateNamespace=false`, `PruneLast=true`, `ServerSideApply=true`.

## Sync Wave Ordering

| Wave | Category            | Apps                                                  |
|------|---------------------|-------------------------------------------------------|
| 0    | Bootstrap           | argo-cd (self-management), metallb                    |
| 1    | Core infrastructure | cert-manager, ingress-nginx, longhorn                 |
| 2    | Platform services   | vault, external-secrets, monitoring, grafana, etc.    |
| 3    | Operators           | cnpg-operator, atlas-operator, arc-systems            |
| 4    | Shared databases    | postgresql-cluster-non, postgresql-cluster-prd, db-ui |
| 5    | Tenant workloads    | ARC runner sets, Atlas schemas                        |

## Namespace Strategy

- Platform namespaces: original names (`vault`, `argocd`, `monitoring`, etc.)
- Tenant namespaces: `<tenant>-<purpose>` (e.g. `jdwlabs-runners`, `dotablaze-tech-runners`)
- Database namespace: shared `database` (CNPG clusters platform-tier)

## Secret Management

- Vault at `http://vault.vault.svc.cluster.local:8200`
- ClusterSecretStore named `vault` for platform-wide access
- Vault KV paths: `kv/platform`, `kv/jdwlabs`, `kv/dotablaze-tech`
- ExternalSecret CRs in each namespace pull from ClusterSecretStore

## Infrastructure Stack

| Component                   | Purpose                                            | Namespace        |
|-----------------------------|----------------------------------------------------|------------------|
| MetalLB                     | Layer2 load balancer (192.168.1.240-250)           | metallb-system   |
| cert-manager                | TLS certificates via Let's Encrypt + Porkbun DNS01 | cert-manager     |
| ingress-nginx               | Ingress controller                                 | ingress-nginx    |
| Longhorn                    | Distributed block storage                          | longhorn-system  |
| Vault                       | Secret management                                  | vault            |
| ESO                         | External Secrets Operator                          | external-secrets |
| ArgoCD                      | GitOps continuous delivery                         | argocd           |
| CNPG                        | CloudNativePG database operator                    | cnpg-system      |
| Atlas                       | Database schema migration operator                 | atlas            |
| ARC                         | GitHub Actions Runner Controller                   | arc-systems      |
| Prometheus + Grafana + Loki | Observability stack                                | monitoring       |

## Domain

All ingresses use `*.jdwlabs.com` with Let's Encrypt TLS via Porkbun DNS01 webhook.
