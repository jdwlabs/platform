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
- **NetworkPolicy**: The policy engine is live and **enforcing on all 4 tenant namespaces** as of 2026-08-21 — `jdwlabs-non`, `jdwlabs-prd`, `dotablaze-tech-non`, `dotablaze-tech-prd` each have `enforce: true`, no `allow-all-ingress`/`allow-all-egress` pair, and 100% `CiliumEndpoint` coverage (`platformctl cluster netpol coverage`). Chained Cilium is installed (see [ADR 0012](adr/0012-networkpolicy-enforcement-via-chained-cilium.md), [ADR 0013](adr/0013-chained-cilium-rollout-sizing-and-proof.md) and [ADR 0017](adr/0017-chained-cilium-rollout-corrections.md), which corrects 0013's rollout sequence and is the one to follow), and audit mode is off: `policy-audit-mode: false` in `cilium-config`.
  - **Root causes of the earlier gap, both fixed.** Two independent bugs blocked enforcement, tracked and root-caused on JDWLABS-355: (1) kubelet's probe traffic was never classified as Cilium's `host` entity under chained Flannel+Cilium networking, so `allow-kubelet-probes`' `fromEntities: [host]` rule never matched — fixed with a `fromCIDR` allow per node's actual gateway IP (PR #316). (2) the shared `database` namespace's own postgres pods were mostly unmanaged (no `CiliumEndpoint`), so cross-namespace egress to an unlucky backend got dropped even when the calling namespace's own coverage was 100% — fixed via a CNPG-safe promote+recreate sequence bringing all 6 postgres pods + `db-ui` under Cilium management. Both were caught live (two enforce attempts crashlooped and were reverted within ~90s each before the real fix landed) rather than assumed from config.
  - **Verification, not inference.** Watched via Hubble on the affected nodes through the final attempt: zero policy drops across 48+ seconds of stable pod restart counts, then a forced fresh-DB-reconnect test (`usersrole-prd` pod restart) confirmed zero denials — this is live evidence a real connection succeeded under the enforced policy, not just that `enforce: true` was set. `jdwlabs-runners`/`dotablaze-tech-runners` also carry `enforce: true` but have zero pods, so they were never the interesting case.

  **So every tenant-namespace pod is now genuinely subject to a restrictive NetworkPolicy**, verified live rather than assumed from the `enforce: true` flag alone. Full evidence trail: JDWLABS-355 comments, PRs #314 (reverted #315), #316, #317, #318 (reverted #319), #320.
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
