# ADR: Agentic App topology, bypass-actor resolution, and the author-identity control

Status: proposed. Phase 2 of JDWLABS-181, resolving the two open questions
ADR 0015 flagged and left to this phase. Does not create the App, change any
live ruleset, or touch branch protection — those remain human-only actions
(see "Human runbook" below), executed after this record is reviewed.

References: `docs/adr/0015-agentic-contribution-identity-and-review-gates.md`
("Options considered — identity", "Bypass actors — a flagged hole, not a
fix", "Consequences — Not addressed here").

## 1. Bypass actor `4065387` — resolved

ADR 0015 flagged `deployments`' `PRD Promotion Review Gate` ruleset granting
`{"actor_type": "Integration", "actor_id": 4065387, "bypass_mode":
"pull_request"}` to an unconfirmed identity, naming Renovate as a candidate.

`gh api orgs/jdwlabs/installations` (org-level, admin-token, read-only) lists
every App installation in the org and resolves it directly:

```
app_id: 4065387, app_slug: "jdwlabs-release-bot", installation_id: 140587771
permissions: {"contents": "write", "metadata": "read", "pull_requests": "write"}
repository_selection: "all"
```

The bypass actor is `jdwlabs-release-bot` itself — not Renovate, not a
third-party integration. This matches the repo's own history: `deployments`
PR #110 ("let the release bot merge its own appVersion bumps") is the change
that dropped `required_approving_review_count` to 0 on `Baseline` and
narrowed `CODEOWNERS` specifically so the release bot's own appVersion-bump
PRs could merge unattended on green checks, while `PRD Promotion Review
Gate`'s `require_code_owner_review: true` (CODEOWNERS-scoped to
`values-prd.yaml` / `argocd/prd/`) still forces a human review for the
production-facing path. The `pull_request`-scoped bypass on that same
ruleset exists so the App can open the PR without needing a reviewer for its
own non-owned-path changes — the hole ADR 0015 flagged is not a hole, it's
the mechanism PR #110 already documented and reasoned through.

A second finding, not previously recorded: `repository_selection: "all"`
means `jdwlabs-release-bot` is already installed org-wide, across all
repositories the org has, not scoped to `deployments` alone — even though
only `deployments`' ruleset currently references its bypass actor. This is
relevant to §2 below: the org's most mature App installation already uses
the broad-install shape, not a narrowly-scoped one.

## 2. App topology — one shared App vs. one per agent class

**Decision: one shared App**, installed selectively on the four repos
(`apps`, `platform`, `infrastructure`, `deployments`), distinct from the
existing purpose-built automation Apps (`jdwlabs-release-bot`,
`jdwlabs-grafana-gitsync`, `jdwlabs-arc`). Proposed name:
`jdwlabs-agent-bot`.

### What already exists in this org

