# Platform

Tenant-centric Kubernetes GitOps platform managed by ArgoCD.

## Structure

- `bootstrap/` - ArgoCD ApplicationSets and AppProjects
- `platform/` - Shared infrastructure apps (MetalLB, Vault, cert-manager, etc.)
- `tenants/` - Per-tenant configurations (ARC runners, database schemas)
- `helm-charts/` - Custom Helm charts (porkbun-webhook)
- `docs/` - Architecture and onboarding documentation

## Tenants

| Tenant         | GitHub Org     | Purpose                 |
|----------------|----------------|-------------------------|
| jdwlabs        | jdwlabs        | Jdwlabs platform        |
| dotablaze-tech | dotablaze-tech | Dotablaze Tech platform |

## Quick Links

- [Bootstrap Guide](docs/BOOTSTRAP.md) - first-time cluster setup
- [Architecture](docs/ARCHITECTURE.md)
- [Tenant Model](docs/TENANT-MODEL.md)
- [Onboarding Guide](docs/ONBOARDING.md)

## Deployment

Merging to `main` auto-deploys via ArgoCD. The `platform` and `tenants` ApplicationSets watch this repository.
