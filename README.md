# Platform

[![Validate](https://github.com/jdwlabs/platform/actions/workflows/validate.yml/badge.svg?branch=main)](https://github.com/jdwlabs/platform/actions/workflows/validate.yml)
[![Release Charts](https://github.com/jdwlabs/platform/actions/workflows/release.yaml/badge.svg?branch=main)](https://github.com/jdwlabs/platform/actions/workflows/release.yaml)

Tenant-centric Kubernetes GitOps platform managed by ArgoCD.

## Structure

- `bootstrap/` - ArgoCD ApplicationSets and AppProjects
- `platform/` - Shared infrastructure apps (Vault, cert-manager, nginx-gateway-fabric, etc.)
- `tenants/` - Per-tenant configurations (ARC runners, database schemas)
- `helm-charts/` - Custom Helm charts (porkbun-webhook)
- `docs/` - Architecture and onboarding documentation

## Tenants

| Tenant                                              | GitHub Org                                          | Deployment Repo                                              | Purpose                     |
|-----------------------------------------------------|-----------------------------------------------------|--------------------------------------------------------------|-----------------------------|
| [jdwlabs](https://github.com/jdwlabs) | [jdwlabs](https://github.com/jdwlabs) | [deployments](https://github.com/jdwlabs/deployments) | AI Agents & Application Services |
| [dotablaze-tech](https://github.com/dotablaze-tech) | [dotablaze-tech](https://github.com/dotablaze-tech) | [deployments](https://github.com/dotablaze-tech/deployments) | Gaming & Tech Services |
| [platform](https://github.com/jdwlabs/platform) | [jdwlabs](https://github.com/jdwlabs) | [platform](https://github.com/jdwlabs/platform) | Cluster Core Infrastructure |

## Quick Links

- [Bootstrap Guide](docs/BOOTSTRAP.md) - first-time cluster setup
- [Architecture](docs/ARCHITECTURE.md)
- [Tenant Model](docs/TENANT-MODEL.md)
- [Onboarding Guide](docs/ONBOARDING.md)

## Deployment

Merging to `main` auto-deploys via ArgoCD. The `platform` and `tenants` ApplicationSets watch this repository.
