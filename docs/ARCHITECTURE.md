# Platform Architecture

## Overview

`jdwlabs/platform` is a tenant-centric GitOps platform managing Kubernetes applications via ArgoCD. It enforces tenant boundaries, namespace isolation, and standardized resource controls.

## Layer model

The lab is split into four layers. Each has one job, and a layer can change
without the layers above it noticing: infrastructure could move from owned
hardware to a cloud provider and tenants would not change.

| Layer | Job | Repos |
|---|---|---|
| Infrastructure | The lab's own cloud provider: a control plane that provisions compute and clusters on whatever hardware is underneath | `jdwlabs/infrastructure` |
| Platform | The managed layer on top, comparable to a managed Kubernetes service: a security baseline every tenant inherits, plus shared services | `jdwlabs/platform` (this repo) |
| Delivery | Each tenant's GitOps declaration of what runs in its namespaces, kept apart from application code | one deployments repo per tenant |
| Applications | The software being built; it produces images and never deploys itself | one or more app repos per tenant |

Platform and Infrastructure are shared. Delivery and Applications belong to a
tenant, and the tenant boundary sits between them and the platform.

**Baseline a tenant inherits** from `helm-charts/tenant-envelope`, without
configuring it: an ArgoCD AppProject scoped to its own repos and namespaces,
Pod Security Standards labels on each namespace, network policies (default
deny once the namespace sets `enforce: true`), resource quotas, limit ranges,
priority classes, and its slice of the observability stack.

**Shared services** live under `tenants/platform/services/`: certificates,
secrets (Vault and External Secrets), PostgreSQL clusters, persistent storage, the ingress gateway, and metrics, logs, traces, dashboards and
alerting.

Where a change belongs: hardware or cluster lifecycle goes to infrastructure;
anything every tenant should get goes here; what one tenant runs goes to its
deployments repo; how the software behaves goes to its app repo.

## Repository Layout

```
platform/
├── bootstrap/        # ArgoCD root Application, governance ApplicationSet, AppProjects, CRDs
├── tenants/          # Per-tenant configurations; tenants/platform/services/ holds the
│                     # shared infrastructure apps (the platform is itself a tenant)
├── helm-charts/      # Custom, versioned Helm charts (porkbun-webhook, tenant-envelope, ...)
├── cli/              # Go source for platformctl
├── observability/    # Dashboards-as-code (jsonnet sources and generated dashboards)
├── scripts/          # Operational helper scripts
├── tools/            # Repo tooling (chart index generation)
└── docs/             # Documentation; decision records under docs/adr/
```

## Chart Management & Publishing
The platform manages custom Helm charts in the `helm-charts/` directory. These are versioned and published automatically to a GitHub Pages-hosted Helm repository:

- **Helm Repo URL**: `https://jdwlabs.github.io/platform/`
- **Automation**: Managed by the `.github/workflows/release.yaml` workflow, which packages charts and updates the repository index on every `main` branch push.

## Bootstrap surface

`platformctl` is the only entry point for cluster lifecycle operations.
It coordinates these four surfaces:

```
platformctl
  ├─ Helm exec ─────────────► Kubernetes API (argocd install in phase 1)
  ├─ Dynamic client ─────────► Kubernetes API (Applications, AppProjects, ApplicationSets)
  ├─ Vault HTTP API ─────────► Vault (init, unseal, kv-v2 read/write)
  └─ Embedded YAML ──────────► bootstrap/root-app.yaml (in-memory --branch patch)
```

All four surfaces are read by both `bootstrap` and `bootstrap verify`;
mutating phases (1–5) write to them. `bootstrap heal` subcommands target
specific surfaces directly to recover from known-bad states.

## ArgoCD Model

### Governance ApplicationSet (`bootstrap/governance-appset.yaml`)
- Scans `tenants/*/tenant.yaml` via git file generator.
- Renders `helm-charts/tenant-envelope` for each tenant.
- Generates per-tenant `<name>-services` and `<name>-deployments` ApplicationSets.

