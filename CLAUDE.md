# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

### Validation
- Validate all YAML files: `yamllint tenants/ bootstrap/`
- Validate tenant.yaml files: `platformctl tenants validate`
- Validate Kubernetes manifests: `kubeconform` (used in CI)
- Build/test the binary: `cd cli && go build ./... && go test ./...`

### Bootstrap Process
Run `platformctl bootstrap` from the repo root. See [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md) for the phase summary and manual-touch points.

## Architecture Overview

### Repository Structure
- `bootstrap/` - ArgoCD ApplicationSets and AppProjects for platform bootstrap
- `platform/` - Shared infrastructure applications (Vault, cert-manager, nginx-gateway-fabric, etc.)
- `tenants/` - Per-tenant configurations (tenant.yaml defines namespaces, services, ARC runners)
- `helm-charts/` - Custom Helm charts (porkbun-webhook, openclaw, tenant-envelope)
- `docs/` - Architecture and onboarding documentation (BOOTSTRAP.md, ARCHITECTURE.md, TENANT-MODEL.md, ONBOARDING.md, OPERATIONS.md)
- `cli/` - Go source for `platformctl` (`go.mod`, `internal/`, `cmd/`, `Makefile`, `.goreleaser.yaml`)

### GitOps Flow
- ArgoCD watches this repository for changes
- Merging to `main` triggers automatic synchronization via ArgoCD
- Platform and tenant ApplicationSets in bootstrap/ manage deployments
- Services deploy in dependency waves (see docs/BOOTSTRAP.md for wave details)

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
- **Monitoring Stack**: Prometheus, Grafana, Loki, Alertmanager

### Dependency Waves (Simplified)
- Wave -1: CRDs (Gateway API, Prometheus, Cert-Manager)
- Wave 0: Bootstrap (AppProjects, ArgoCD self-management)
- Wave 1: Gateway, cert-manager, nginx, Longhorn
- Wave 2: Vault + ESO + ClusterSecretStores
- Wave 3: CNPG operator, ARC controller
- Wave 4: PostgreSQL clusters, db-ui
- Wave 5: Tenant ARC runner sets, Atlas schema migrations

## Common Tasks

### Adding a New Tenant
1. Create directory under `tenants/<tenant-name>/`
2. Add `tenant.yaml` following existing templates
3. Validate: `platformctl tenants validate`
4. For tenant-specific secrets, they are seeded by `platformctl bootstrap phase 4`
5. If deployments exist in separate repo, set `deploymentRepo.url` in tenant.yaml

See [docs/ONBOARDING.md](docs/ONBOARDING.md) for the full flow.

### Adding a Platform Service
1. Add Helm chart to `platform/` directory (or use existing in helm-charts/)
2. Create Application in `bootstrap/platform/services/<service>/` with:
   - ApplicationSet pointing to helm chart
   - Values file with configuration
   - SyncWave set appropriately (see existing services for wave numbers)
3. Ensure service has required Vault secrets seeded if needed

### Troubleshooting
See [docs/OPERATIONS.md §5](docs/OPERATIONS.md#5-troubleshooting-symptom--fix) for the symptom→fix table.

## References
- [Bootstrap Guide](docs/BOOTSTRAP.md)
- [Operations Manual](docs/OPERATIONS.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Tenant Model](docs/TENANT-MODEL.md)
- [Onboarding Guide](docs/ONBOARDING.md)

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
| `bootstrap heal --all`                                           | Run every healer in safe order               |

### Spec workflow

Design specs live in `docs/superpowers/specs/`. Implementation plans
live in `docs/superpowers/plans/`. Both are append-only — never edit a
landed spec or plan; write a new one and reference the old.

### OpenClaw service (jdwlabs-ai)

OpenClaw is an autonomous agent runtime deployed as a tenant service:

- **Namespace:** `jdwlabs-ai`
- **Public URL:** `https://ai.jdwlabs.com`
- **Vault secrets:** `kv/jdwlabs-ai-keys` fields `anthropic_api_key`, `openai_api_key`
- **Troubleshoot connectivity:** `kubectl describe httproute openclaw -n jdwlabs-ai`
- **Logs:** `kubectl logs -n jdwlabs-ai -l app.kubernetes.io/name=openclaw -c openclaw`
