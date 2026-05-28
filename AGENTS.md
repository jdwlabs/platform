# AGENTS.md

Context for AI agents (OpenAI Codex, GitHub Copilot, and others) working in this repository.

## What This Repo Is

jdwlabs `platform` is the GitOps source of truth for the jdwlabs Kubernetes cluster. It configures the full platform stack via ArgoCD and a custom CLI called `platformctl`.

## Key Concepts

- **GitOps:** Merging to `main` triggers ArgoCD sync — never apply changes directly to the cluster
- **platformctl:** The ONLY approved interface for cluster operations. All agent actions must go through this CLI. Raw kubectl/vault/helm are out of scope.
- **Tenant model:** Each tenant (e.g. jdwlabs, dotablaze-tech) has isolated namespaces, RBAC, ResourceQuotas, and ApplicationSets defined in `tenants/<name>/tenant.yaml`
- **Dependency waves:** Platform services deploy in dependency order via ArgoCD sync waves (-1 through 5). See `docs/BOOTSTRAP.md` for the wave table.

## Key Files

- `tenants/<name>/tenant.yaml` — defines namespaces, services, ARC runners for a tenant
- `bootstrap/` — ArgoCD bootstrap ApplicationSets and AppProjects
- `platform/` — Helm-based platform service Applications
- `cli/cmd/` — platformctl command implementations
- `docs/OPERATIONS.md §5` — symptom→fix troubleshooting table

## Constraints

- Do not invoke `kubectl`, `vault`, or `helm` directly — use `platformctl`
- If `platformctl` cannot do what you need, file an issue instead of bypassing
- Never commit secrets — secrets are managed by Vault + ExternalSecrets Operator
- Specs are append-only: write a new spec/plan instead of editing a landed one