### `extraSourceRepos` and multi-source Apps

When a tenant's ArgoCD AppProject must reference more than one source
repo (e.g. tenant renders a chart or raw manifests from this repo and
also needs its private deployment manifests from `<tenant>/deployments`),
declare additional repos in `tenant.yaml`:

```yaml
project:
  extraSourceRepos:
    - https://github.com/jdwlabs/platform.git
```

Failure to set this causes ArgoCD to reject the Application with "repo
not permitted" errors.

### Services Deployment
Services use versioned Helm charts from the repository:
```yaml
# tenants/jdwlabs/tenant.yaml
services:
  - name: arc-runner-set-jdwlabs
    chart: gha-runner-scale-set
    repo: ghcr.io/actions/actions-runner-controller-charts
    revision: 0.14.1
```

## Traffic Routing
Traffic flows through DNS ➜ Router (NAT) ➜ HAProxy ➜ NGINX Gateway Fabric (NodePort) ➜ Backend Pods.

```
┌─────────────────────────────────────────────┐
│                  Internet                   │
└─────────────────────────────────────────────┘
                       │                       
              DNS: *.jdwlabs.com               
              CNAME ➜ jdwlabs.com              
            A ➜ <router public IP>             
                       │                       
                       ▼                       
┌─────────────────────────────────────────────┐
│          Router (NAT/Port Forward)          │
│                                             │
│             :80  ➜ <haproxy>:80             │
│            :443 ➜ <haproxy>:443             │
└─────────────────────────────────────────────┘
                       │                       
                       ▼                       
┌─────────────────────────────────────────────┐
│       HAProxy (Static IP, bare-metal)       │
│                                             │
│         :80/:443  ➜  nginx-gateway          │
│         :6443     ➜  Kubernetes API         │
│           :50000    ➜  Talos API            │
│         :9000     ➜  HAProxy stats          │
└─────────────────────────────────────────────┘
                       │                       
        ┌──────────────┼──────────────┐        
        │              │              │        
        ▼              ▼              ▼        
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│   Control   │ │   Control   │ │    Worker   │
│    Plane    │ │    Plane    │ │    Node     │
│             │ │             │ │             │
│ :30180 HTTP │ │ :30180 HTTP │ │ :30180 HTTP │
│ :30543 HTTPS│ │ :30543 HTTPS│ │ :30543 HTTPS│
│ :6443  K8s  │ │ :6443  K8s  │ │             │
│ :50000Talos │ │ :50000Talos │ │             │
└──────┬──────┘ └──────┬──────┘ └──────┬──────┘
       │               │               │       
       └───────────────┼───────────────┘       
                       │                       
             kube-proxy (iptables)             
               routes to pod IP                
                       │                       
                       ▼                       
┌─────────────────────────────────────────────┐
│          nginx-gateway-fabric Pod           │
│         (DaemonSet, one per worker)         │
│                                             │
│               Terminates TLS                │
│        Routes via Gateway + HTTPRoute:      │
│                                             │
│         argocd.jdwlabs.com   ➜ :443         │
│         vault.jdwlabs.com    ➜ :8200        │
│          grafana.jdwlabs.com ➜ :80          │
└─────────────────────────────────────────────┘
                       │                       
                       ▼                       
┌─────────────────────────────────────────────┐
│             Backend Service Pod             │
│         (argocd, vault, grafana...)         │
└─────────────────────────────────────────────┘                          
```

```mermaid
graph TB
    Internet --> Router
    Router --> HAProxy
    HAProxy --> CP1[Control Plane 1]
    HAProxy --> CP2[Control Plane 2]
    HAProxy --> Worker
    CP1 --> Ingress
    CP2 --> Ingress
    Worker --> Ingress
    Ingress --> Backend
```

### Components Summary

| Component | Purpose |
| :--- | :--- |
| **HAProxy** | External Load Balancer (Bare-metal) |
| **NGINX Gateway** | Gateway API Controller (DaemonSet) |
| **HTTPRoute** | Per-service host-based routing |
