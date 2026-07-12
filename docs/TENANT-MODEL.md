# Tenant Model

## What is a Tenant?

A tenant is a GitHub organization (or individual developer account) that:

1. Owns one or more Kubernetes namespaces with isolation boundaries
2. Has dedicated ARC runner sets registered to their GitHub org
3. Has their own database schemas within shared CNPG clusters
4. Has scoped secrets in Vault under a per-tenant path prefix
5. Has an ArgoCD AppProject restricting deployments to their own namespaces

## Current Tenants

| Tenant         | GitHub Org     | Namespaces                                                           | Vault Prefix        | Deployment Repo              |
|----------------|----------------|----------------------------------------------------------------------|---------------------|------------------------------|
| jdwlabs        | jdwlabs        | `jdwlabs-non`, `jdwlabs-prd`, `jdwlabs-runners`                      | `kv/jdwlabs`        | `jdwlabs/deployments`        |
| dotablaze-tech | dotablaze-tech | `dotablaze-tech-non`, `dotablaze-tech-prd`, `dotablaze-tech-runners` | `kv/dotablaze-tech` | `dotablaze-tech/deployments` |

## Tenant Resources

Each tenant receives:

- **Namespaces** with `platform.jdwlabs.io/tenant` labels
- **ArgoCD AppProject** scoped to their namespaces (no cluster-scoped resources)
- **Vault path prefix** for secret isolation
- **ARC runner sets** registered to their GitHub org
- **Database schemas** managed by Atlas operator in shared CNPG clusters
- **NetworkPolicies** (default-deny + DNS + ingress controller)
- **ResourceQuota** and **LimitRange** per namespace

## Directory Structure

Each tenant lives under `tenants/<name>/` in the platform repo:

```
tenants/<name>/
├── tenant.yaml             # Tenant definition (processed by governance ApplicationSet)
└── services/
    └── <service-name>/
        ├── values.yaml     # Helm values for the service (e.g. ARC runner set)
        └── postInstall/    # Optional raw manifests applied after Helm install
            └── ...
```

Tenants with a `deploymentRepo` also maintain a separate Git repository for application deployments.
See [ARCHITECTURE.md](ARCHITECTURE.md#deployments-applicationset) for the deployment repo structure and config schema.

## Isolation Boundaries

- **Namespace**: Per-tenant namespaces prevent resource collisions
- **ArgoCD AppProject**: Tenant apps can only deploy to their own namespaces
- **NetworkPolicy**: Default-deny with explicit allow rules per namespace
- **ResourceQuota**: Prevents resource exhaustion by any single tenant
- **Vault**: Tenant secrets are under separate KV path prefixes

## Tenant secret seeding

`platformctl bootstrap phase 4` discovers tenant-scoped kv paths from each
`tenant.yaml` and prompts for the required fields. For each tenant
`<name>` listed in `tenants/`, the following paths are populated:

| Path                          | Fields                                     |
|-------------------------------|--------------------------------------------|
| `kv/<name>-github-app`        | `app_id`, `installation_id`, `private_key` |
| `kv/<name>-ai-keys`           | `openai_api_key`, `anthropic_api_key`, `openrouter_api_key`, `nvidia_api_key` |
| `kv/<name>-discord-bot-token` | `token`                                    |

In non-interactive mode, each field reads from
`PLATFORMCTL_<NAME>_<FIELD>` (uppercase, `-` → `_`). See
[OPERATIONS.md §6](OPERATIONS.md#6-non-interactive--ci-mode) for the full env contract.

## `deploymentRepo.url`

A tenant may keep its application manifests in a separate private repo.
Set `deploymentRepo.url` in `tenant.yaml`:

```yaml
deploymentRepo:
  url: https://github.com/<tenant>/deployments.git
  revision: main
```

When set, the tenant's `<tenant>-deployments` ApplicationSet auto-generates
Apps from that repo. Leave the field unset if all of the tenant's
workloads live in `tenants/<name>/services/`.

## Removing a tenant

1. Delete the `tenants/<name>/` directory.
2. Commit and push — ArgoCD will prune the tenant's Apps and AppProject.
3. Run `platformctl bootstrap heal --orphan-namespaces` to clean up
   namespaces that ArgoCD left behind (governance cascade does not delete
   tenant ns automatically, by design — operator confirms each one).

> `platformctl tenants remove <name>` orchestrating all three steps is
> a tracked v2 feature.
