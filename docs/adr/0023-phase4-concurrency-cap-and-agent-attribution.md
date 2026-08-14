# ADR: Phase 4 — concurrency guardrails, actor cap, and agent-session attribution

Status: accepted. Phase 4 of JDWLABS-181, closing out the checklist on
JDWLABS-309. Builds on
[0015-agentic-contribution-identity-and-review-gates](0015-agentic-contribution-identity-and-review-gates.md),
[0018-agentic-app-topology](0018-agentic-app-topology.md), and the
"Concurrency: one worktree, one branch, one agent invocation" section of
`AGENTS.md`, which this record does not reopen.

## What's already in force

One of JDWLABS-309's six checklist items is closed outright, and half of a
second is:

1. **The worktree/branch/invocation invariant** (item 1) is live in
   `AGENTS.md` ("Concurrency: one worktree, one branch, one agent
   invocation"), not merely proposed — it names the shared-worktree incident
   directly and states the rule as a hard invariant for agents, generalizing
   `deployments/.github/workflows/promote-prd.yml`'s `concurrency:` group
   from one automated actor to N.
2. **Bypass-event logging** — the second half of item 4, "carried over from
   Phase 2/3" — is `tools/audit-admin-bypass.py` (merged, platform PR #248),
   which measures JDWLABS-181's first success metric directly against live
   `gh` data rather than leaving it unmeasured.

This record covers items 2, 3, 4 (its unfinished attribution half), and 5.
Item 6 — recording each success metric's measured value in a closing comment
on the epic, JDWLABS-181 — is **not** closed by this record: it needs
Phase 2's remaining gaps landed as well, and is posted on the epic itself,
not here. §3 supplies the measured value that item 6 will cite for the
epic's third metric.

## 1. Concurrency-group serialization on shared-ref orchestration workflows

JDWLABS-309 asks to generalize `promote-prd.yml`'s pattern "to any
orchestration workflow that opens/updates PRs against a shared branch."

### The precedent is wider than one workflow

`deployments/.github/workflows/promote-prd.yml` carries
`concurrency: { group: promote-prd, cancel-in-progress: false }`, serializing
runs that would otherwise race on the same promotion branch —
`chore/promote-<app>-prd`, which is reused across runs and updated in place
rather than recreated per run.

It is not the only precedent, and an earlier draft of this record was wrong
to present it as one:

- `deployments/.github/workflows/release.yml` and
  `deployments/.github/workflows/update-pages.yml` share
  `concurrency: { group: gh-pages-write, cancel-in-progress: false }` — both
  commit and `git push` to the shared `gh-pages` ref. Their inline comments
  document the exact race class this record reasons about, including the
  mechanism's own failure mode, which §2 now has to reconcile against.
- `apps/.github/workflows/ci.yml` serializes its release job with
  `group: release-main` for the same reason one level up: "nx release must
  never tag a moved main tip."

### What already exists, and the one gap

By the criterion stated in the decision below — writing a ref that is not
scoped to the invoking run's own ephemeral branch — four workflows already
qualify. Three are covered; one is not:

| Workflow | Shared ref written | `concurrency:` |
|---|---|---|
| `deployments/.github/workflows/promote-prd.yml` | `chore/promote-<app>-prd`, then a PR onto `main` | `promote-prd` |
| `deployments/.github/workflows/release.yml` | `gh-pages` | `gh-pages-write` |
| `deployments/.github/workflows/update-pages.yml` | `gh-pages` | `gh-pages-write` |
| `platform/.github/workflows/update-pages.yml` | `gh-pages` | **none** |

`platform/.github/workflows/update-pages.yml` checks out `gh-pages`, commits
`index.html`, and pushes it with no `concurrency:` block at all — the same
unguarded write its `deployments` twin was given a group to serialize. That
gap is real and is tracked as follow-up work; it is deliberately not fixed
here, because this record is docs-only and a workflow change belongs in its
own reviewable PR.

What does *not* exist in any of the four repos is an **agent-orchestration**
workflow — one that opens or updates PRs on behalf of concurrent agentic
actors, which is what this checklist item is ultimately aimed at.
`promote-prd.yml` opens PRs, but as a single automated promoter serialized
into one slot, not as N agents. So there is no orchestration workflow to
attach a `concurrency:` block to today; there is, however, an existing
workflow already in breach of the rule.

**Decision:** the requirement is standing, not forward-looking only. Any
workflow in any of the four repos — existing or future — that opens or
updates PRs/branches against a shared ref (a promotion branch, a release
branch, `gh-pages`, anything not scoped to the invoking workflow's own
ephemeral branch) MUST carry a `concurrency:` block keyed to that shared ref,
mirroring `promote-prd.yml` and `gh-pages-write`. Adopters must also read the
two obligations in §2 that come with `cancel-in-progress: false`.

This is a documented precondition for review, the same way `docs/adr/`
numbering is gated by `tools/check-adr-numbering.py` rather than by
convention alone. Unlike that gate it is not mechanised — and unlike the
earlier framing of this section, that is not because there is nothing to
gate: a linter over `.github/workflows/` would have caught `platform`'s
missing group. Building one is follow-up work, not scope invented ahead of
the need.

## 2. Explicit cap on concurrent agentic actors per repo

JDWLABS-309 asks for "a hard number, not 'as many as fit'."

**Decision: 3 concurrent agentic actors per repo**, until the observability
work in §4 produces data justifying raising it.

Rationale:

- This session ran 4 concurrent actors against `platform` with zero
  collisions (§3), 3 of which wrote to the repo's refs, 2 of those raising
  PRs on distinct branches within the same minute. That makes 3 a
  demonstrated floor rather than a guess — but read §3's breakdown before
  quoting it as anything stronger; one of the three writers was an
  uncoordinated session, not a dispatched actor.
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

### Why 3 here is not the 3 that breaks `cancel-in-progress: false`

§1 tells workflows to mirror `promote-prd.yml` and `gh-pages-write`, and that
mechanism has a documented failure at exactly three
(`deployments/.github/workflows/update-pages.yml`): `cancel-in-progress:
false` "holds one pending run per group, not an unbounded queue. A 3rd
concurrent entrant … cancels the previously-pending run instead of queuing
behind it, and a `cancelled` conclusion reads as success to anything matching
only on `failure`." Picking 3 as the actor cap is not that number
reappearing, and the two do not share a queue:

- The cap governs concurrent **agent actors** — sessions, each holding its
  own worktree and its own branch. An Actions concurrency group serializes
  **runs within one group against one shared ref**. Three actors do not
  imply three entrants in any one group: they normally target three
  different refs, and conversely a single actor can put several runs into
  one group by itself. The resources are unrelated, so the Actions limit is
  not a reason to lower the cap to 2, and raising the cap later neither
  causes nor worsens the queuing loss.

Two obligations follow for anything adopting §1's pattern, and they hold at
any cap:

- A workflow MUST NOT rely on `cancel-in-progress: false` to queue a third
  entrant. It holds one pending run; a third displaces the second, silently.
  Work that must not be dropped needs an idempotent re-run path, or a
  retry/rebase on the push itself — not a deeper queue, which Actions does
  not offer.
- Anything gating on such a run MUST treat a `cancelled` conclusion as
  not-success. A condition matching only on `failure` reads a cancellation
  as success, which is how a dropped run becomes a green board.

## 3. Validating the epic's 3rd success metric

JDWLABS-181's third success metric reads ">= N concurrent agentic actors
raise PRs without branch/state collision"; JDWLABS-309's checklist item 5
phrases the validation as "run N concurrent agentic actors against a shared
repo and confirm zero branch/state collisions." This session ran that
validation directly rather than deferring it.

Three agent invocations were dispatched concurrently against this repo from
one coordinating session on 2026-08-14:

| Actor | Isolation | Target |
|---|---|---|
| JDWLABS-22 Phase 1 (CI immunity) | fresh worktree + branch | `tenants/*/services/arc-runner-set-*` |
| JDWLABS-307 verification | read-only, no worktree | Jira/GitHub API reads only |
| JDWLABS-284/285 feasibility scout | read-only, no worktree | local devbox + `infrastructure/` reads only |

Simultaneously, a fourth, independent actor (a separate live session, not
coordinated by this one) landed `9182956 fix(tenants): enforce NetworkPolicy
on CI runner namespaces` and eight other commits on `main` — via merged PRs,
not direct pushes — while this session was mid-flight. That is an unplanned,
real instance of exactly the concurrency this metric asks about, not a staged
one. This session detected the divergence on its next `git fetch origin main`
(`AGENTS.md`'s "re-fetch before rebasing or pushing" rule, exercised live) and
rebased onto it rather than proceeding on stale state, avoiding collision with
`tenants/*/tenant.yaml` by staying out of that file entirely once the
concurrent write was visible.

**Result: zero branch/state collisions**, across both the 3 coordinated actors
and the 1 uncoordinated concurrent session, N=4 for this run. Recorded here as
the measured value item 6 will cite in JDWLABS-181's closing comment.

### What this run does and does not evidence

Stated precisely, so the epic's closing comment does not overclaim:

- **Evidenced from the repo:** two agent actors raised PRs against `platform`
  within seconds of each other on distinct branches — #253
  (`feat/JDWLABS-309-phase4-closeout`) and #254
  (`fix/JDWLABS-22-arc-runner-work-emptydir`), both opened 2026-08-14 05:47–05:48Z
  — while a third, uncoordinated session merged #249–#251 onto `main` across
  the same window. No branch was contended and no rebase conflicted. This is
  the epic's metric in its literal "raise PRs" form, at N=3 writers.
- **Softer than N=4:** of the three coordinated invocations, only the
  JDWLABS-22 one held a worktree and a branch. The other two were read-only
  and could not have collided by construction, so they count toward
  concurrent *actors* but not toward concurrent *writers*. Counting writers
  to this repo's refs gives 3 — the coordinating session (#253), the
  JDWLABS-22 invocation (#254), and the uncoordinated session (#249–#251) —
  inside a window of N=4 concurrent actors.
- **Not evidenced here:** that 3 remains collision-free under sustained load,
  or against workflows sharing one `concurrency:` group (§2's second
  obligation is unexercised — no `cancelled` conclusion occurred in this
  window). The cap in §2 rests on review capacity, not on this run alone.

## 4. Agent-action observability: which agent/session produced which PR

Phase 2 (JDWLABS-307) settled App topology as one shared identity
(`jdwlabs-agent-bot`) rather than one App per agent class. ADR 0018 also
already answered *which* agent produced a commit, and this record does not
reopen that: the `Co-Authored-By: <agent name> <noreply@anthropic.com>`
trailer carries the agent's name and model (`Claude Fable 5
<noreply@anthropic.com>` on platform PR #112's own commit), plus a concrete
session or task identifier when useful — which 0018 calls "finer-grained than
a per-class App identity could ever get."

The gap is **tooling, not signal**. `tools/audit-admin-bypass.py` matches the
trailer with `Co-Authored-By:.*<[^>]*@anthropic\.com>`; the `.*` discards the
name, so the tool answers only *is this PR agent-authored*, never *which
agent*. The identity is on the commit already and nothing parses it. An
earlier draft of this section claimed every agent-authored PR "looks
identical from that signal alone" — that describes the audit tool's regex,
not the trailer.

**Decision:** any future orchestration workflow that opens a PR under the
shared App identity MUST include a session/task identifier as a trailer in
the PR body (e.g. `Agent-Session: <opaque-id>`), distinct from and in addition
to the existing co-author trailer. This is a convention decision, recorded now
so it's load-bearing on the first orchestration workflow rather than retrofitted
after several exist with inconsistent PR bodies — it is not code, because
(as in §1) there is no orchestration workflow yet to carry it.

`Agent-Session:` **complements 0018's commit trailer; it does not supersede
it.** They answer different questions at different granularities, and both
stay:

- The commit trailer stays the record of which agent and model wrote a given
  commit. A PR can hold commits from more than one agent, and 0018's answer
  is what keeps that legible.
- The PR-body trailer names the owning orchestration session for the PR as a
  whole. That is the unit a review gate, an actor cap (§2), and a
  collision post-mortem (§3) actually operate on, and no per-commit trailer
  supplies it.

`tools/audit-admin-bypass.py` is the natural place to parse both — the
`Agent-Session:` trailer once one exists, and the agent name its current
regex already throws away. Neither is done here: this record is docs-only.

## Out of scope, still

Unchanged from JDWLABS-309: identity/App provisioning (Phase 2, done),
review-gate matrix rollout (Phase 3), model hosting/routing, building the
agents themselves. This record does not build an orchestration workflow, a
concurrency-group linter, or trailer-parsing logic — it specifies the
contract each must satisfy once one exists.

Also deliberately out of scope here, and carried as follow-up work rather
than folded into a docs record:

- adding the missing `concurrency:` group to
  `platform/.github/workflows/update-pages.yml` (§1);
- a linter over `.github/workflows/` that enforces §1's rule mechanically;
- teaching `tools/audit-admin-bypass.py` to keep the agent name its regex
  currently discards, and to parse `Agent-Session:` once one is set (§4).

## Consequences

The two items with no code to point at (§1, §4) are the ones most likely to
be silently missed when the first real orchestration workflow lands, because
nothing here mechanically blocks a workflow that skips them — the same
gap `docs/adr/0017-chained-cilium-rollout-corrections.md` names for its own
missing DaemonSet-divergence alert. `platform`'s unguarded `gh-pages` push is
the proof that this is not hypothetical: the rule §1 states was already
violated before the rule was written down, in a repo where three sibling
workflows get it right. Whoever builds the first orchestration workflow
should read this record first, not discover the requirement in review.
