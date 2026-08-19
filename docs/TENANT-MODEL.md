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
- **NetworkPolicy**: The policy engine is live but **isolates nothing today** — treat namespace/RBAC/AppProject scoping as the real isolation boundary. Chained Cilium is installed (see [ADR 0012](adr/0012-networkpolicy-enforcement-via-chained-cilium.md), [ADR 0013](adr/0013-chained-cilium-rollout-sizing-and-proof.md) and [ADR 0017](adr/0017-chained-cilium-rollout-corrections.md), which corrects 0013's rollout sequence and is the one to follow), and audit mode is off: `policy-audit-mode: false` in `cilium-config`, with all 8 `cilium-agent` pods reporting `PolicyAuditMode: Disabled`. Two independent gaps sit between that and actual isolation:
  - **Identity coverage.** Only pods created after the Cilium DaemonSet (`2026-08-13T06:44Z`) have a `CiliumEndpoint`, and without one a pod has no identity, so no policy is evaluated against it. Measured 2026-08-19: **84 of 136** pod-network Running pods have a `CiliumEndpoint` (61%), up from 18 of 125 (14%) two days earlier — ordinary churn enrolls pods on its own. Per node the spread is still stark: `talos-k3y-y3e` is 12/12 because its pods were all recreated after its agent started, while `talos-lx0-6a4` is 27/54 and `talos-4h8-zy6` is 23/40. Recreating a workload is the only thing that enrolls it. Re-measure with `platformctl cluster netpol coverage`; the sequenced remediation and its abort criteria are in [OPERATIONS.md §9](OPERATIONS.md#9-closing-the-cilium-managed-endpoint-gap), and the decision not to run it from a merge is [ADR 0028](adr/0028-cilium-managed-endpoint-coverage.md).
  - **Policy content.** A namespace only drops the `allow-all-ingress`/`allow-all-egress` pair — which otherwise subsumes every scoped rule beside it — when its `tenant.yaml` entry sets `enforce: true`. As of 2026-08-19 that is two namespaces: `jdwlabs-runners` and `dotablaze-tech-runners`; both also keep `allow-all-egress`, so enforcement there is ingress-only, and both have zero pods. `jdwlabs-non` and `dotablaze-tech-non` enforcement is deliberately deferred (see the `tenant.yaml` comment) until their pods are Cilium-managed and their allow-set has been checked against observed traffic. Every other namespace, platform ones included, still renders allow-all in both directions.

  **So no pod in the cluster is currently subject to a restrictive NetworkPolicy** — the only two enforcing namespaces have no pods to enforce against. "Cilium is enforcing" is true of the engine and false of every workload; do not treat a namespace's `enforce: true` as evidence that its pods are isolated. This is more stable than it looks: previously `jdwlabs-non`/`dotablaze-tech-non` enforced with zero identity coverage, so the next pod restart there would have silently started enforcing an unverified allow-set — deferring enforcement there removed that landmine. Closing the identity-coverage gap and un-deferring the `-non` namespaces are both tracked as JDWLABS-355; `jdwlabs-non` and `jdwlabs-prd` are now fully enrolled, `dotablaze-tech-non` and `dotablaze-tech-prd` are at 0/1 each.
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
