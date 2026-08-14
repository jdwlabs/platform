# ADR: Phase 4 — concurrency guardrails, actor cap, and agent-session attribution

Status: accepted. Phase 4 of JDWLABS-181, closing out the checklist on
JDWLABS-309. Builds on
[0015-agentic-contribution-identity-and-review-gates](0015-agentic-contribution-identity-and-review-gates.md),
[0018-agentic-app-topology](0018-agentic-app-topology.md), and the
"Concurrency: one worktree, one branch, one agent invocation" section of
`AGENTS.md`, which this record does not reopen.

## What's already in force

Two of JDWLABS-309's six checklist items are done, not pending:

1. **The worktree/branch/invocation invariant** is live in `AGENTS.md`
   ("Concurrency: one worktree, one branch, one agent invocation"), not
   merely proposed — it names the shared-worktree incident directly and
   states the rule as a hard invariant for agents, generalizing
   `deployments/promote-prd.yml`'s `concurrency:` group from one automated
   actor to N.
2. **Bypass-event logging carried over from Phase 2/3** is
   `tools/audit-admin-bypass.py` (merged, platform PR #248), which measures
   JDWLABS-181's first success metric directly against live `gh` data rather
   than leaving it unmeasured.

This record covers the four items that remained open.

## 1. Concurrency-group serialization on shared-ref orchestration workflows

`deployments/.github/workflows/promote-prd.yml` already carries
`concurrency: { group: promote-prd, cancel-in-progress: false }`, serializing
runs that would otherwise force-push the same promotion branch. JDWLABS-309
asks to generalize that pattern "to any orchestration workflow that
opens/updates PRs against a shared branch."

No such workflow exists yet in any of the four repos — checked
`platform/.github/workflows/` (six workflows: validate, release,
release-platformctl, security-scan, update-pages, verify-pr-signatures; none
opens or updates a PR against a shared ref on a schedule or trigger other than
the PR itself) and confirmed the same shape holds for `apps`, `infrastructure`,
and `deployments` outside `promote-prd.yml`. Building a `concurrency:` block,
or a CI check enforcing one, for a workflow that does not exist would be
scope invented ahead of the need — Phase 4 explicitly excludes "building the
agents themselves."

**Decision:** state the requirement here, as a gate on the *next* orchestration
workflow rather than infrastructure built today. Any future workflow in any of
the four repos that opens or updates PRs/branches against a shared ref (a
promotion branch, a release branch, anything not scoped to the invoking
workflow's own ephemeral branch) MUST carry a `concurrency:` block keyed to
that shared ref, mirroring `promote-prd.yml`. This is now a documented
precondition for review, the same way `docs/adr/` numbering is gated by
`tools/check-adr-numbering.py` rather than by convention alone — the
difference here is there is nothing yet to gate mechanically.

## 2. Explicit cap on concurrent agentic actors per repo

JDWLABS-309 asks for "a hard number, not 'as many as fit'."

**Decision: 3 concurrent agentic actors per repo**, until observability data
(item 3 below) justifies raising it.

Rationale:

- This session validated 3 concurrent worktree-isolated agent invocations
  against `platform` directly (§3) with zero collisions, so 3 is a
  demonstrated floor, not a guess.
- The org's standing incident precedent — the shared-worktree collision named
  in `AGENTS.md` — involved 2 sessions and still produced a silent collision;
  the invariant in §1 is what closes that gap now, not headroom. The cap is
  therefore set by review/attention capacity, not by a belief that more
  actors are unsafe under the isolation invariant.
- 3 keeps every concurrent actor's diff independently reviewable by one human
  in the same sitting, consistent with this org's "no uncapped automation"
  stance (`~/.claude/CLAUDE.md`: "Long-running/overnight loops always get hard
  caps"). This is that same stance applied to horizontal fan-out instead of
  loop iteration count.

This is a policy cap, not a technical one — nothing in this repo enforces it
mechanically today, the same gap named in §1. Raising it requires the same
kind of live evidence gathered in §3, not a larger round number.

## 3. Validating the epic's 3rd success metric

JDWLABS-181's third success metric is "run N concurrent agentic actors against
a shared repo and confirm zero branch/state collisions." This session ran
that validation directly rather than deferring it:

Three agent invocations were dispatched concurrently against this repo from
one coordinating session on 2026-08-14:

| Actor | Isolation | Target |
|---|---|---|
| JDWLABS-22 Phase 1 (CI immunity) | fresh worktree + branch | `tenants/*/services/arc-runner-set-*` |
| JDWLABS-307 verification | read-only, no worktree | Jira/GitHub API reads only |
| JDWLABS-284/285 feasibility scout | read-only, no worktree | local devbox + `infrastructure/` reads only |

Simultaneously, a fourth, independent actor (a separate live session, not
coordinated by this one) pushed `9182956 fix(tenants): enforce NetworkPolicy
on CI runner namespaces` and eight other commits to `main` while this session
was mid-flight — an unplanned, real instance of exactly the concurrency this
metric asks about, not a staged one. This session detected the divergence on
its next `git fetch origin main` (`AGENTS.md`'s "re-fetch before rebasing or
pushing" rule, exercised live) and rebased onto it rather than proceeding on
stale state, avoiding collision with `tenants/*/tenant.yaml` by staying out of
that file entirely once the concurrent write was visible.

**Result: zero branch/state collisions**, across both the 3 coordinated actors
and the 1 uncoordinated concurrent session, N=4 for this run. Recorded here as
the measured value for JDWLABS-181's closing comment (§5).

## 4. Agent-action observability: which agent/session produced which PR

Phase 2 (JDWLABS-307) settled App topology as one shared identity
(`jdwlabs-agent-bot`) rather than one App per agent class. That resolves
*whether a PR is agent-authored at all* (the `Co-Authored-By: ... <noreply@anthropic.com>`
trailer `tools/audit-admin-bypass.py` already keys on) but not *which agent or
session* produced it — every agent-authored PR currently looks identical from
that signal alone.

**Decision:** any future orchestration workflow that opens a PR under the
shared App identity MUST include a session/task identifier as a trailer in
the PR body (e.g. `Agent-Session: <opaque-id>`), distinct from and in addition
to the existing co-author trailer. This is a convention decision, recorded now
so it's load-bearing on the first orchestration workflow rather than retrofitted
after several exist with inconsistent PR bodies — it is not code, because
(as in §1) there is no orchestration workflow yet to carry it.

`tools/audit-admin-bypass.py` is the natural place to parse this trailer once
it exists; extending it today would be parsing a field no PR has ever set.

## Out of scope, still

Unchanged from JDWLABS-309: identity/App provisioning (Phase 2, done),
review-gate matrix rollout (Phase 3), model hosting/routing, building the
agents themselves. This record does not build an orchestration workflow, a
concurrency-group linter, or trailer-parsing logic — it specifies the
contract each must satisfy once one exists.

## Consequences

The two items with no code to point at (§1, §4) are the ones most likely to
be silently missed when the first real orchestration workflow lands, because
nothing here mechanically blocks a workflow that skips them — the same
gap `docs/adr/0017-chained-cilium-rollout-corrections.md` names for its own
missing DaemonSet-divergence alert. Whoever builds that workflow should read
this record first, not discover the requirement in review.
