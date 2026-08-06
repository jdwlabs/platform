# ADR: Identity, review gates, and concurrency for agentic contribution

Status: proposed.

## Problem

All four repos (`apps`, `platform`, `infrastructure`, `deployments`) are
single-maintainer: every PR is authored by `jdwillmsen`, and every ruleset
that requires a review requires it from a human. GitHub refuses to let an
account approve its own pull request, so a review-required merge authored by
`jdwillmsen` has exactly one path to `main`: an `OrganizationAdmin` bypass.

That path has already been used. Two PRs on `platform` show the whole
problem in one comparison:

| PR | Author | `reviewDecision` | How it merged |
|---|---|---|---|
| #112 (`fix(argocd): right-size application-controller memory`) | `jdwillmsen` (human) | `REVIEW_REQUIRED`, permanently | `--admin` bypass |
| #63 (`chore(deps): bump golang.org/x/net`) | `app/dependabot` (bot) | `APPROVED` | `jdwillmsen` reviewed and merged normally |

Same repo, same ruleset (`required_approving_review_count: 1`,
`require_code_owner_review: false`), same human doing the merging. The only
variable is who the PR's author is. When the author is `jdwillmsen`, no
review is possible and the gate has to be bypassed. When the author is a
distinct identity, the existing gate works exactly as designed — no ruleset
change, no bypass, no exception.

As AI agents start opening PRs against these repos, they inherit whichever
credential they run under. Today that is `jdwillmsen`'s own `gh`/git
credentials by default, which reproduces #112 on every agent-authored change
that touches a reviewed path — "author bypasses review" would become the
*routine* state instead of a two-time incident, which is the opposite of
what the epic's first success metric asks for (0 routine admin-bypass
merges).

This ADR is Phase 1 of JDWLABS-181 ("Design & decisions"). It proposes an
identity model, a review-gate matrix by change class, and a concurrency
contract for multiple agent worktrees. It changes no live ruleset, creates
no GitHub App or bot account, and touches no branch protection — those are
Phase 2+ and require Jake's explicit sign-off, executed as separate work.

## What already exists as precedent

`deployments` has been running an agent-facing authoring identity in
production since the PRD promotion workflow shipped:

- **`jdwlabs-release-bot`** — a first-party GitHub App. Its workflow
  (`.github/workflows/promote-prd.yml`) generates a short-lived installation
  token via `actions/create-github-app-token`, and writes the promotion
  commit through the GitHub contents API rather than a `git push`. The
  workflow's own comment states the property this buys: the commit is
  "created server-side, signed by GitHub, and authored by the bot identity
  behind `GH_TOKEN`" — i.e., GPG signing is inherited from GitHub's App
  commit-signing, with no key custody burden on the human maintainer at all.
  When the App credentials (`RELEASE_APP_ID` / `RELEASE_APP_PRIVATE_KEY`)
  are absent, the workflow explicitly falls back to `GITHUB_TOKEN` and warns
  that the resulting PR won't trigger CI — the App path is the intended one.
- **`PRD Promotion Review Gate`** ruleset (`deployments/.github/rulesets/prd-promotion-review-gate.json`) —
  `require_code_owner_review: true`, scoped to `main`, with no path
  condition of its own. The path-scoping instead comes from
  `.github/CODEOWNERS`, which owns only `/charts/*/values-prd.yaml`,
  `/argocd/prd/`, and `/.github/` to `@jdwillmsen`. Its comment states the
  intent directly: "no catch-all: an unowned path needs no code-owner
  approval, which is what lets the release bot merge its own appVersion
  bumps." Combined with the repo's `Baseline` ruleset
  (`required_approving_review_count: 0`), this is already a two-tier
  review-gate matrix by change class, running today: unowned paths merge
  with automated checks only, CODEOWNERS-owned paths require an explicit
  human owner approval.
