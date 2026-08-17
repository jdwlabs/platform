# ADR: Review gates by change class, and the bypass that made the old gate ornamental

Status: proposed. Implements Phase 3 of JDWLABS-181 and the review-gate
matrix sketched in
[agentic-contribution-identity-and-review-gates](0015-agentic-contribution-identity-and-review-gates.md).
Deviates from that record in three places, each recorded below. Acceptance
is conditional on one fact this session could not establish — see
"The reviewer this depends on".

## The gate that already exists does not hold

`platform`'s `Baseline` ruleset has required one approving review since
2026-06-13. Every recently merged PR on `main` cleared it the same way:

| PR | Author | `reviewDecision` at merge | Approving reviews |
|---|---|---|---|
| #273 | `jdwillmsen` | `REVIEW_REQUIRED` | none |
| #269 | `jdwillmsen` | `REVIEW_REQUIRED` | none |
| #268 | `jdwillmsen` | `REVIEW_REQUIRED` | none |
| #267 | `jdwillmsen` | `REVIEW_REQUIRED` | none |
| #262 | `app/renovate` | `REVIEW_REQUIRED` | none |
| #261 | `jdwillmsen` | `REVIEW_REQUIRED` | none |

All six merged by `jdwillmsen` through the `OrganizationAdmin` bypass that
every ruleset in every one of the four repos carries at
`bypass_mode: always`. ADR 0015 framed admin bypass as a two-time incident
(#112, #63). It is not: it is how essentially every change reaches `main`.
#262 is the sharpest datapoint — a Renovate-authored PR, with no
self-approval wall to hit and an approval available for the asking, merged
unapproved anyway. The bypass is not a break-glass here, it is the
default path.

That has a direct consequence for this ticket. Adding a stricter review
requirement on top of a bypass that is exercised on every merge produces
no enforcement at all. This repo has already shipped exactly that mistake
once: `required_signatures` sat on `main` until `5701d6d` removed it,
having "rejected nothing", surviving only because the bypass made its
unsatisfiability invisible. A second ornamental gate is not worth the
diff.

So the load-bearing decision in this record is not the taxonomy. It is
that the new gate carries **no bypass actors at all**.

## What actually differentiates risk here

Churn and risk are different axes, and this org has the history to prove
it. `deployments/charts/*/Chart.yaml` is the most-changed file set in the
org — 60, 59, 44, 44 commits apiece over 300 — and has caused zero
incidents, because those changes are machine-generated and digest-pinned.
Meanwhile `platform`'s outages cluster in a handful of files that change
far less often. The taxonomy below follows the incidents, not the churn.

**Class 1 — Cluster networking and tenant isolation.** The only true
cluster-wide outage this org has had. `500cebb` disabled the chained
Cilium rollout after "Service/ClusterIP traffic broke cluster-wide within
roughly two hours — CoreDNS unreachable via its own ClusterIP, cascading
to every ExternalSecret and several application pods". The re-enable
commit `c4301a2` names three separate bugs in one values file. Four ADRs
(0012, 0013, 0017, 0019) exist to record this surface's failure modes.
This class is categorically worse than everything below it because a
networking fault takes out the observability that would diagnose it and
the GitOps loop that would revert it. `helm-charts/tenant-envelope/`
belongs here too: `templates/network-policies.yaml` defines isolation for
every tenant namespace at once, and the per-tenant enforce switches in
`tenants/*/tenant.yaml` were already turned on and rolled back
(`1758e25` then `32635f7`).

**Class 2 — Gate integrity and supply chain.** The files that decide
whether any other gate means anything. `.github/rulesets/` and this
repo's CODEOWNERS define the gates; `8e9ed87` widened a bot's bypass and
`5701d6d` deleted an inert rule, both landing through the gates they
change. `.github/workflows/` mints GitHub App tokens — `audit-admin-bypass.yml`
mints one scoped across all four repos, and per ADR 0020 the App is
installed org-wide, so a workflow that omits its `repositories:` input
mints an org-wide credential silently. `tools/` holds the scripts CI
trusts to fail: `check-image-pins.py`, `check-adr-numbering.py`, and the
image pin allowlist. Editing them turns checks green without touching
what they check.

**Class 3 — GitOps root of trust.** `bootstrap/` changed three times in
300 commits, which is precisely why it is easy to under-weight.
`root-app.yaml` is the apex of the App-of-Apps tree; a bad change there
breaks the mechanism that would deploy its own fix.

**Class 4 — Platform-tenant services.** Secret material, storage, and the
cluster's own control plane. `tenants/platform/tenant.yaml` is the single
most-changed file in the repo (33 commits) and is the enable/disable and
syncWave toggle behind the CSI revert (`4166aba`, 181 deletions after
five node pods stuck in `ContainerCreating`), the monitoring schema
revert (`4e32a79`), and the memory trims set below documented live usage
(`0a80772`, `5c28a84`). The secret surfaces sit here too, and this org has
already lost an age key with no recovery path (`bc0a4e2`) and committed a
live Proxmox token (`0687e81`). `holmes/postInstall/externalsecret-talos.yaml`
hands Talos credentials to an AI agent.

Owned wholesale rather than service by service, deliberately: a service
added under this path later is gated by default. The alternative — an
explicit list of the dangerous services — fails silently the first time
someone adds a dangerous one and forgets the CODEOWNERS line, which is
the exact failure mode a malformed or under-broad CODEOWNERS produces.

**Class 5 — Agent-facing interface.** `cli/` is what agents drive the
platform through. Its own quality gates have been silently inert before:
`d0c0519` and `dd4f007` record that the Go linter could not start at all,
hiding 13 findings including a latent panic in the log encoder.

**Class 6 — Routine app-tenant config.** The application tenants
(`tenants/jdwlabs/`, `tenants/dotablaze-tech/`, `tenants/jdwillmsen/`
below their `tenant.yaml`) and the non-envelope charts. Real but bounded:
failures are per-service and recoverable by the same GitOps loop that
caused them.

**Class 7 — Docs and dashboards.** `docs/` is the third-highest-churn
directory in the repo (35 commits) with no runtime effect at all, along
with `observability/dashboards/`.

Classes 1 through 5 are gated. Classes 6 and 7 are not.

## The mechanism, and the limit it imposes

GitHub rulesets condition on refs, not paths. `require_code_owner_review`
is a repo-wide boolean on the `pull_request` rule with no path scoping of
its own — all path scoping comes from CODEOWNERS. This is the shape
`deployments` already runs: a `PRD Promotion Review Gate` ruleset with
`require_code_owner_review: true` layered under a `Baseline`, path-scoped
entirely by a CODEOWNERS file that deliberately carries no catch-all.

The consequence is that the enforceable tiering is **binary** — a path is
owned or it is not — regardless of how many classes the taxonomy names.
Classes 1 through 5 therefore resolve to the same enforcement today. The
finer grain is not decoration: it is the ownership map that assigns
different reviewers per class as more reviewers exist, and it is the
argument for which paths belong in the owned set at all. Anyone reading
this expecting five distinct enforcement levels will not find them, and
GitHub cannot currently express them.

Two further consequences worth stating plainly:

`platform`'s CODEOWNERS carried `* @jdwillmsen` as a catch-all. Under
`require_code_owner_review: false` that entry enforced nothing — it only
auto-requested a reviewer. Turning the flag on with the catch-all still
present would have gated every path in the repo on one person who authors
almost every PR, which is a blanket gate, not a class-based one, and a
guaranteed deadlock. The catch-all is removed for that reason. No merge
protection is lost: `Baseline`'s one-approval floor applies to every path,
owned or not, and is untouched.

A GitHub App cannot appear in CODEOWNERS. `jdwlabs-agent-bot` can satisfy
an approval count but can never satisfy a code-owner gate. The agent
identity is an author here, never an approver — consistent with ADR 0018
§3 and ADR 0021, which both declined to give it a bypass entry.

## Decision

Add a `Change Class Review Gate` ruleset to `platform`, modelled on
`deployments`' PRD gate: `require_code_owner_review: true`,
`required_approving_review_count: 0`, scoped to `refs/heads/main`.
Rulesets layer and the strictest wins, so a count of zero here adds the
owner requirement without disturbing `Baseline`'s floor of one.

Its `bypass_actors` list is **empty**. `Baseline`'s and `Production
Gates`' `OrganizationAdmin` entries are left exactly as they are — ADR
0015 listed the break-glass as deliberately unchanged, and this record
does not touch it. What changes is that the break-glass on `Baseline` no
longer silently covers the classes above, because those are gated by a
different ruleset that does not grant it.

This is not tamper-proof and should not be described as such. An
organization admin can still edit or delete the ruleset. The property it
buys is that clearing the gate stops being a merge-time checkbox and
becomes a deliberate, separately-logged configuration change — the
difference between the six PRs in the table above and an act someone has
to decide to commit.

Break-glass for a genuine outage is therefore: disable the ruleset, merge,
re-enable, and expect the audit to show it. That is intentionally more
friction than `--admin`.

## The reviewer this depends on

A gate nobody can satisfy is worse than no gate, so: who can approve each
gated class today?

The org has exactly two human accounts, both org admins with push on
`platform`: `jdwillmsen` and `jdwlabs-root`. Both are listed as owners on
every gated path, so every gated class has the same answer, and the answer
depends on who authored the change:

- **Authored by `jdwlabs-agent-bot`** — either human approves. This works
  today and is already proven: PRs #244 and #248 were opened under the App
  identity and merged.
- **Authored by `jdwillmsen`** — only `jdwlabs-root` can approve.
- **Authored by `jdwlabs-root`** — only `jdwillmsen` can approve.

The second and third cases are where this record's acceptance hangs.
`jdwlabs-root` exists, is an active org admin, and has submitted exactly
one review in its lifetime (`platform` #5). Whether it is an account Jake
can actually sign into and review from, or a dormant org-owner account
nobody holds working credentials for, was not established here and cannot
be established by inspection.

If it is usable, this gate holds for every authorship case and Phase 3's
"author != approver" goal is met natively for the gated classes.

If it is not usable, then every Class 1–5 change authored by `jdwillmsen`
deadlocks, and the only exits are routing that change through the agent
identity or disabling the ruleset — which is why the ruleset is shipped as
a file and not applied. Applying it is a manual step, and it should not be
taken until `jdwlabs-root` has approved one real PR.

The honest reading of that second branch: the gate would not be broken so
much as it would be telling the truth. `jdwillmsen` authoring 92.5% of
merged PRs with no second reviewer is the condition that made the bypass
routine in the first place, and no ruleset shape fixes it. What fixes it
is either a usable second reviewer or agent authorship becoming the norm
for gated paths.

## Deviations from ADR 0015

**Docs-only does not get a zero-review tier.** ADR 0015's matrix put
docs-only at "0 human review; automated checks only". Implementing that
requires dropping `Baseline` to
`required_approving_review_count: 0` and re-adding the floor through a
second ruleset, because the count is repo-wide and cannot be scoped by
path. That lowers the floor for every unowned path in the repo, which is a
real weakening bought for a convenience. Docs stay at one approval.

**Five classes, two enforcement levels.** ADR 0015 read as though its five
rows were five gates. They cannot be, for the reason in "The mechanism"
above. The rows survive as an ownership map; the enforcement is binary.

**A sixth class the matrix did not have.** ADR 0015 swept cluster
networking into "Routine / non-prod config" at one non-owner approval.
That is the class that produced the only cluster-wide outage. It is now
Class 1.

## Rollout

`platform` first, as the pilot: it is the repo with the incident history,
the ADR context, and the tooling. The other three repos follow the same
two-file shape (a CODEOWNERS restructure plus one additive ruleset),
sequenced behind confirmation that the reviewer identity works.

The audit tooling had a matching blind spot, fixed alongside this:
`tools/audit-admin-bypass.py` scored a ruleset by its
`required_approving_review_count` alone, so an owner-gated ruleset with a
count of zero — the shape both `deployments`' PRD gate and this one use —
read as requiring nothing, and every path it protects was silently exempt
from the bypass audit. Shipping this gate without that fix would have made
it unmeasurable, and Phase 3 is supposed to verify the gate holds.

## Revisit

When a third reviewer identity exists, revisit whether Classes 1 through 5
should carry different owners rather than the same pair. The taxonomy is
written to make that a CODEOWNERS edit rather than a redesign.

If GitHub gains path-scoped ruleset conditions, revisit the binary
enforcement limit — the class boundaries in this record are the ones the
evidence supports, and would become directly expressible.
