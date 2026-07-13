# Bootstrap Doc + Tooling Revamp — Design Spec

**Date:** 2026-05-12
**Status:** Body complete, pending self-review + user review (sections 1–8 approved 2026-05-12)
**Owner:** jdwillmsen
**Skill:** superpowers:brainstorming → next: writing-plans

---

## 1. Problem statement

The current `docs/BOOTSTRAP.md` (656 lines) reads as a runbook but is too long, mixes happy-path with troubleshooting, has multiple stale claims vs. cluster reality, and produced an install that left the cluster in a stuck/half-deployed state. The user wants a complete revamp:

- Concise, machine-parseable (audience: solo operator + AI agent partner)
- Scripted everything possible, with idempotent re-run safety
- Identify and address all known install issues, not just rewrite text

## 2. Issue inventory (cluster ↔ doc gap, observed 2026-05-12)

| #  | Issue                                                                  | Evidence                                                                                              | Root cause                                                                  |
|----|------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------|
| 1  | `applicationset/platform-services` stuck terminating                   | `deletionTimestamp: 2026-05-12T05:26:09Z`, `finalizers: [foregroundDeletion]` on AppSet               | No detection/recovery in doc; user resorted to manual `patch.json` scratch  |
| 2  | Vault/ESO/cert-manager never deployed (pods 0 in namespaces)           | `kubectl get pods -n vault` / `-n external-secrets` / `-n cert-manager` empty                          | Phase 2 doc assumes cascade succeeded; no per-phase verification gate       |
| 3  | CNPG operator missing — `cluster` CRD not installed                    | `error: the server doesn't have a resource type "cluster"`                                            | Wave 3 silent failure; downstream of #2                                     |
| 4  | Flyway schema pods fail (`*-superuser` secret not found) ×4            | `Error: secret "platform-postgresql-cluster-non-superuser" not found`                                 | CNPG superuser secret naming undocumented; flyway dependency not surfaced   |
| 5  | `default` AppProject manually recreated (scratch `default-project.yaml`)| Untracked file in repo root                                                                           | Not declared in repo                                                        |
| 6  | Stuck-finalizer escape hatch needed (scratch `patch.json`)             | `{"metadata":{"finalizers":null}}` untracked                                                          | Not in doc or repo tooling                                                  |
| 7  | ClusterSecretStores `vault` + `k8s-secret-store` missing               | `kubectl get clustersecretstore` shows only tenant-scoped stores                                      | Doc claims auto-managed; chain broken upstream                              |
| 8  | cert-approver Phase 8 imperative `kubectl patch` recipes               | `docs/BOOTSTRAP.md` lines 567–579                                                                     | Should be repo-managed (chart/kustomize)                                    |
| 9  | `root-app.yaml` hardcodes `targetRevision: main`                       | `bootstrap/root-app.yaml`                                                                             | Cannot bootstrap from branch/PR without manual edit                         |
| 10 | Broken markdown fence in BOOTSTRAP.md                                  | Lines ~598–622 (fence closes mid-tree, plain text continues)                                          | Editorial bug                                                               |
| 11 | No "verify phase N before N+1" gates                                   | Whole doc                                                                                             | User cannot tell what's actually stuck                                      |
| 12 | Partial re-runs leave residue (`jdwlabs-ai` ns 3h45m vs services 60m)  | `kubectl get ns` ages mismatched                                                                      | No reset/cleanup procedure                                                  |
| 13 | `tools/validate-tenants.py` separate from main tooling                 | python script invoked from CI                                                                         | Yet-another-thing-to-install                                                |

## 3. Goals / non-goals

**Goals**
- One entry point command (`platformctl bootstrap`) drives end-to-end bring-up
- Every phase idempotent and individually re-runnable
- Doc shrinks ~75% by moving imperative recipes into the binary
- All scratch artifacts (`patch.json`, `default-project.yaml`, `namespaces.txt`) become repo-managed code or are deleted
- Phase 8 imperative cert-approver patches become a Helm chart
- AI agent can invoke binary with `--json` and parse phase events programmatically

**Non-goals**
- Replacing ArgoCD/Vault/CNPG with anything else
- Multi-cluster bootstrap orchestration
- Web UI for bootstrap

## 4. Decisions locked

| Topic                | Decision                                                                                       |
|----------------------|------------------------------------------------------------------------------------------------|
| Doc scope            | All docs in scope; primary focus BOOTSTRAP                                                     |
| Final doc layout     | BOOTSTRAP, OPERATIONS (NEW), ONBOARDING, ARCHITECTURE, TENANT-MODEL. Drop AI-AGENTS → CLAUDE.md |
| Automation level     | Script everything possible                                                                     |
| Audience             | Solo operator + AI agent partner                                                               |
| Substrate            | **Go single binary** (`cmd/platformctl`), distributed via GitHub Releases                      |
| Healing model        | `platformctl bootstrap heal --<flag>` subcommands, all idempotent                              |
| Branch override      | `--branch=<x>` flag, in-memory patch of `root-app.yaml`                                        |

