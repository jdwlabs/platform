# Platform

[![Validate](https://github.com/jdwlabs/platform/actions/workflows/validate.yml/badge.svg?branch=main)](https://github.com/jdwlabs/platform/actions/workflows/validate.yml)
[![Release Charts](https://github.com/jdwlabs/platform/actions/workflows/release.yaml/badge.svg?branch=main)](https://github.com/jdwlabs/platform/actions/workflows/release.yaml)
[![Release platformctl](https://github.com/jdwlabs/platform/actions/workflows/release-platformctl.yml/badge.svg)](https://github.com/jdwlabs/platform/actions/workflows/release-platformctl.yml)
[![License](https://img.shields.io/badge/License-PolyForm%20NonCommercial%201.0-blue)](https://polyformproject.org/licenses/noncommercial/1.0.0/)

Tenant-centric Kubernetes GitOps platform managed by ArgoCD.

## Structure

- `bootstrap/` - ArgoCD ApplicationSets and AppProjects
- `platform/` - Shared infrastructure apps (Vault, cert-manager, nginx-gateway-fabric, etc.)
- `tenants/` - Per-tenant configurations (ARC runners, database schemas)
- `helm-charts/` - Custom Helm charts (porkbun-webhook)
- `docs/` - Architecture and onboarding documentation

## Tenants

| Tenant                                              | GitHub Org                                          | Deployment Repo                                              | Purpose                          |
|-----------------------------------------------------|-----------------------------------------------------|--------------------------------------------------------------|----------------------------------|
| [jdwlabs](https://github.com/jdwlabs) | [jdwlabs](https://github.com/jdwlabs) | [deployments](https://github.com/jdwlabs/deployments) | Workload Tier (AI & Browser-Automation) |
| [dotablaze-tech](https://github.com/dotablaze-tech) | [dotablaze-tech](https://github.com/dotablaze-tech) | [deployments](https://github.com/dotablaze-tech/deployments) | Workload Tier (Distributed Services) |
| [platform](https://github.com/jdwlabs/platform) | [jdwlabs](https://github.com/jdwlabs) | [platform](https://github.com/jdwlabs/platform) | Infrastructure Tier (Core Governance) |

## Quick Links

- [Bootstrap Guide](docs/BOOTSTRAP.md) — install `platformctl` + first-time cluster setup
- [Operations Manual](docs/OPERATIONS.md) — day-2 ops, troubleshooting, CI mode
- [Architecture](docs/ARCHITECTURE.md)
- [Tenant Model](docs/TENANT-MODEL.md)
- [Onboarding Guide](docs/ONBOARDING.md)

## Deployment

Merging to `main` auto-deploys via ArgoCD. The `platform` and `tenants` ApplicationSets watch this repository.
