# AGENTS.md

Canonical context for AI agents (Claude Code, OpenAI Codex, Gemini CLI, GitHub Copilot, and others) working in this repository. `CLAUDE.md` and `GEMINI.md` are thin pointers to this file — make edits here.

## What This Repo Is

jdwlabs `platform` is the GitOps source of truth for the jdwlabs Kubernetes cluster. It configures the full platform stack via ArgoCD and a custom CLI called `platformctl`.

## Repository Structure

- `bootstrap/` — ArgoCD bootstrap: root Application (`root-app.yaml`), governance ApplicationSet (`governance-appset.yaml`), CRD bootstrap (`00-crds.yaml`, `crds/`), and AppProjects (`argocd/projects/`)
- `tenants/` — per-tenant configuration; `tenants/<name>/tenant.yaml` defines namespaces, services, and ARC runners; per-service values and post-install manifests live in `tenants/<name>/services/<service>/`
- `tenants/platform/services/` — the shared platform stack (Vault, cert-manager, nginx-gateway-fabric, monitoring, ...); the platform itself is modeled as a tenant
- `helm-charts/` — custom and vendored Helm charts (kubelet-serving-cert-approver, litellm-helm, porkbun-webhook, tenant-envelope)
- `cli/` — Go source for `platformctl` (`cmd/`, `internal/`, `Makefile`, `.goreleaser.yaml`)
- `observability/` — dashboards-as-code (jsonnet sources and generated dashboards)
- `scripts/` — operational helper scripts (Vault seeding)
- `tools/` — repo tooling (chart index generation)
- `docs/` — architecture and operations documentation (BOOTSTRAP.md, ARCHITECTURE.md, TENANT-MODEL.md, ONBOARDING.md, OPERATIONS.md); decision records under `docs/adr/`

## Key Concepts

- **GitOps:** Merging to `main` triggers ArgoCD sync — never apply changes directly to the cluster
- **platformctl:** The ONLY approved interface for cluster operations. All agent actions must go through this CLI. Raw kubectl/vault/helm are out of scope.
- **Tenant model:** Each tenant (e.g. jdwlabs, dotablaze-tech — and `platform` itself) has isolated namespaces, RBAC, ResourceQuotas, and ApplicationSets defined in `tenants/<name>/tenant.yaml`
- **Dependency waves:** Services deploy in dependency order via ArgoCD sync waves (see table below and `docs/BOOTSTRAP.md`)

## Development Commands

### Validation

- Validate all YAML files: `yamllint tenants/ bootstrap/`
- Validate tenant.yaml files: `platformctl tenants validate`
- Verify ExternalSecret references resolve in Vault: `platformctl tenants verify-secrets`
- Validate Kubernetes manifests: `kubeconform` (used in CI)
- Build/test the binary: `cd cli && go build ./... && go test ./...`

### Bootstrap Process

Run `platformctl bootstrap` from the repo root. See [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md) for the phase summary and manual-touch points.

## Architecture Overview

### GitOps Flow

- ArgoCD watches this repository for changes
- Merging to `main` triggers automatic synchronization via ArgoCD
- The governance ApplicationSet in `bootstrap/` expands every `tenants/<name>/tenant.yaml` into namespaces, RBAC, quotas, and per-service Applications
- Services deploy in dependency waves (see below)

### Tenant Model

Each tenant (jdwlabs, dotablaze-tech) has:

- Tenant-specific namespaces with labels and Pod Security Standards
- ResourceQuotas, LimitRanges, NetworkPolicies
- ArgoCD AppProject scoped to tenant namespaces
- ARC RBAC for runner namespaces
- Two ApplicationSets: `<tenant>-services` and `<tenant>-deployments` (if deploymentRepo.url set)

### Key Components

- **ArgoCD**: GitOps controller, self-managed via bootstrap
- **Vault**: Central secret management, initialized by `platformctl bootstrap phase 3`
- **cert-manager**: TLS certificate issuance via DNS-01 (Porkbun webhook)
- **ExternalSecrets Operator**: Syncs Vault secrets to Kubernetes secrets
- **Longhorn**: Block storage
- **CNPG Operator**: PostgreSQL clusters
- **ARC Controller**: Self-hosted GitHub Actions runners
- **Atlas Operator**: Database schema migrations
- **Monitoring Stack**: Prometheus, Grafana, Loki, Tempo, Alertmanager
- **AI-SRE Stack**: LiteLLM gateway, HolmesGPT agent, alert relay (`ai-sre` namespace)

### Dependency Waves (Simplified)