`gh api orgs/jdwlabs/installations` shows the org already runs three
distinct first-party Apps, each scoped to one automation surface:
`jdwlabs-release-bot` (prd promotion), `jdwlabs-grafana-gitsync` (dashboard
sync), `jdwlabs-arc` (self-hosted runner administration). That precedent is
a split **by automation surface**, not by individual workflow instance or
by "which agent session" produced a change — there is one App per distinct
*kind* of automated actor, not per actor instance. General agentic code
contribution (this ADR's subject) is itself one more distinct surface,
alongside release promotion and dashboard sync — so it gets its own App,
consistent with the existing pattern. What the existing pattern does *not*
do is fragment further within a surface (e.g., there is no
`jdwlabs-release-bot-authui` vs `jdwlabs-release-bot-container` split per
chart). Agentic contribution is one surface: an agent opening a PR against
any of the four repos, regardless of which agent session, model, or task
produced it. One App for that surface is the direct continuation of the
existing convention, not a departure from it.

### Why not split further, by agent class

- **Operational cost is the dominant variable, and it scales linearly with
  App count.** Every step in the App lifecycle — registration, private-key
  generation, per-repo installation, per-repo secret wiring, and eventual
  key rotation — is a manual, browser-driven action (see the runbook below;
  none of it can be automated by an agent). This org has one human
  maintainer. Registering and rotating N Apps multiplies that fixed human
  cost by N for no corresponding reduction in risk, since:
- **Permission scope is identical regardless of topology.** Every agent
  class under consideration needs the same two capabilities — write PR
  content, open/update pull requests (`contents:write`,
  `pull_requests:write`). Splitting by class doesn't shrink any one App's
  blast radius, because there is no narrower permission set to assign to a
  subset of classes; they all need the same grant.
- **Attribution doesn't require separate App identities.** ADR 0015's Phase
  1 left "which agent produced which PR" as the open question a shared App
  would need to answer. This org already has the answer running in
  production: agent-authored commits already carry a
  `Co-Authored-By: <agent name> <noreply@anthropic.com>` trailer — visible
  on `platform` PR #112's own commit, `Co-Authored-By: Claude Fable 5
  <noreply@anthropic.com>`, alongside `jdwillmsen`'s human authorship. A
  shared App's commits can carry the same trailer (with the concrete
  session/task identifier when useful, e.g. `JDWLABS-307`), which
  distinguishes producing agent, model, and task at the commit level —
  finer-grained than a per-class App identity could ever get, since a class
  still covers many individual sessions. `reviewDecision`/`mergedBy` already
  distinguish App-authored from human-authored at the PR level; commit
  trailers distinguish within App-authored. Nothing about attribution
  requires a second App registration.
- **The precedent that does exist (`jdwlabs-release-bot`) is installed
  broadly (`repository_selection: "all"`, §1), not narrowly per-purpose
  within its own surface** — reinforcing "one App per surface" over "one
  App per finer-grained class."

### Where this is installed narrower than precedent

Unlike `jdwlabs-release-bot`'s org-wide install, `jdwlabs-agent-bot` should
use `repository_selection: "selected"`, installed on exactly the four repos
named in this ADR's title. `jdwlabs-release-bot`'s all-repos install was
never a deliberate scoping decision recorded anywhere — it's just what
happens by default when an App is installed via the "all repositories"
radio button — worth not repeating by default now that it's visible. Scope
`jdwlabs-agent-bot` to what it actually needs.

### Revisit trigger

Per ADR 0015's own "Revisit" section: if the shared-App identity later
proves insufficient for attribution — concretely, if two concurrent agent
sessions ever produce PRs against the same ref where commit-trailer
attribution is ambiguous or missing, or a future requirement demands a
narrower permission set for one agent class than another (e.g., a read-only
research agent vs. a write-capable contribution agent) — split at that
point, informed by which specific gap showed up, rather than pre-splitting
against a hypothetical one now.

## 3. Technical control: catching an agent authored under the wrong identity

ADR 0015 named the actual fix directly: "an agent that pushes under
`jdwillmsen`'s own token reintroduces #112 regardless of how the ruleset is
shaped." This section proposes the concrete, automatable check for that
failure mode — not applied here (no App exists yet to test against), drafted
for Phase 2 implementation once `jdwlabs-agent-bot` is registered.

### The failure mode already has a fingerprint, and #112 already left one

`platform` PR #112 (`fix(argocd): right-size application-controller
memory`) — the exact PR ADR 0015 cites as the incident — was authored by
`jdwillmsen` (human identity) and merged via `--admin` bypass. Its commit
body already carries:

```
Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

This is the fingerprint: a commit with an agent co-author trailer, opened
under a non-agent identity. #112 is not a hypothetical case this control
would need to catch — it is a real, already-merged PR that would have
tripped it. That makes this control directly falsifiable against history
rather than speculative: run it against `platform`'s merged PRs and #112
fails, #63 (dependabot-authored, no human co-author trailer) passes, and any
future PR opened correctly through `jdwlabs-agent-bot` passes because its
author *is* the agent identity, not a human one carrying an agent trailer.

### Proposed check

A new required status check, `agent-identity / co-author-check`, added
alongside the existing `signatures` job pattern already required on every
repo's `Baseline` (`docs/adr/0015-...` and this repo's own
`.github/rulesets/baseline.json` both already require a `signatures /
signatures` check — this generalizes the same shape):

- For every commit on the pull request, check the commit message trailers
  for a co-author line matching a recognized agent signature (starting
  narrow: `Co-Authored-By: .*<noreply@anthropic\.com>`; extend the pattern
  if other agent tooling adopts a different trailer later).
- If any commit matches, **and** the PR author is not the registered agent
  identity (`jdwlabs-agent-bot`, or any other identity this ADR's org later
  registers for the same purpose), fail the check with a message pointing
  at this ADR and the runbook below: the change needs to be re-opened
  through the agent App token, not merged under a personal identity.
- If the PR author *is* the agent identity, the check passes unconditionally
  — the whole point is to prove the identity discipline held, not to
  penalize agent-authored PRs for carrying their own trailer.