- **`dependabot`** (#63 above) is the second, independent proof that any
  distinct authoring identity — not just a purpose-built App — clears the
  self-approval wall.

Nothing here is hypothetical. The fix this ADR proposes is: take the pattern
already proven in one repo for one workflow, and generalize it to be the
standard authoring identity and review-gate shape for agent-driven changes
across all four repos.

## Options considered — identity

**A second human-adjacent GitHub account** (an alt account under Jake's
control, or a shared service-account login used interactively). Rejected.
It doesn't distinguish which agent produced a given PR when more than one
agent runs concurrently, which directly undermines the epic's observability
goal — every PR would carry the same author identity regardless of which
agent, session, or task produced it. It also needs its own GPG key custody,
extending a problem this org already has open running costs on (age key not
present on the workstation; a full-org GPG re-sign was needed once already).
A GitHub App commit gets GitHub's own commit signing for free, an alt human
account does not.

**Route agent commits through `jdwillmsen`'s own credentials, unchanged.**
Rejected — this is the status quo that produced #112, and scaling it to
routine agent activity converts a two-time exception into the default
failure mode the epic exists to eliminate.

**A first-party GitHub App as the agent-authoring identity.** Chosen. This
generalizes the `jdwlabs-release-bot` pattern already running in
`deployments`. It gets: GitHub-signed commits via the contents API (no key
management), fine-grained per-repo permission scoping (install only on the
repos and content paths an agent workflow needs), a stable non-human account
that `jdwillmsen` (or a future second reviewer) can always approve without
tripping the self-review block, and an identity that shows up distinctly in
`reviewDecision`/`mergedBy`/audit logs — the mechanism the epic's third and
fourth success metrics (concurrent actors, review time) need to measure
against.

Phase 1 does not decide whether every agent gets its own App installation
or all agents share one App with per-session commit metadata for
attribution — that's flagged as an open question for Phase 2, where the
concrete observability requirements (which agent produced which PR) can be
weighed against the operational cost of managing N App registrations.

## Options considered — review-gate matrix

The `deployments` PRD gate proves the mechanism (CODEOWNERS ownership +
`require_code_owner_review`, layered under a baseline
`required_approving_review_count`) generalizes cleanly to more than two
tiers. Proposed matrix, using each repo's actual path structure:

| Change class | Example paths | Required review | Precedent / rationale |
|---|---|---|---|
| Docs-only | `docs/**`, `*.md`, ADRs | 0 human review; automated checks only (lint, signature, scan) | `deployments` `Baseline` already runs `required_approving_review_count: 0` for everything not CODEOWNERS-scoped |
| Routine / non-prod config | `platform/tenants/*/services/**` (non-prod), chart `values.yaml` (non-prd), non-prd ArgoCD apps | 1 approval, any qualified reviewer (not owner-scoped) | Matches current `platform`/`apps` `Baseline` (`required_approving_review_count: 1`, `require_code_owner_review: false`) |
| Release-pipeline / prod-facing | `deployments/charts/*/values-prd.yaml`, `argocd/prd/**`, `.github/workflows/**`, `.github/rulesets/**` | CODEOWNERS-gated human owner approval | Direct extension of the live `PRD Promotion Review Gate` — same shape, same repo, already proven |
| Infra-apply | `infrastructure/terraform/**`, Talos machine-config, `bootstrap/**` | CODEOWNERS-gated owner approval; `terraform apply` remains a separate, explicit human action regardless of PR approval (unchanged constraint — infra forbids autonomous `apply`) | Highest operational risk; this ADR does not touch the no-autonomous-apply rule, only who reviews the PR that precedes it |
| Agent-facing CLI / interface | `platform/cli/**` (`platformctl`), `infrastructure` bootstrap tooling (`talops`) | CODEOWNERS-gated owner approval | Already CODEOWNERS-scoped in `platform` (`/cli/ @jdwillmsen`) — carry the same gating forward for agent-authored changes to these interfaces |

The mechanism to enforce "author != approver" natively, rather than as a
convention: keep `required_approving_review_count >= 1` (or CODEOWNERS
gating) on every reviewed class, and require — procedurally now, technically
enforced in Phase 2 — that agent-driven changes are *only* ever opened
through the App identity, never through `jdwillmsen`'s personal
credentials. An agent that pushes under `jdwillmsen`'s own token
reintroduces #112 regardless of how the ruleset is shaped; the identity
discipline is the actual fix, the ruleset is what makes the discipline
enforceable instead of aspirational.

## Bypass actors — a flagged hole, not a fix

Every current `Baseline` and `Production Gates` ruleset across all four
repos carries `{"actor_type": "OrganizationAdmin", "bypass_mode": "always"}`.
This is exactly the escape hatch #112 used, and it stays available to every
future agent-authored PR that hits a review wall unless something changes
who authors the PR in the first place. This ADR does not propose removing
it — a break-glass path has legitimate uses — but Phase 2 should treat any
*future* admin-bypass on an agent-authored PR as a signal that the identity
or gate design has a gap, not as a normal operational lever, and should log
bypass events toward the epic's "0 routine admin-bypass merges" metric.

`deployments`' `PRD Promotion Review Gate` ruleset separately grants an
`Integration` actor (`actor_id: 4065387`) a scoped `bypass_mode: pull_request`.
Its exact identity wasn't confirmed in this pass (candidates include an
existing automation app already active in the repo, such as Renovate) — Phase
2 should confirm it explicitly before using this ruleset as a template, since
an unscoped Integration bypass on a review gate is worth understanding before
it's replicated to three more repos.