## 5. Architecture

### 5.1 Binary layout

```
cmd/platformctl/main.go
internal/
├── bootstrap/{preflight,argocd,root,vault_init,vault_seed,backups,verify,heal}.go
├── k8s/      # client-go wrappers, wait helpers, finalizer strip
├── helm/     # helm action SDK
├── vault/    # hashicorp/vault/api client
├── tenants/  # tenant.yaml schema + validate (replaces tools/validate-tenants.py)
└── prompt/   # huh/charmbracelet interactive flows
.github/workflows/release.yml  # cross-compile Win/Linux/Mac amd64+arm64 on tag
```

### 5.2 CLI contract

```
platformctl bootstrap                         # full bring-up
platformctl bootstrap phase <1-6>             # single phase
platformctl bootstrap verify                  # alias phase 6
platformctl bootstrap heal [--all|--<flag>]   # recovery
platformctl vault seed                        # standalone seed prompts
platformctl tenants validate [path]           # replaces tools/validate-tenants.py
platformctl --branch=<x>                      # override root-app branch
platformctl --dry-run                         # log, no execute
platformctl --non-interactive                 # consume PLATFORMCTL_* env vars
platformctl --json                            # machine-parseable output
```

### 5.3 Phase contract

```go
type Phase interface {
    Name() string
    Number() int
    Detect(ctx) (State, error)  // already_done | in_progress | not_started | broken
    Apply(ctx) error            // idempotent
    Verify(ctx) error           // hard gate
}
```

States:
- `already_done` — skip Apply, run Verify, exit 0
- `not_started` — run Apply, run Verify
- `in_progress` — wait + re-Verify (5 min timeout), exit 2 if still pending
- `broken` — refuse Apply, suggest `heal`, exit 3

Exit codes: `0` done · `1` hard fail · `2` soft/progressing · `3` broken · `4` user abort

### 5.4 Phases

| # | Name           | Action                                                                                | Manual touch                            |
|---|----------------|---------------------------------------------------------------------------------------|-----------------------------------------|
| 1 | argocd-install | helm upgrade --install platform-argo-cd                                               | —                                       |
| 2 | root-apply     | kubectl apply root-app.yaml; wait AppProjects + governance AppSet (no deletionTimestamp) | —                                    |
| 3 | vault-init     | vault operator init, unseal, store keys/token in k8s secrets, enable kv               | Save `vault-init.json` offline (prompt) |
| 4 | vault-seed     | Prompt + put kv/{porkbun, grafana, longhorn, alertmanager, *-github-app, *-ai-keys, usersrole, *-discord-bot-token} | Enter secret values  |
| 5 | backups-init   | rclone OAuth → kv/rclone-gdrive                                                       | Browser OAuth                           |
| 6 | verify         | Apps Synced+Healthy, ES SecretSynced, certs Ready, CNPG healthy, ARC registered       | DNS records out-of-band                 |

### 5.5 Verification gates

| Phase | Gate                                                                                              |
|-------|---------------------------------------------------------------------------------------------------|
| 1     | `deploy/argocd-server` Available, `argocd-application-controller-0` Ready                         |
| 2     | `application/bootstrap` Synced+Healthy, AppProjects exist, `appset/platform-services` no deletionTimestamp |
| 3     | `vault status` initialized+unsealed, `secret/vault-token` in `external-secrets`, `clustersecretstore/{vault,k8s-secret-store}` both Valid |
| 4     | All required `externalsecret` SecretSynced=True, `clusterissuer/letsencrypt-prod` Ready=True       |
| 5     | `cronjob/postgres-backup` exists, `secret/rclone-gdrive` synced in `database`                     |
| 6     | All apps Synced+Healthy; all certs Ready; CNPG clusters healthy AND `<cluster>-superuser` + `<cluster>-app` secrets present per CNPG naming; flyway/atlas migration pods Completed; ARC runners registered |

**CNPG secret naming (Issue 4):** CNPG auto-generates `<cluster-name>-superuser` and `<cluster-name>-app` secrets in the cluster's namespace. Flyway/Atlas pods reference these by exact name — Phase 6 verify asserts both exist before declaring cluster healthy.

### 5.6 Healing primitives (`platformctl bootstrap heal`)

| Flag                              | Action                                                                            |
|-----------------------------------|-----------------------------------------------------------------------------------|
| `--stuck-finalizer=<kind/name>`   | Strip `metadata.finalizers` (replaces `patch.json` scratch)                       |
| `--default-project`               | Apply `bootstrap/argocd/projects/default.yaml` (NEW in-repo)                      |
| `--cert-approver`                 | Apply repo-managed cert-approver patches                                          |
| `--tls-reissue`                   | Delete stale TLS secrets so cert-manager retries                                  |
| `--orphan-namespaces`             | Delete tenant ns with no matching tenant.yaml                                     |
| `--all`                           | Run every safe healer in sequence (with confirm prompt)                           |

### 5.7 Repo changes