This is a `pull_request`-triggered CI job (same trigger surface as the
existing `signatures` check), not a ruleset primitive on its own — GitHub
rulesets have no native "inspect commit trailers" rule type. The ruleset's
job is only to make the check's result required, same as every other CI gate
already listed in `required_status_checks`.

### Proposed ruleset diff (draft only — not applied)

The existing `Baseline` `pull_request` rule shape needs **no change** on
`apps`, `platform`, or `infrastructure`: `required_approving_review_count:
1`, `require_code_owner_review: false` already produces the PR #63 pattern
for any distinct authoring identity, agent App included — that's the
mechanism ADR 0015 identified and it requires no new configuration to work
for agent-authored PRs. `deployments`' `Baseline`
(`required_approving_review_count: 0`, narrowed `CODEOWNERS`) already
produces the same effect for its own change classes. No `bypass_actors`
entry is proposed for `jdwlabs-agent-bot` on any repo — unlike
`jdwlabs-release-bot`, agent-authored contribution PRs are meant to clear
review normally (PR #63's shape), not bypass it (PR #110's shape); granting
a bypass here would reintroduce exactly the "author != approver, but
unreviewed" gap this whole ADR chain exists to close.

The only concrete ruleset change proposed is adding the new check to
`required_status_checks` on each repo's `Baseline`, once it exists and has
been proven against a real PR:

```diff
   "required_status_checks": [
     ...
+    { "context": "agent-identity / co-author-check", "integration_id": 15368 },
     { "context": "signatures / signatures", "integration_id": 15368 }
   ]
```

Applied via each repo's existing `.github/rulesets/apply.sh`, same as every
other ruleset change in this org — not applied by this PR. Per this ticket's
scope, this stays a draft: the check has to exist and pass on a real PR
before it can be required, or every PR in every repo starts failing a check
that doesn't exist yet.

## 4. Human runbook

Every step below is human-only — App registration, private-key generation,
and installation all require the GitHub UI and org-admin permissions no
agent in this org has. A companion wizard script automates the parts that
*can* be automated once the human-only steps produce their outputs (the App
ID and private key): writing the four repos' secrets via `gh`.

- `scripts/setup-agentic-app-identity.sh` — run interactively by Jake. Walks:
  1. Register the App in the GitHub UI (name, permissions: Repository
     contents Read & write, Pull requests Read & write; no webhook; install
     on this account only).
  2. Generate and locally save the private key (`.pem`).
  3. Install the App on exactly `apps`, `platform`, `infrastructure`,
     `deployments` (not "all repositories" — see §2).
  4. Wire `AGENT_APP_ID` / `AGENT_APP_PRIVATE_KEY` secrets on all four repos
     via `gh secret set --repo`.
  5. Verify each repo can mint an installation token.

None of this is executed by this ADR or this PR — the script is committed
for Jake to run once, and to re-run unmodified for the eventual key
rotation.

### After the App exists (separate future PR, not this one)

- Add an `actions/create-github-app-token` step to whichever workflow opens
  agent-authored PRs, mirroring `deployments/.github/workflows/promote-prd.yml`'s
  `Generate promotion bot token` step, keyed on `AGENT_APP_ID` /
  `AGENT_APP_PRIVATE_KEY` instead of `RELEASE_APP_ID` / `RELEASE_APP_PRIVATE_KEY`.
- Implement and land `agent-identity / co-author-check` (§3), prove it
  against `platform` PR #112 (should fail) and #63 (should pass) as a
  regression fixture, then add it to `required_status_checks` per the draft
  diff above and run `apply.sh`.

## Consequences

**Accepted cost.** One new App registration and one new pair of per-repo
secrets (`AGENT_APP_ID` / `AGENT_APP_PRIVATE_KEY`), on top of the three the
org already manages. Key rotation is a repeat of the human-only runbook.

**Not addressed here.** The `agent-identity / co-author-check` job's actual
implementation (a script, not just a ruleset entry) and its CI wiring; the
workflow change that adds `actions/create-github-app-token` where agent PRs
are opened; running the human runbook itself. All are follow-up work gated
on Jake completing the App registration.

**Deliberately unchanged.** `jdwlabs-release-bot`'s existing bypass and
installation shape (§1 only explains it, doesn't touch it); the
`OrganizationAdmin` break-glass bypass (ADR 0015); every repo's existing
`required_approving_review_count` / `require_code_owner_review` shape (§3 —
no ruleset edits needed there, only a new required check once it exists).
