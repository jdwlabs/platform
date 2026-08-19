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
- Verify ExternalSecret references resolve in Vault: `platformctl tenants verify-secrets` — scans the `tenants/` tree by default, so it checks refs on an unmerged branch; add `--source cluster` to scan applied state instead. Refs it does not check (service absent from `tenant.yaml`, non-vault store, `dataFrom` pattern) are named with counts in the summary — read that line, a "0 issues" over a narrowed scan is not full coverage
- Validate Kubernetes manifests: `kubeconform` (used in CI)
- Build/test the binary: `cd cli && go build ./... && go test ./...`

### Bootstrap Process

Run `platformctl bootstrap` from the repo root. See [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md) for the phase summary and manual-touch points.

### Storage (Longhorn volumes)

- List volumes with their reclaim classification: `platformctl cluster volumes list` — TOON output, four default fields (`name,state,class,size`), widened by `--fields <csv>` or `--full` and narrowed by `--class orphaned|claimed|attached|other`
- Preview a reclaim: `platformctl cluster volumes reclaim --all-orphaned --dry-run` — lists exactly what would be deleted and mutates nothing
- Delete: `platformctl cluster volumes reclaim --all-orphaned --confirm`, or `--name <volume>` (repeatable) for specific ones. Reclaim refuses to run without `--confirm` or `--dry-run`, never reads stdin, and refuses any volume still claimed by a PVC or by a `Bound` PersistentVolume — a refusal is reported as a `refused` row and exits non-zero rather than being skipped quietly
- `--class` is the tool's verdict, not Longhorn's `state`. A claim is resolved from the PersistentVolumeClaim's `spec.volumeName`; the volume's own `status.kubernetesStatus.pvcName` is historical and repeats across generations of the same StatefulSet, so it never proves a volume is live. The `longhorn-single` class uses `Retain`, so detached volumes never age out on their own

### Storage (TrueNAS volumes)

`platformctl cluster volumes truenas` covers what Longhorn's backend cannot see: the objects the two democratic-csi drivers leave on the NAS. Both TrueNAS classes use `Retain`, so deleting a PVC deletes nothing there — one `truenas-iscsi` PVC leaks a zvol, an iSCSI extent, an iSCSI target and the target-extent mapping; one `truenas-nfs` PVC leaks a dataset and its NFS export. None of it is visible to the cluster.

