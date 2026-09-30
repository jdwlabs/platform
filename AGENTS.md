# AGENTS.md

Canonical instructions for AI agents (Claude Code, Codex, Gemini CLI, Copilot)
in this repo. `CLAUDE.md` and `GEMINI.md` only import this file — edit here.
Keep it short: per-command and reference detail belongs in `docs/` behind a
one-line pointer, because Codex silently drops everything past 32 KiB.

## What this repo is

The GitOps source of truth for the jdwlabs Kubernetes cluster. ArgoCD syncs
`main`, so merging is deploying — never change the cluster directly to "fix"
drift; change the repo. The platform itself is modelled as a tenant:
`tenants/platform/tenant.yaml` plus `tenants/platform/services/<service>/`.

Where to read before changing things:

- Layout, ArgoCD model, routing: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- Bootstrap phases and dependency waves: [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md) —
  per-service waves are authoritative in each `tenant.yaml` (`syncWave`)
- Tenants: [docs/TENANT-MODEL.md](docs/TENANT-MODEL.md), onboarding in
  [docs/ONBOARDING.md](docs/ONBOARDING.md)
- Day-2 operations and the symptom→fix table:
  [docs/OPERATIONS.md](docs/OPERATIONS.md) (§5 troubleshooting)
- Decisions: `docs/adr/` — ADR numbers are unique (`tools/check-adr-numbering.py`
  runs in CI)

## Validate before pushing

- `yamllint tenants/ bootstrap/`
- `platformctl tenants validate`
- `platformctl tenants verify-secrets` — checks ExternalSecret refs against
  Vault, scanning the `tenants/` tree (so it covers an unmerged branch);
  `--source cluster` scans applied state instead. The summary line names refs
  it did not check — a "0 issues" over a narrowed scan is not full coverage
- `cd cli && go build ./... && go test ./...` — add `-buildvcs=false` when
  building from a worktree, or VCS stamping fails
- Every image reference must be digest-pinned, including images inherited from
  a remote chart's defaults. CI gates this with `tools/check-image-pins.py` and
  `tools/check-remote-chart-image-pins.py`; pin the **index** digest as
  `<tag>@sha256:<digest>`, or add a documented exception to
  `tools/image-pin-allowlist.yaml` / `tools/remote-chart-image-pin-allowlist.yaml`
- A new tenant Grafana folder needs all three of the `tenant.yaml` key, a
  `Repository` definition and a `TENANT_REPOSITORIES` line —
  `tools/check-gitsync-tenant-folders.py` fails CI on any subset

## Adding a platform service

1. Service entry in `tenants/platform/tenant.yaml` (chart, repo, revision,
   namespace, `syncWave` — copy wave placement from a neighbour)
2. Values at `tenants/platform/services/<service>/values.yaml`; extra
   manifests in `.../<service>/postInstall/`; custom charts under `helm-charts/`
3. Seed any Vault secrets it needs (below), then `platformctl tenants validate`

## Gated paths and required checks

`.github/`, `tools/`, `renovate.json`, `bootstrap/`, `tenants/platform/`,
`helm-charts/tenant-envelope/`, `tenants/*/tenant.yaml` and `cli/` need a
CODEOWNERS approval that no admin bypass clears (`.github/CODEOWNERS`,
`docs/adr/0026-review-gates-by-change-class.md`).

Branch rulesets live in `.github/rulesets/` and are applied manually with
`apply.sh` after merge. Read that script's header before renaming, merging or
removing a required CI job — the wrong order leaves PRs permanently unmergeable.

## Operating the cluster

`kubectl`, `helm` and friends are fine. Where `platformctl` has a command for
the job — heal, volume reclaim, gitsync, seeding, drain-check — prefer it: those
commands carry the refusal and liveness checks a raw `kubectl delete` skips.
Per-command detail: [docs/PLATFORMCTL.md](docs/PLATFORMCTL.md).

Pass `--json` when parsing output: one NDJSON event per state transition,
`status` ∈ `info | progressing | ok | broken | failed`.

| Exit | Meaning                 | Agent action                                   |
|------|-------------------------|------------------------------------------------|
| 0    | Done                    | Continue                                       |
| 1    | Hard failure            | Read the last `failed` event; stop             |
| 2    | Progressing (timed out) | Retry after a back-off                         |
| 3    | Broken state            | Run a `bootstrap heal` subcommand; don't retry blindly |
| 4    | User aborted            | Surface to the human; do not auto-retry        |

Rules that each prevented a real incident:

- **A TrueNAS authentication attempt is a mutation.** Repeated failures
  invalidate the `truenas-csi` key and stop provisioning for every storage
  class (ADR 0025). Never probe or retry auth in a loop; on a rejection, stop
  and export a throwaway read-only key as `PLATFORMCTL_TRUENAS_API_KEY`
- `--dry-run` exists only on `cluster volumes reclaim`,
  `cluster volumes truenas reclaim`, `gitsync delete` and `gitsync recreate`;
  every other command rejects it. Deletes need an explicit `--confirm`
- Seeding with no TTY: `platformctl bootstrap seed <spec> --field <f>
  --from-file <path|->`. There is no `--value` flag — argv leaks via `/proc`
  and shell history
- Grafana Git Sync objects are invisible to `kubectl` and ArgoCD; editing
  `gitsync-resources.yaml` alone changes nothing. Use `platformctl gitsync`
- Never commit secrets — they live in Vault and reach the cluster through
  ExternalSecrets

## Repo conventions

- ADRs in `docs/adr/` are append-only: never edit a landed record; write a new
  one that references it. Plans go in `docs/superpowers/plans/` (gitignored)
- Comments in any file here, YAML included, explain *why*; ticket IDs and
  PR numbers go in commits and PR descriptions only
- Commit format, AI attribution trailers and PR body rules:
  [CONTRIBUTING.md](CONTRIBUTING.md). `.github/workflows/agent-identity.yml`
  checks agent-authored commits
- Several agents work this repo concurrently: re-fetch `origin/main` right
  before rebasing or pushing — a worktree's cached view goes stale silently
  (rationale: `docs/adr/0015-agentic-contribution-identity-and-review-gates.md`)
- Treat ticket evidence older than about a week as a hypothesis: re-check it
  against live state, state the scope you searched before calling something
  absent, and record a disproved premise on the ticket

## Tooling traps

Repo- and cluster-specific traps (CI not queuing on a conflicted PR, merge vs
rebase here, `.imageID` vs `.image`): [docs/AGENT-TOOLING-TRAPS.md](docs/AGENT-TOOLING-TRAPS.md).
Box-wide tool traps: `~/.local/share/chezmoi/docs/agent-tooling-traps.md` (dotfiles).
