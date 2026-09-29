# ADR: An audited break-glass on the change-class review gate

Status: accepted, 2026-09-29. Supersedes one decision in
[review-gates-by-change-class](0026-review-gates-by-change-class.md): that
the `Change Class Review Gate` ruleset carries **no bypass actors at all**.
Everything else in that record stands: the class taxonomy, the CODEOWNERS
shape, and the choice to leave `Baseline`'s and `Production Gates'`
`OrganizationAdmin` entries alone.

## What changes

The gate's `bypass_actors` gains one entry, in the shape every other ruleset
in `.github/rulesets/` already uses for the org-admin role, with only the mode
changed:

```json
{ "actor_id": null, "actor_type": "OrganizationAdmin", "bypass_mode": "pull_request" }
```

At the same time, `tools/audit-admin-bypass.py` stops treating a merge past
this gate as routine. Any merged PR that touched a CODEOWNERS path without an
approval from one of that path's owners, other than its author, is
**reportable**. The daily `audit-admin-bypass` run then exits with the
`bypassed` outcome and sends a warning, whatever the PR's status checks said
and whether or not an agent wrote it. The only way to clear it is a hold in
`tools/admin-bypass-holds.yaml` that has a reason.

The two changes go together. The ruleset change without the audit change
would bring back the failure ADR 0026 was written to end.

## Why the no-bypass rule was too strong

ADR 0026 set the break-glass for a real outage as: disable the ruleset,
merge, re-enable. It left open whether `jdwlabs-root` was an account anyone
could sign in to. It is, and it has approved real PRs since. That makes the
gate satisfiable, but for a PR `jdwillmsen` writes, it is satisfiable only by
signing in as a second identity.

GitHub never counts a PR author's own review, so no review settings can let
the owner clear the gate on their own PR. The only ways through were:

- sign in as `jdwlabs-root` and approve, which makes a second login a
  mandatory step in fixing any outage whose fix touches a gated path; or
- disable the whole ruleset, which ungates every PR in flight for as long
  as it stays off.

On 2026-09-29 the owner decided the first is too strong a requirement for an
emergency. Gated paths include `tenants/platform/` and
`.github/workflows/`, which is where most outage fixes land. The second is
worse than a scoped bypass: it removes the gate for everyone, and nothing in
the ruleset history ties the disable to the PR it was meant for.

## Why `pull_request` mode

GitHub's REST API accepts three `bypass_mode` values for a ruleset actor:
`always`, `pull_request` and `exempt`. `pull_request` lets the actor bypass
the rules only when merging a pull request. Pushing straight to `main` is
still refused by the gate. Its `pull_request` rule covers all of `main`, not
only the owned paths, so while `Baseline`'s `always` bypass would let an admin
push directly, this gate would not. Every change therefore still reaches
`main` as a merged PR, with its changed files and its reviews. That record is
what the audit reads. `always` would also allow a direct push, and `exempt` would not
even create a bypass audit entry.

## Why this does not make the gate ornamental again

ADR 0026's diagnosis was that the `Baseline` bypass was ornamental because it
was **used on every merge and seen by nobody**. Using it cost nothing, so it
became the normal way to merge. There are two parts to that failure, and the
design deals with each:

- **Seen by nobody.** The audit reports every use. Unlike an unapproved but
  green merge elsewhere, a merge past this gate never counts as routine. It
  stays a finding until someone writes down why it happened.
- **Costs nothing.** Each use leaves a failing daily audit and an alert until
  a hold with a reason is committed. Committing a hold means a PR to
  `tools/`, which is itself a gated path. Doing that is small but deliberate,
  and doing it as a habit would show in the holds file.

The audit finds a bypass from what was merged, not from the fact that a
bypass happened. GitHub gives this org's plan no audit-log API. It matches
each PR's changed files against the live CODEOWNERS and checks each owned
file for an owner's approval that is still current. It does not use
`reviewDecision`, because `Baseline` needs one approval on every path, so
`REVIEW_REQUIRED` cannot say which of the two requirements was missed. A
merge that went past the gate while the ruleset was **disabled**, which was
ADR 0026's own break-glass, looks exactly the same and is reported the same
way.

The audit cannot see some things. Each gap is listed in the script's
docstring:

- CODEOWNERS and the rulesets are read as they are today, so a PR is judged
  against today's owned paths. PRs merged before the gate ruleset existed are
  skipped using its `created_at`.
- A rename is judged by its new path only.
- An owner written as a team or an email address cannot be matched to a
  reviewer's login. It gives no approval, so the audit reports the merge
  rather than missing it.

## When using it is legitimate

It is a break-glass, not a queue-jumper. Use it when:

- production, or the GitOps loop that would deploy the fix, is broken or
  getting worse; and
- the fix touches a gated path; and
- waiting for a second-identity approval would make the harm meaningfully
  worse.

Use by convenience, such as a slow review, a quiet evening or a routine
change, is not legitimate. The audit reports those uses exactly as it reports
legitimate ones, and a hold written to cover one is a record that says so.

## What each use owes

Each use gets **one** of these, soon after the merge. The next daily run is
the deadline in practice, because that is when the warning fires.

1. **A hold with a reason.** Add an entry to `tools/admin-bypass-holds.yaml`
   keyed on the repo and PR number. The reason names the emergency, and
   either says who reviewed the change afterwards or links the follow-up that
   will. "Emergency" on its own is not a reason.
2. **A follow-up.** If the change should not stand as merged, the
   follow-up PR reverts or corrects it. It goes through the gate normally,
   with an owner's approval, and the hold still records the original merge.

A hold is keyed on the PR number, so it cannot expire on its own. The audit
already reports a hold as stale if its PR falls inside the window and did not
need holding. Once the rolling window passes the PR, the entry does nothing
and can be deleted whenever convenient.

## Rollout

Rulesets are applied by hand after merge. The committed export now carries
the live ruleset's `id`, so `.github/rulesets/apply.sh` updates the ruleset
in place. Before this, the export had no `id`, and applying it would have
created a second copy. Merge the audit change before applying the ruleset or
together with it, never after: a bypass that exists while the audit cannot
yet see it is the ornamental state for as long as that gap lasts.

## Revisit

- If GitHub makes the org audit log readable on this plan, report bypass
  events directly. That removes the inference from paths and reviews and its
  blind spots.
- If holds for this gate become frequent, the legitimate-use bar above is not
  holding. Revisit the bypass itself, not the audit.
