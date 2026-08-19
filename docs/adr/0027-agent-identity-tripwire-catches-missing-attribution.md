# ADR: the agent-identity tripwire catches missing attribution, not attributed agent work

Status: accepted. Supersedes the failure condition specified in §3 of
[agentic-app-topology](0018-agentic-app-topology.md); extends
[agent-identity-verified-via-test-pr](0021-agent-identity-verified-via-test-pr.md)
and §4 of
[phase4-concurrency-cap-and-agent-attribution](0023-phase4-concurrency-cap-and-agent-attribution.md).
0018 is not edited — records here are append-only, so the correction is
written forward, per the same convention
[agent-app-installed-org-wide](0020-agent-app-installed-org-wide.md) used.

Closes JDWLABS-368. The check this record re-scopes ships in
`.github/workflows/agent-identity.yml` in all four repos.

## 1. Two sections of 0018 disagree, and the implementation followed the wrong one

0018 §2 ("Attribution doesn't require separate App identities") settles
attribution on the commit trailer, and points at a **human-authored** PR as
the working example: *"agent-authored commits already carry a
`Co-Authored-By: <agent name> <noreply@anthropic.com>` trailer — visible on
`platform` PR #112's own commit ... alongside `jdwillmsen`'s human
authorship."* That is the mechanism the org runs on today and the one 0023
§4 builds its "which agent produced this commit" answer on.

0018 §3 then defines the identical shape as the violation fingerprint: *"a
commit with an agent co-author trailer, opened under a non-agent identity."*
Both cannot be true. §2 says a trailer on a human-opened PR is the
attribution working as designed; §3 says it is the failure being hunted.

§3 is the mis-scoped half, and the reason is visible in its own text: it
reasons entirely from PR #112, where the actual defect was an agent-produced
change **merged unreviewed through an `--admin` bypass**. The trailer was
merely the only machine-readable trace that anything agentic had happened, so
§3 promoted the trace into the offence. The offence in #112 was the missing
review gate — which 0015's `required_approving_review_count` and 0026's
review-gate matrix address directly, and which this check never touched.

## 2. Why the mis-scope was actively harmful, not merely noisy

Every agent-assisted PR opened under a human identity trips the check as
written, so its signal is not weak — it is inverted. JDWLABS-368 records
`platform` PRs #268 and #269 as instances: all content checks green,
`co-author-check` red, nothing wrong with either change.

Both contribution paths are live in this org, and the mis-scope only damages
one of them:

- **The App path works and is in routine use.** An installation credential is
  held locally, outside CI, and is what opens `jdwlabs-agent-bot[bot]` pull
  requests today — eight were open across `platform`, `apps`, and
  `infrastructure` when this record was written. JDWLABS-368's premise that
  the App is reachable only from inside a workflow run is therefore wrong,
  and so is any reading of 0018 §4 that treats
  `actions/create-github-app-token` as the *only* way the identity can be
  obtained; that step is still unbuilt, and the identity is in use anyway.
