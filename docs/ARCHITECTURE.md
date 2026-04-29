# Platform Architecture

## Overview

`jdwlabs/platform` is a tenant-centric GitOps platform managing Kubernetes applications via ArgoCD. It enforces tenant boundaries, namespace isolation, and standardized resource controls.

## Repository Layout

```
platform/
├── bootstrap/        # ArgoCD ApplicationSets and AppProjects
├── platform/         # Shared infrastructure apps (Vault, cert-manager, etc.)
├── tenants/          # Per-tenant configurations (ARC runners, database schemas)
├── helm-charts/      # Custom, versioned Helm charts (porkbun-webhook, openclaw)
└── docs/             # Architecture and onboarding documentation
```

## Chart Management & Publishing
The platform manages custom Helm charts in the `helm-charts/` directory. These are versioned and published automatically to a GitHub Pages-hosted Helm repository:

- **Helm Repo URL**: `https://jdwlabs.github.io/platform/`
- **Automation**: Managed by the `.github/workflows/release.yaml` workflow, which packages charts and updates the repository index on every `main` branch push.

## ArgoCD Model

### Governance ApplicationSet (`bootstrap/governance-appset.yaml`)
- Scans `tenants/*/tenant.yaml` via git file generator.
- Renders `helm-charts/tenant-envelope` for each tenant.
- Generates per-tenant `<name>-services` and `<name>-deployments` ApplicationSets.

### Services Deployment
Services use versioned Helm charts from the repository:
```yaml
# tenants/jdwlabs/tenant.yaml
services:
  - name: openclaw
    chart: openclaw
    repo: https://jdwlabs.github.io/platform/
    revision: 0.1.15
```

## Traffic Routing
Traffic flows through DNS ➜ Router (NAT) ➜ HAProxy ➜ NGINX Gateway Fabric (NodePort) ➜ Backend Pods.

| Component | Purpose |
| :--- | :--- |
| **HAProxy** | External Load Balancer (Bare-metal) |
| **NGINX Gateway** | Gateway API Controller (DaemonSet) |
| **HTTPRoute** | Per-service host-based routing |

See `docs/ARCHITECTURE.md` for full detailed diagrams.
