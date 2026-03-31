# Tenant Model

## What is a Tenant?

A tenant is a GitHub organization (or individual developer account) that:

1. Owns one or more Kubernetes namespaces with isolation boundaries
2. Has dedicated ARC runner sets registered to their GitHub org
3. Has their own database schemas within shared CNPG clusters
4. Has scoped secrets in Vault under a per-tenant path prefix
5. Has an ArgoCD AppProject restricting deployments to their own namespaces

## Current Tenants

| Tenant | GitHub Org | Namespace | Vault Prefix |
|--------|------------|-----------|--------------|
| jdwlabs |jdwlabs | `jdwlabs-runners` | `kv/jdwlabs` |
| dotablaze-tech | dotablaze-tech | `dotablaze-tech-runners` | `kv/dotablaze-tech` |

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

Each tenant lives under `tenants/<name>/`:

```
tenants/<name>/
├── tenant.yaml      # Tenant metadata (not machine-processed, for documentation)
├── config.yaml      # App registry consumed by tenant ApplicationSet
└── apps/
    ├── <app-name>/
    │   ├── values.yaml
    │   └── postInstall/
    │       └── ...
    └── ...
```

## Isolation Boundaries

- **Namespace**: Per-tenant namespaces prevent resouce collisions
- **ArgoCD AppProject**: Tenant apps can only deploy to their own namespaces
- **NetworkPolicy**: Default-deny with explicit allow rules per namespace
- **ResourceQuota**: Prevents resource exhaustion by any single tenant
- **Vault**: Tenant secrets are under separate KV path prefixes
