# Tenant Model

## What is a Tenant?

A tenant is a GitHub organization (or individual developer account) that:

1. Owns one or more Kubernetes namespaces with isolation boundaries
2. Has dedicated ARC runner sets registered to their GitHub org (dormant — CI runs on GitHub-hosted runners; see OPERATIONS.md "Self-hosted CI runners (ARC)")
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
- **ARC runner sets** registered to their GitHub org (dormant by default)
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
- **NetworkPolicy**: The policy engine is live but **isolates nothing today** — treat namespace/RBAC/AppProject scoping as the real isolation boundary. Chained Cilium is installed (see [ADR 0012](adr/0012-networkpolicy-enforcement-via-chained-cilium.md), [ADR 0013](adr/0013-chained-cilium-rollout-sizing-and-proof.md) and [ADR 0017](adr/0017-chained-cilium-rollout-corrections.md), which corrects 0013's rollout sequence and is the one to follow), and audit mode is off: `policy-audit-mode: false` in `cilium-config`, with all 8 `cilium-agent` pods reporting `PolicyAuditMode: Disabled`. Two independent gaps sit between that and actual isolation, and both have to close before any namespace is protected:
  - **Identity coverage.** Only pods created after the Cilium DaemonSet (`2026-08-13T06:44Z`) have a `CiliumEndpoint`, and without one a pod has no identity, so no policy is evaluated against it. Measured 2026-08-17: **18 of 125** pod-network Running pods have a `CiliumEndpoint` (14%). Per node the spread is stark — `talos-k3y-y3e` is 7/7 only because its pods were all recreated after its agent started, while `talos-lx0-6a4` is 5/58 and `talos-4h8-zy6` is 2/34. Restarting a workload is what enrolls it.
  - **Policy content.** A namespace only drops the `allow-all-ingress`/`allow-all-egress` pair — which otherwise subsumes every scoped rule beside it — when its `tenant.yaml` entry sets `enforce: true`. As of 2026-08-17 that is four namespaces: `jdwlabs-non`, `dotablaze-tech-non`, `jdwlabs-runners` and `dotablaze-tech-runners`. Every other namespace, platform ones included, still renders allow-all in both directions. The two runner namespaces also keep `allow-all-egress`, so even they are ingress-only, and both have zero pods.

  **The two gaps compound, and today they cancel out entirely.** The four enforcing namespaces hold 7 running pods between them (`jdwlabs-non` 6, `dotablaze-tech-non` 1) — and **0 of those 7 have a `CiliumEndpoint`**, because every one was created on 2026-08-07 or 2026-08-09, before Cilium existed. So **no pod in the cluster is currently subject to a restrictive NetworkPolicy**: the namespaces with real policy hold only pods with no identity, and the pods with identities live in namespaces that allow everything.

  Two consequences worth planning around. First, "Cilium is enforcing" is true of the engine and false of every workload — do not treat a namespace's `enforce: true` as evidence that its pods are isolated. Second, this is latent rather than stable: the next restart of anything in `jdwlabs-non` or `dotablaze-tech-non` enrols it and the policy starts biting immediately, so the allow-set for those namespaces wants verifying *before* the next deploy, not after. Closing the gap is tracked as JDWLABS-355.
- **ResourceQuota**: Prevents resource exhaustion by any single tenant
- **Vault**: Tenant secrets are under separate KV path prefixes

## Tenant secret seeding

`platformctl bootstrap phase 4` discovers tenant-scoped kv paths from each
`tenant.yaml` and prompts for the required fields. For each tenant
`<name>` listed in `tenants/`, the following paths are populated:

| Path                          | Fields                                     |
|-------------------------------|--------------------------------------------|
| `kv/<name>-github-app`        | `github_app_id`, `github_app_installation_id`, `github_app_private_key` |
| `kv/<name>-ai-keys`           | `openai_api_key`, `anthropic_api_key`, `openrouter_api_key`, `nvidia_api_key` |
| `kv/<name>-discord-bot-token` | `token`                                    |

In non-interactive mode, each field reads from
`PLATFORMCTL_<NAME>_<FIELD>` (uppercase, `-` → `_`). See
[OPERATIONS.md §6](OPERATIONS.md#6-non-interactive--ci-mode) for the full env contract.

## `deploymentRepo.url`

A tenant may keep its application manifests in a separate repo.
Set `deploymentRepo.url` in `tenant.yaml`:

```yaml
deploymentRepo:
  url: https://github.com/<tenant>/deployments.git
  revision: main
```

When set, the tenant's `<tenant>-deployments` ApplicationSet auto-generates
Apps from that repo. Leave the field unset if all of the tenant's
workloads live in `tenants/<name>/services/`.

### Private deployment repos

A public repo needs nothing beyond the field above — ArgoCD clones it
anonymously. A **private** repo additionally needs a repository credential,
or the ApplicationSet renders Apps that cannot sync.

Register the credential as a Secret in the `argocd` namespace labelled
`argocd.argoproj.io/secret-type: repository`, sourced from Vault via an
ExternalSecret so no credential is committed. GitHub App is the preferred
mechanism — it does not expire, is scoped to the repos you grant it, and
matches how git access is already provisioned elsewhere in this repo.

Reuse the per-tenant `kv/<tenant>-github-app` path that bootstrap phase 4
already seeds rather than adding a parallel one: its `github_app_id`,
`github_app_installation_id` and `github_app_private_key` fields map directly
onto ArgoCD's `githubAppID`, `githubAppInstallationID` and
`githubAppPrivateKey`. Seed it with
`platformctl bootstrap seed <tenant>-github-app`, run from a tree that
contains `tenants/<tenant>/` — seed specs are derived from the tenants found
on disk, so the key does not resolve before the tenant directory exists. See
`tenants/platform/services/argo-cd/postInstall/` for a worked example.

The App needs Contents: Read on the deployment repo, and must be installed on
it — an App that exists but is not installed yields an installation ID that
resolves to nothing.

The AppProject's `sourceRepos` is derived from `deploymentRepo.url` by the
`tenant-envelope` chart, so no separate project change is required.

## Removing a tenant

1. Delete the `tenants/<name>/` directory.
2. Commit and push — ArgoCD will prune the tenant's Apps and AppProject.
3. Run `platformctl bootstrap heal --orphan-namespaces` to clean up
   namespaces that ArgoCD left behind (governance cascade does not delete
   tenant ns automatically, by design — operator confirms each one).

> `platformctl tenants remove <name>` orchestrating all three steps is
> a tracked v2 feature.