## Concurrency and isolation

Two failure modes are already logged against this exact hazard, from human
sessions, and multi-agent concurrency makes both more likely, not less:

- **Shared worktree across concurrent sessions** — an unpushed local commit
  from one session landed on `main` twelve minutes after a second session
  pushed, because both were operating against the same worktree state. The
  fix already adopted for humans (re-fetch immediately before rebase/push,
  never assume a worktree is exclusively yours) has to be a hard invariant
  for agents, not a convention: **one worktree, one branch, one agent
  invocation** — never shared across sessions or reused across tasks.
- **Racing writes to a shared server-side ref.** `promote-prd.yml` already
  had to solve this for a single automated actor: it declares
  `concurrency: { group: promote-prd, cancel-in-progress: false }` so two
  triggered runs can't race to force-push the same promotion branch.
  Multi-agent orchestration multiplies this risk across N concurrent actors
  instead of duplicate runs of one workflow. Any future orchestration
  workflow that opens or updates PRs against a shared branch needs the same
  `concurrency:` group discipline, scoped per target ref rather than per
  workflow, so that agent A opening a PR against `main` can never race
  agent B doing the same against the same ref.

Phase 4 ("Concurrency/orchestration at scale") is where the queueing and
cap design belongs; this ADR's contribution is naming the two failure modes
already observed and the two mechanisms (worktree exclusivity,
`concurrency:` groups on shared refs) that already work elsewhere in this
org, so Phase 4 starts from proven primitives instead of a blank page.

## Consequences

**Accepted cost.** A GitHub App per repo (or a shared App installed on all
four) is a new piece of infrastructure to register, credential, and rotate
— `RELEASE_APP_ID`/`RELEASE_APP_PRIVATE_KEY`-equivalent secrets per repo,
managed the same way `deployments` already manages them.

**Not addressed here.** Which specific App topology (one shared App vs. one
per agent class) ships, the exact CODEOWNERS diffs per repo, and the
technical control that prevents an agent from authoring under
`jdwillmsen`'s own credentials are all Phase 2 design work, not decided by
this ADR.

**Deliberately unchanged.** The `OrganizationAdmin` break-glass bypass; the
requirement that `terraform apply` is never run autonomously; the general
shape of `required_status_checks` (signatures, scan, gitleaks) already
enforced on every repo's `Baseline`.

## Revisit

If Phase 2 finds that a single shared App identity is insufficient for
per-agent observability (the epic's third success metric — N concurrent
actors without collision — implies attribution matters, not just
non-collision), revisit the "one App vs. one per agent class" question
before it's load-bearing across all four repos; retrofitting App topology
after review-gate rollout is more expensive than deciding it once up front.