- **The human path is not deprecated, and it is the one that broke.** 0018 §2
  endorses it explicitly, and it remains what an interactive session
  produces by default. For a PR opened that way, the check offered exactly
  one lever that turns it green: **delete the trailer** — the precise defeat
  §3 itself names (*"an agent instructed to can simply omit it, and the check
  has nothing to see"*).

A control whose only satisfiable path on a sanctioned workflow destroys the
evidence it exists to preserve is worse than no control, and it stayed that
way because the check is advisory: it is absent from every repo's `Baseline`
`required_status_checks` (verified live across all four repos), so nothing
blocked and nobody was forced to resolve it.

## 3. Decision: keep the trailer, invert the condition

**The trailer stays exactly as 0018 §2 and 0023 §4 define it, on every
agent-assisted commit, regardless of who opens the pull request.** It is not
evidence of a violation and must never be stripped to satisfy CI.

The check's question changes from *"is there agent attribution under a human
identity?"* to *"is the agent identity present with its attribution
missing?"*. Precisely, `agent-identity / co-author-check` fails when, and
only when:

1. the registered agent identity (`jdwlabs-agent-bot`) appears as the GitHub
   or git author or committer of a commit that carries no agent co-author
   trailer; or
2. the pull request was opened by that identity and not one of its commits
   carries the trailer; or
3. the branch cannot be fully inspected — the API read fails, the reported
   branch length is not a number, the pull request has no commits, or fewer
   commits come back than the branch holds.

Everything else passes, including the shape that motivated this record: an
attributed, agent-assisted pull request opened by a human.

Clause 1 exempts commits GitHub itself created — a merge from the
update-branch button, a web-UI revert or edit. Those are committed as
`web-flow` and give nobody an opportunity to write a trailer, so flagging
them would rebuild the same unsatisfiable red this record exists to remove.

The exemption is keyed on **GitHub's own signature** over such a commit, and
deliberately not on the two weaker signals an earlier draft of this record
named. A committer login proves nothing on its own: GitHub resolves it from
the commit's committer email with no signature involved, so any local commit
setting `noreply@github.com` resolves to `web-flow` — a forgery this repo can
demonstrate, since several of its own unsigned commits resolve a login the
same way. Parent count proves even less: git imposes no relationship between
a merge commit's tree and its parents', so `git merge -s ours` ships an
arbitrary diff under two parents. GitHub signs every commit it creates, and a
forged one arrives unsigned, so the signature is the only part of that shape
an attacker cannot supply.

### Which clause is doing the work today

The two clauses are not equally live, and the distinction matters for anyone
reading a red check or deciding whether to require this one:

- **Clause 2 is the live clause.** It matches every `jdwlabs-agent-bot[bot]`
  pull request — all eight open at the time of writing — and they pass
  because every commit on them carries a trailer. It is what stops the App
  identity from being used with the attribution deleted.
- **Clause 1 is currently inert, by mechanism.** A `git push` over an
  installation token does not rewrite commit identity: all eight of those
  bot-opened PRs carry `jdwillmsen` in the GitHub author and committer logins
  and in all four git name/email fields. Only commits GitHub *creates* — the
  contents API, `createCommitOnBranch`, the web UI — get an
  `<app-slug>[bot]@users.noreply.github.com` committer. Clause 1 is forward
  cover for that path, not a control that fires on the current one.

Clause 2 is JDWLABS-368's third deliverable and closes a gap the old shape
left wide open: a PR authored by the App used to pass *unconditionally*, so a
bot PR with every trailer stripped went green while proving nothing. Identity
and attribution answer different questions (0023 §4: the App identity says a
change is agent-authored, only the trailer says *which* agent), and the check
now requires both wherever either appears.

## 4. What this still cannot catch

Unchanged from 0018 §3, and worth restating because the inversion does not
touch it: an agent working under a human identity that simply never writes
the trailer is invisible here. The trailer is unauthenticated, self-asserted
text; nothing in a commit distinguishes an unattributed agent-written change
from a human-written one. This remains a tripwire for the accidental case,
not enforcement — the enforcement is procedural, and the review gates in 0026
are what actually stand between an agent-produced change and `main`.

What the inversion does buy is that the tripwire's failure mode is no longer
self-defeating. Removing a trailer can no longer turn the check green: on the
App path clause 2 turns it red, and on the human path the check was already
silent, so deletion gains nothing.

## 5. Three implementation defects fixed alongside the re-scope

The shipped job diverged from its own description in three ways, each
independent of the scoping question:

- **The trailer scan was unanchored.** `Co-Authored-By:.*<noreply@anthropic\.com>`
  matched anywhere in the commit message, so a commit whose *body discusses*
  the trailer matched as if it carried one. These ADRs quote the literal
  string, which made editing this very file a live trigger. Now anchored to
  line start (`^Co-Authored-By:`), the shape a real git trailer has.
- **The address pattern was narrower than the org's other reader of the same
  trailer.** `tools/audit-admin-bypass.py` matches
  `Co-Authored-By:.*<[^>]*@anthropic\.com>`, which 0023 §4 quotes; the
  workflow hardcoded `<noreply@anthropic\.com>`, so any other
  `@anthropic.com` sender read as unattributed. The workflow now accepts the
  wider set — on its own merits, since the sender address is incidental to
  the claim being made, not because 0023 §4 is a specification: it describes
  the audit tool, and 0023's own canonical example of the trailer uses
  `<noreply@anthropic.com>`. **The two still differ in the other direction**:
  the audit tool's pattern remains unanchored and carries exactly the prose
  false positive fixed here. It over-reports agent authorship in a report
  rather than failing a gate, so it is left as follow-up rather than changed
  from a CI PR — but it is not fixed, and should not be read as if it were.
- **The branch-length guard compared a frozen count against a live read.**
  The 250-commit cap on the commits endpoint is real and the guard against it
  stays, but it took its expected length from `github.event.pull_request.commits`
  — frozen at trigger time — while counting a live API response. A re-run
  after a push therefore failed a fully-inspected branch with "refusing to
  report a pass over part of the branch." The count is now read live, before
  the commit list, so an ordinary push landing mid-run can only make the list
  longer than the count; only a *short* read, which is what truncation
  produces, is reported. A force-push that shortens the branch inside that
  window still fails closed, which is the safe direction.

## Consequences

**Clause 2 is enforceable now; promoting the check no longer waits on
JDWLABS-309.** An earlier draft of this record said the opposite, on the
mistaken belief that the App identity was not yet on the push path. It is:
eight bot-opened PRs were live when this was written, all passing clause 2.
Adding `agent-identity / co-author-check` to each repo's `Baseline`
`required_status_checks` — 0018 §3's draft diff, applied through each repo's
`.github/rulesets/apply.sh` — is therefore a decision that can be taken
today, and is deliberately left as its own reviewed change rather than folded
into a workflow PR. Until it is required, a red `co-author-check` means one
thing to a reviewer: agent identity is present on this branch and the
attribution naming the agent is missing. That is a real finding rather than
the default state of every agent-assisted PR.

**A prose mention can still register as attribution.** Anchoring stops a
mid-sentence quote, not a trailer quoted at line start inside a fenced block
in a commit body. Under the inverted condition that error can only mark a
commit *attributed*, never fail a pull request, so it costs a small amount of
strictness on the App path and cannot produce a false red. Parsing trailers
with `git interpret-trailers` instead of a line regex would close it, and
needs a checkout this job deliberately does not do.

**Nothing mechanically prevents two branches from claiming the same ADR
number.** `tools/check-adr-numbering.py` sees only the branch it runs on, and
`Baseline`'s `required_status_checks` is not strict, so two concurrent
branches can each add an `NNNN-` prefix and both merge. This record collided
with another in exactly that way while it was open, resolved by hand. Making
the gate cross-branch — comparing against `origin/main` plus open PRs — is
follow-up work, not done here.

**0018 §3 is now inaccurate about what the check does**, in the same way 0020
left §2 inaccurate about installation scope. Its analysis of what the control
*cannot* prove (§4 above) survives intact and is still the section to read
before relying on this check for anything.