| Wave | Services |
|------|----------|
| -1   | CRDs (Gateway API, Prometheus, Cert-Manager) |
| 0    | Bootstrap (AppProjects, ArgoCD self-management) |
| 1    | cert-manager, porkbun-webhook, kubelet-serving-cert-approver, nginx-gateway-fabric, Longhorn, local-path-provisioner, democratic-csi |
| 2    | Vault, ESO, vault-config-operator, metrics-server, monitoring, Grafana, Loki, kube-prometheus-stack, Tempo, Headlamp |
| 3    | CNPG operator, ARC controller |
| 4    | PostgreSQL clusters, db-ui, litellm-db, litellm-redis |
| 5    | postgres-backup, litellm, holmes, tenant ARC runner sets, Atlas schema migrations |
| 6    | ai-sre-relay (alert webhook target; calls holmes and litellm) |

The authoritative per-service wave assignments live in `tenants/<name>/tenant.yaml` (`syncWave` field).

## Common Tasks

### Adding a New Tenant

1. Create directory under `tenants/<tenant-name>/`
2. Add `tenant.yaml` following existing templates
3. Validate: `platformctl tenants validate`
4. For tenant-specific secrets, they are seeded by `platformctl bootstrap phase 4`
5. If deployments exist in separate repo, set `deploymentRepo.url` in tenant.yaml

See [docs/ONBOARDING.md](docs/ONBOARDING.md) for the full flow.

### Adding a Platform Service

1. Add a service entry to `tenants/platform/tenant.yaml` (chart, repo, revision, namespace, `syncWave` — see existing entries for wave placement)
2. Add configuration at `tenants/platform/services/<service>/values.yaml`; extra manifests go in `tenants/platform/services/<service>/postInstall/`
3. For a custom chart, add it under `helm-charts/` and reference it from the service entry
4. Ensure required Vault secrets are seeded if needed
5. Validate: `platformctl tenants validate`

### Troubleshooting

See [docs/OPERATIONS.md §5](docs/OPERATIONS.md#5-troubleshooting-symptom--fix) for the symptom→fix table.

## Code & Manifest Comments

Never put a Jira ticket ID (`JDWLABS-*`) or PR/issue number in a comment in
any file here — YAML `values.yaml`/manifests included. Traceability lives
in the commit message and PR description; comments should explain *why*
the config is what it is so they stay meaningful after the ticket closes.

## Binary contract for AI agents

Any AI agent operating this repo MUST drive cluster operations through
`platformctl`. Raw `kubectl`/`vault`/`helm` invocations are explicitly
out of scope — if `platformctl` cannot do something the agent needs,
file an issue rather than reaching for an escape hatch.

### Output parsing

Always invoke with `--json` when consuming output programmatically. Every
state transition emits one newline-delimited JSON object:

```json
{"ts":"2026-05-12T18:00:00Z","phase":"bootstrap","name":"vault-init","status":"ok","message":"applied"}
```

`status` ∈ `info | progressing | ok | broken | failed`.

### Exit codes

| Code | Meaning                       | Agent action                                  |
|------|-------------------------------|-----------------------------------------------|
| 0    | Done                          | Continue                                      |
| 1    | Hard failure                  | Read the last `failed` event; stop            |
| 2    | Progressing (timed out)       | Retry after a back-off                        |
| 3    | Broken state                  | Run a `heal` subcommand; do not retry blindly |
| 4    | User aborted                  | Surface to the human, do not auto-retry        |

### Heal subcommand index (idempotent — safe to re-run)

| Subcommand                                                       | Effect                                       |
|------------------------------------------------------------------|----------------------------------------------|
| `bootstrap heal --stuck-finalizer --kind <kind> --name <name>`   | Strip metadata.finalizers                    |
| `bootstrap heal --default-project`                               | Apply bootstrap/argocd/projects/default.yaml |
| `bootstrap heal --cert-approver`                                 | Trigger ArgoCD refresh of cert-approver App  |
| `bootstrap heal --tls-reissue`                                   | Delete cert-manager-managed TLS secrets      |
| `bootstrap heal --orphan-namespaces`                             | Delete tenant-labeled ns with no tenant.yaml |
| `bootstrap heal --longhorn-fresh-install`                        | Create Longhorn SA + RBAC for pre-upgrade hook on fresh cluster |
| `bootstrap heal --stuck-sync --sync-app <name>`                  | Terminate stuck ArgoCD sync (Helm hook Job TTL race)           |
| `bootstrap heal --all`                                           | Run every healer in safe order               |

### Decision records and plans

Architecture decision records live in `docs/adr/` (version-controlled).
Implementation plans live in `docs/superpowers/plans/` (local scratch,
gitignored). Both are append-only — never edit a landed record or plan;
write a new one and reference the old.

## Constraints

- Do not invoke `kubectl`, `vault`, or `helm` directly — use `platformctl`
- If `platformctl` cannot do what you need, file an issue instead of bypassing
- Never commit secrets — secrets are managed by Vault + ExternalSecrets Operator
- Decision records and plans are append-only: write a new one instead of editing a landed one