| Scratch / pain                  | Becomes                                                                |
|---------------------------------|------------------------------------------------------------------------|
| `default-project.yaml` (root)   | `bootstrap/argocd/projects/default.yaml`                               |
| `patch.json` (root)             | `internal/k8s/finalizer.go` + `heal --stuck-finalizer`                 |
| `namespaces.txt` (root)         | Deleted                                                                |
| Phase 8 imperative patches      | `helm-charts/kubelet-serving-cert-approver/` + `heal --cert-approver`  |
| Hardcoded `main` in root-app    | `--branch` flag in binary                                              |
| `tools/validate-tenants.py`     | `platformctl tenants validate`                                         |

## 6. Doc target outlines

### 6.1 BOOTSTRAP.md (~150 lines)

1. Prereqs
2. Install `platformctl` (Linux/Mac/Win/source)
3. One-shot `platformctl bootstrap`
4. Phase summary table with manual-touch column
5. Re-running phases
6. DNS records section
7. Non-interactive mode pointer to OPERATIONS
8. Heal commands index
9. Dependency-chain reference block

**Decision:** Raw `kubectl`/`vault` recipes dropped entirely. `platformctl` is sole entry point. Any state the binary cannot recover from is a `heal` subcommand gap, not a doc gap.

### 6.2 OPERATIONS.md (~180 lines, NEW)

1. **Day-2 access** — ArgoCD/Grafana/db-ui/Vault login retrieval
2. **Vault lifecycle** — unseal, token rotation, re-key, init-json recovery
3. **PostgreSQL ops** — manual backup, restore, failover, schema replay
4. **TLS certs** — force re-issue, ClusterIssuer health, DNS-01 troubleshooting
5. **Troubleshooting symptom→fix table** — extracted from BOOTSTRAP Phase 7 + healer recipes
6. **Non-interactive / CI mode** — `PLATFORMCTL_*` env vars, exit codes, `--json` schema, example GHA workflow
7. **Cluster lifecycle** — node drain/cordon, Talos rolling upgrade, full disaster recovery
8. **Observability quick-refs** — Loki query patterns, Prom alert routes, where-to-look first

## 7. Doc deltas (existing docs)

### 7.1 ARCHITECTURE.md

- Add **"Bootstrap surface"** section: diagram of `platformctl` ↔ cluster ↔ Vault ↔ ArgoCD
- Add **`extraSourceRepos` pattern** subsection (commit 46e8243 trip-up — multi-source Apps)
- Replace inline `kubectl`/`helm` commands with `platformctl` invocations
- Audit pass: fix drift vs. current `platform/` chart layout

### 7.2 TENANT-MODEL.md

- Replace `python3 tools/validate-tenants.py` with `platformctl tenants validate`
- Add **"Tenant secret seeding"** subsection — `platformctl vault seed` discovers tenant kv paths from `tenant.yaml`
- Document `deploymentRepo.url` field semantics (set vs unset)
- Add **"Removing a tenant"** — `platformctl tenants remove <name>` + cleanup expectations

### 7.3 ONBOARDING.md

- Step 1: install `platformctl` (link BOOTSTRAP §2)
- Step 2: clone repo + `platformctl tenants validate`
- Step 3: `deploymentRepo` init (if separate repo)
- Drop manual `yamllint`/`kubeconform` invocations — folded into `platformctl tenants validate` + CI
- Keep narrative structure; reduce ~30%

### 7.4 CLAUDE.md (absorbs AI-AGENTS.md)

- **Absorb AI-AGENTS.md content**, delete standalone file
- Add **"Binary contract for AI agents"** block:
  - `platformctl --json` event stream schema reference
  - Exit codes 0/1/2/3/4 meaning
  - Heal subcommand index with idempotency guarantees
  - "Never run raw kubectl/vault commands; if `platformctl` can't do it, file an issue"
- Add **"Spec workflow"** pointer: `docs/superpowers/specs/` is the design archive
- Existing "Development Commands" + "Architecture Overview" stay; tighten where possible

## 8. Open items (deferred to plan/implementation)

1. Migration order confirmation — currently: skeleton+preflight+verify → heal → argocd+root → vault → backups
2. Binary name confirmation — currently `platformctl`
3. `--json` event schema concrete shape (defer to Phase B implementation)
4. Whether `platformctl tenants remove` lands in v1 or v2

## 9. Implementation plan (after spec approval)

Next session: invoke `superpowers:writing-plans` to break work into checkpointed tasks:

- Phase A: Repo cleanup (kill scratch files, declare default-project, fix markdown)
- Phase B: Binary skeleton + preflight + verify (read-only, lowest risk)
- Phase C: Heal subcommands
- Phase D: argocd-install + root-apply phases
- Phase E: vault-init + vault-seed phases
- Phase F: backups-init phase
- Phase G: Doc rewrites (BOOTSTRAP, OPERATIONS, ARCHITECTURE, TENANT-MODEL, ONBOARDING, CLAUDE.md)
- Phase H: CI swap (`validate-tenants.py` → `platformctl`), cross-compile release workflow
- Phase I: Cert-approver Helm chart