- Same UX as the Longhorn backend: `list` emits TOON with four default fields (`name,kind,class,size`), widened by `--fields <csv>` or `--full`, narrowed by `--class orphaned|claimed|attached|other`. `--storage-class truenas-iscsi|truenas-nfs` narrows to one driver
- `platformctl cluster volumes truenas reclaim --all-orphaned --dry-run` previews; `--confirm` deletes; neither is assumed and stdin is never read. `--name <name>` is repeatable and is still checked against the same rules
- A refusal is a `refused` row and a non-zero exit, never a quiet skip. Under `--all-orphaned` that covers the `other` class — the classifier declining to conclude — while `claimed` and `attached` are simply not selected. A read that degraded the run, an unreadable session list above all, is a `warnings` line at the top of the output
- **A zvol's own name never proves it is live.** Provisioned objects are named for the PVC UID they were created for, and that name outlives the PV, the PVC and the workload — the same trap as Longhorn's recorded `pvcName`. Liveness is only ever read from the other side: a PersistentVolume whose CSI volume handle, volume attributes, NFS path or iSCSI IQN names the object (claims resolved from each PVC's `spec.volumeName`), or an open iSCSI session on a target that exports it. If the session list cannot be read, every zvol is refused — unknown liveness is not idle
- **The two liveness rungs are not both available.** The middleware keeps no client state for an NFS export, so `truenas-nfs` has no equivalent of the session rung and the PersistentVolume side is the whole of the evidence; a dataset an outside client mounts, or one whose PV a partial resync has not recreated, is indistinguishable from an idle one. The one NAS-side signal that survives is an export **above** the dataset, and a dataset covered by one is refused. Anything under the driver's `detachedSnapshotsDatasetParentName` is refused too. Because the gap cannot be closed from the NAS, it is disclosed at runtime: any selected set containing a `truenas-nfs` candidate gets a standing `warnings` entry saying so, under `--dry-run` and `--confirm` alike
- A reclaim that stops part-way is resumable. The candidate it stopped on is named on an `incomplete` line, deleting an already-absent object succeeds, and a re-run re-classifies from live state — so resuming is the same command again
- The iSCSI objects are joined by **numeric ID, not by name**: an extent's `disk` field is the only statement of which zvol it exports, and a target reaches its zvol only through a mapping row. A target named for volume A can be mapped to an extent exporting volume B, so every hop is resolved through IDs, and a target that also exports something else is refused
- Deletes run in dependency order (mapping → extent → target → export → dataset → `Released` PV) and each object is re-read and matched on its exact name immediately before the delete, because middleware row IDs are small and get reused
- The driver configs are read from the rendered `democratic-csi` Secrets, so the NAS address, dataset parents and iSCSI naming affixes follow the config rather than being hard-coded. `--truenas-ca-file` or `--truenas-insecure-skip-tls-verify` is required while the NAS presents its stock self-signed certificate
- The credential comes from `PLATFORMCTL_TRUENAS_API_KEY` and nowhere else by default. `--truenas-use-csi-api-key` opts into the key inside the driver-config Secret, which is the one democratic-csi provisions with — see the authentication note below

`platformctl` reaches the NAS over **JSON-RPC 2.0 on `wss://<host>/api/current`** — the API that replaces REST in TrueNAS 26, isolated behind one `Caller` interface. This is deliberately not the transport the CSI drivers use: they are stuck on REST and gate the NAS at 25.10.x (`docs/adr/0024-truenas-rest-removal-blocks-democratic-csi.md`), and every REST call also keeps the NAS's deprecation alert alive, which is the only live evidence that the drivers still depend on the removed transport.

**An authentication attempt is a mutation, not a read.** Repeated failures invalidate the `truenas-csi` key and take provisioning down for every class at once (`docs/adr/0025-truenas-metrics-what-the-graphite-push-can-and-cannot-carry.md`: four attempts did it, and that ADR also records this transport rejecting the key outright). Never probe or retry auth in a loop — fail fast on a rejection and report it. That is why the CSI key is opt-in rather than the default, and why a rejection prints its own help telling you **not** to re-run: export a throwaway read-only key as `PLATFORMCTL_TRUENAS_API_KEY` instead.

### Grafana Git Sync

Connection and Repository live in Grafana's own API server — invisible to `kubectl` and to ArgoCD — so `platformctl gitsync` is the only sanctioned way to read or reset them.

- Diagnose: `platformctl gitsync status` — TOON output, four default fields (`kind,name,healthy,syncState`); the full health message is printed for anything not healthy, and `--full` adds it for everything. Exits non-zero when any resource is unhealthy, when a resource reports no health at all, or when no resources exist (credentialed but not connected)
- Change a definition: merging an edit to `gitsync-resources.yaml` alone does nothing, because the apply Job creates but never updates. `platformctl gitsync recreate --dry-run` then `--confirm` deletes the repository **before** the connection and asks ArgoCD to re-run the apply Job
- Single resource: `platformctl gitsync delete --kind repository|connection --name <n> --confirm`
- Both delete paths refuse a repository that still owns dashboards (its remove-orphan-resources finalizer would collect them; override with `--allow-owned-dashboards`) and refuse a connection a repository still references
- A health message never names its own cause: a connection reporting `GitHub App lacks required 'webhooks' permission` is describing a requirement derived from a bound repository's `write` workflow, not a missing grant on the App

### Seeding one Vault field

- `platformctl bootstrap seed <spec> --field <name>` writes individual properties of one spec, so a new field can be added without re-supplying or being prompted for the credentials already at that path. Repeatable; requires exactly one spec argument; a field named explicitly is written even where the spec marks it optional
- An unknown spec key or field name is now an error listing the valid set. Previously an unrecognised key selected an empty spec, wrote nothing, and still reported success — which is how a binary older than the seed spec it is asked to write skips the field silently
- Seeding has no preview mode. `--dry-run` is accepted **only** by `cluster volumes reclaim`, `cluster volumes truenas reclaim`, `gitsync delete`, and `gitsync recreate` — the four commands that implement it. Every other command, `bootstrap seed` included, rejects the flag with an unknown-flag error rather than mutating while reporting a preview

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
- **ARC Controller**: Self-hosted GitHub Actions runners — dormant; CI runs exclusively on GitHub-hosted runners (service entries commented in tenant.yaml, see OPERATIONS.md)
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
| 3    | CNPG operator, ARC controller (dormant — commented out) |
| 4    | PostgreSQL clusters, db-ui, litellm-db, litellm-redis |
| 5    | postgres-backup, litellm, holmes, tenant ARC runner sets (dormant — commented out), Atlas schema migrations |
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
3. For a custom chart, add it under `helm-charts/` and reference it from the service entry — every image reference in that chart's values (and in the tenant overlay) must be digest-pinned or carry a documented exception in `tools/image-pin-allowlist.yaml`; `python3 tools/check-image-pins.py` gates this in CI
4. For a **remote** chart, the images you inherit are the chart's defaults, not anything visible in this repo. `python3 tools/check-remote-chart-image-pins.py` fetches the chart at its pinned `revision`, merges your overlay over it the way Helm does, and rejects any image whose tag can still move (`latest`, `main`, a truncated `v1`, or a tag absent with no chart `appVersion` behind it). Fix by overriding the tag in the overlay as `<tag>@sha256:<index digest>`, or record a documented exception in `tools/remote-chart-image-pin-allowlist.yaml`. Pin the **index** digest, not a per-arch child manifest
5. Ensure required Vault secrets are seeded if needed
6. Validate: `platformctl tenants validate`

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

## Concurrency: one worktree, one branch, one agent invocation

Multiple AI agents may operate against this repo at the same time. Never
work on `main`/`master`, and create a worktree before touching code. For
humans this is standing practice; for agents it is a **hard invariant,
not a convention they can relax**:

- Every agent invocation gets its own worktree and its own branch. Never
  share a worktree across two concurrent agent sessions, and never reuse
  one worktree for a second, unrelated task after the first is done —
  create a fresh one instead.
- Before rebasing or pushing, re-fetch `origin/<default-branch>` rather
  than trusting the worktree's cached view of it. A worktree that looks
  up to date can be stale by the time a concurrent session has pushed.
- Never assume you are the only agent with a checkout of this repo.
  Two sessions sharing state is how an unpushed local commit has landed
  on `main` minutes after a second, unrelated session already pushed —
  the failure is silent until the histories are compared.

This generalizes the same failure mode `deployments/.github/workflows/promote-prd.yml`
already had to solve for a single automated actor: a workflow-wide
`concurrency:` group serializes promotion runs so two can never race on
the same promotion branch. A shared mutable resource (a worktree, or a
branch) needs exclusive ownership per in-flight task, or concurrent
writers eventually race. See `docs/adr/0015-agentic-contribution-identity-and-review-gates.md`
("Concurrency and isolation") for the fuller rationale — that ADR is
still `proposed`; this invariant is the part of it already in force.

## Tooling Traps

RTK's filtered output is **not** the tool's output — it summarises, truncates, and
prints its own status lines. Every `rtk` row below is that one root cause. Run
anything you intend to act on through `rtk proxy <cmd>` and read the raw result.

| Symptom | Cause | Fix |
|---|---|---|
| `rtk go build -o <path>` prints `Go build: Success` but the binary is missing (or the real exit code is non-zero) | RTK's success line doesn't reflect the actual Go toolchain result; inside a git worktree, `go build` also fails VCS stamping (`error obtaining VCS status: exit status 128`) that RTK swallows silently | `rtk proxy go build ...` to see the real output/exit code; add `-buildvcs=false` when building `cli/` from a worktree |
| `helm template <release> <chart> > out.yaml` on a chart under `helm-charts/` writes a short file ending in a literal `... (N lines truncated)` marker | RTK caps the captured output regardless of the redirect target — one chart rendered 41 lines with the marker where the real render is 534 lines. Anything downstream (kubeconform, a diff, a review) then validates a fragment while looking successful | `rtk proxy helm template ...` for the untruncated render. Applies to local validation of `helm-charts/` and to reproducing the helm-lint CI job |
| `gh pr view <n>` reports `OPEN` for a PR that has already been merged | RTK caches the `gh` response, and the cached body is well-formed — unlike the truncation marker or the bogus success line above, a stale answer gives you nothing to notice. Observed on three PRs at once: `gh pr view` said `OPEN` while all three were already merged. The same staleness reaches the check summary, so a red gate can read green | `rtk proxy gh pr view <n>` (or `rtk proxy gh pr list`) returns live state. Via the API read `.merged`, not `.state` — REST only reports `open`/`closed`, so a merged PR reads `closed`: `gh api repos/<owner>/<repo>/pulls/<n> --jq .merged` |
| `gh pr edit` fails on every PR in this org | `gh` resolves the org through a GraphQL **query** that requires the `read:org` scope, and the active `GITHUB_TOKEN` (`ghp_...`) lacks it — it fails before any mutation is attempted (`the 'login' field requires ... ['read:org']`) | `unset GITHUB_TOKEN` so `gh` falls back to the keyring `gho_` OAuth token, which already carries `read:org`. Fallback if that token is unavailable: `gh api -X PATCH repos/<owner>/<repo>/pulls/<n> --input payload.json` |
| `gh run watch <n>` errors or watches nothing | It takes the run's **databaseId**, not the run number shown in the UI or in a `gh run list` number column | Resolve it first — `gh run list --json databaseId,number,headBranch` — and pass the `databaseId` |
| `.status.containerStatuses[].image` disagrees with the pod spec — a bare `sha256:…` with no repo, or a digest that matches nothing you deployed | That field carries the **config** digest, reported under whichever reference resolved first; `.imageID` carries the repo plus the **manifest** digest. Sampled live: `.image` was `sha256:9700374b…` with no repo while `.imageID` was `docker.io/jdwlabs/ai-sre-relay@sha256:f42b749b…` — two different digests for one running container | Read `.imageID`, never `.status…image`, when verifying which image is running. If the repo names still disagree, compare config/layer digests rather than concluding the wrong image is deployed |
| `curl --cacert <ca>.pem https://host` prints `http_code=000` on Windows and the CA is blamed or cleared on that alone | Windows curl uses the **Schannel** TLS backend, which **does** honour `--cacert` — but every TLS and connection failure reports `000`, so the body-level code carries no verdict either way | Read the **exit code**, not the HTTP code: `60` = chain/verification failed against the bundle you passed, `77` = the bundle itself is unreadable or unparseable, `7` = TCP connect refused (TLS never started, so the CA is irrelevant) |
| A PR that was `mergeable` goes `dirty`/`BLOCKED` with zero CI runs registered for the latest push, sometimes for many minutes | `pull_request`-triggered workflows check out the `refs/pull/<n>/merge` ref, and GitHub can't materialize that ref once the branch conflicts with the current `main` tip — so no run is ever created, independent of merge strategy. This repo's `required_linear_history` only decides *which* merge method can land (rebase here — `allow_squash_merge`/`allow_merge_commit` are both off), not whether checks queue | `gh api repos/<owner>/<repo>/pulls/<n> --jq '{mergeable, mergeable_state}'` to confirm before assuming CI is stuck; if `dirty`, `git fetch origin main && git rebase origin/main`, resolve, push, checks register within seconds |
| A locally-resolved conflict reappears at merge time even though the branch showed no conflict before pushing | Resolving with `git merge origin/main` creates a merge commit — but GitHub's rebase-merge button replays each of the branch's **original** commits individually and silently discards merge commits, so the pre-resolution conflict comes back exactly as if nothing was fixed | On a rebase-only repo, always resolve with `git rebase origin/main` (never `git merge origin/main`), then `git push --force-with-lease` — this replays your commits on the new base and the resolution actually sticks. This repo also requires the `signatures` check, and a plain `git rebase` only re-signs replayed commits if `commit.gpgsign=true` (or pass `-S` explicitly) |

## Verify Before You Start

Ticket evidence more than ~a week old (or gathered in a different investigation) is a hypothesis, not ground truth. Before acting on it:

- Re-confirm the ticket's premises against live state — don't build on a stale finding
- State the scope you searched before claiming something is absent or unowned ("checked all N tenants", "every object in the release") — one sample is not the whole set
- A disproved premise is a valuable result: record it on the ticket, don't quietly work around it

## Constraints

- Do not invoke `kubectl`, `vault`, or `helm` directly — use `platformctl`
- If `platformctl` cannot do what you need, file an issue instead of bypassing
- Never commit secrets — secrets are managed by Vault + ExternalSecrets Operator
- Decision records and plans are append-only: write a new one instead of editing a landed one
