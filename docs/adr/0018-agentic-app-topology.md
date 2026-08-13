# ADR: Agentic App topology, bypass-actor resolution, and the author-identity control

Status: proposed. Phase 2 of JDWLABS-181, resolving the two open questions
ADR 0015 flagged and left to this phase. Does not create the App, change any
live ruleset, or touch branch protection — those remain human-only actions
(see "Human runbook" below), executed after this record is reviewed.

References: `docs/adr/0015-agentic-contribution-identity-and-review-gates.md`
("Options considered — identity", "Bypass actors — a flagged hole, not a
fix", "Consequences — Not addressed here").

## 1. Bypass actor `4065387` — identity resolved, purpose re-examined

ADR 0015 flagged `deployments`' `PRD Promotion Review Gate` ruleset granting
`{"actor_type": "Integration", "actor_id": 4065387, "bypass_mode":
"pull_request"}` to an unconfirmed identity, naming Renovate as a candidate,
and asked Phase 2 to understand it "before it's replicated to three more
repos."

### Identity

`gh api orgs/jdwlabs/installations` (org-level, admin-token, read-only) lists
every App installation in the org and resolves it directly:

```
app_id: 4065387, app_slug: "jdwlabs-release-bot", installation_id: 140587771
permissions: {"contents": "write", "metadata": "read", "pull_requests": "write"}
repository_selection: "all"
```

The bypass actor is `jdwlabs-release-bot` itself — not Renovate, not a
third-party integration.

### It isn't only on `PRD Promotion Review Gate`

The same `Integration 4065387` bypass, at the same `bypass_mode:
"pull_request"`, is present on all three `deployments` rulesets that cover
`main`:

| Ruleset | id | `required_approving_review_count` | `require_code_owner_review` | Other rules |
|---|---|---|---|---|
| `Baseline` | 17653708 | 0 | false | `deletion`, `non_fast_forward`, `required_linear_history`, `required_status_checks` (9 checks) |
| `PRD Promotion Review Gate` | 18824430 | 0 | true (CODEOWNERS-scoped: `values-prd.yaml`, `argocd/prd/`) | — |
| `Production Gates` | 17653706 | 0 | false | `deletion`, `non_fast_forward`, `required_linear_history`, `creation`; scoped to `main`, `release/**`, `hotfix/**` |

ADR 0015 only examined the first row. `Production Gates` in particular
carries a materially broader rule set (creation, deletion, linear-history —
not just review) that ADR 0015 didn't mention at all.

### What the bypass was meant to do, and what it turned out not to do

`deployments` PR #51 ("scope the release app bypass to pull requests")
narrowed the bypass on all three rulesets from `always` to `pull_request`,
and states its own reasoning directly: *"`bypass_mode: pull_request` lets
the App merge its own pull request — which it must, since an App cannot
approve one."* That belief is the entire justification for the bypass
existing in its current form on all three rulesets.

`deployments` PR #110 ("let the release bot merge its own appVersion
bumps"), filed later against the exact stall this bypass was supposed to
prevent, found that belief **wrong**: *"`bypass_mode: pull_request` does
NOT permit the App's explicit merge call... both the auto-merge and
explicit-merge paths are closed."* PR #110's actual fix was unrelated to
the bypass — it dropped `required_approving_review_count` to 0 on
`Baseline` and narrowed `CODEOWNERS` so the bot's own non-owned-path
changes need no review from anyone, bypass or not.

My own earlier read of this ("the bypass exists so the App can open the PR
without needing a reviewer") was also wrong, for a simpler reason: opening
a pull request is never gated by branch-protection rules under any
ruleset — only merging is. There's nothing for a bypass to do at PR-open
time.

### Conclusion: the bypass looks vestigial, not intentional

Following the review-count value each ruleset actually carries today: on
`Baseline` and `Production Gates`, `required_approving_review_count` is
already 0, so the `pull_request` rule requires no review from anyone
regardless of the bypass — the bypass has nothing left to do there. On
`PRD Promotion Review Gate`, `require_code_owner_review: true` still
applies to the CODEOWNERS-owned paths the promotion workflow actually
touches (`values-prd.yaml`), and per PR #110's own finding the bypass
cannot get the App around that requirement either — which is why PR #12's
original design always routed promotion PRs through an explicit human
review and merge, bypass or not. In no case examined does the bypass
appear to be doing any live work today: it was added on the strength of an
assumption (PR #51) that a later investigation (PR #110) explicitly
disproved, and nothing since has gone back to remove it.

This ADR does not remove it — that's a `deployments`-only ruleset change,
independent of registering `jdwlabs-agent-bot`, and removing a bypass
grant deserves its own verification pass rather than a drive-by edit here.
But it should not be read as a validated pattern to replicate onto the new
App's installations either. Recommended as separate follow-up: re-verify
live behavior directly (e.g. dispatch a real promotion PR and attempt an
App-token merge against each rule shape) before either removing the bypass
entries as dead weight or, if some live effect is found that this reading
missed, documenting what it actually is.

### A second, unrelated finding

`repository_selection: "all"` means `jdwlabs-release-bot` is installed
org-wide, across every repository the org has, not scoped to `deployments`
alone — even though only `deployments`' rulesets reference its bypass
actor. None of PR #12, #51, or #110 — the three PRs that built and
reshaped this App's access — discusses installation scope at all; the
`all`-repositories setting is most plausibly the default outcome of
whichever install click-path was used, not a decision anyone recorded
reasoning about. Absence of a recorded rationale isn't proof of accident,
but it's not evidence of a deliberate broad-access design either, so it's
noted here only as a fact worth not repeating by default (see §2's
narrower install choice for `jdwlabs-agent-bot`) — not as precedent for
anything.

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
- **What §1's `repository_selection: "all"` finding does *not* support.**
  An earlier draft of this ADR cited `jdwlabs-release-bot`'s org-wide
  install as reinforcing evidence for "one App per surface." §1 now shows
  that setting was never a deliberate choice — an unintentional default
  can't be cited as precedent for anything, and this decision doesn't rest
  on it.

### The real cost this topology accepts, and how to bound it

The argument above rules out per-class Apps on operational-cost grounds, but
that isn't the same as saying one shared App is free. It has a real
downside the earlier draft of this ADR didn't name: one App means the
**same private key** sits in all four repos' Actions secret stores
(`AGENT_APP_PRIVATE_KEY` on `apps`, `platform`, `infrastructure`, and
`deployments` alike). A compromised CI job in any *one* of those repos — a
malicious transitive dependency, a compromised third-party Action, a
`pull_request_target` workflow that lets an untrusted PR body reach code
with secret access — yields a credential good for `contents:write` +
`pull_requests:write` everywhere the App is installed, `deployments`'
production-promotion-adjacent paths included. That blast radius is the
actual price of "one App, four repos," and per-class Apps would not have
been the right fix for it anyway (splitting by *agent class* doesn't map to
splitting by *repo*, and the operational cost above still applies).

The right mitigation is scoping at token-mint time, not at App-registration
time: `actions/create-github-app-token` — the same action
`promote-prd.yml` already uses — accepts a `repositories` input (restrict
the minted token to one repo, a subset of the App's full install list) and
per-permission inputs (`permission-contents`, `permission-pull-requests`,
everything else implicitly `none`). Each repo's workflow mints its own
token scoped to just that repo at exactly the App's already-minimal
permission ceiling, even though the underlying App installation spans all
four. A token leaked from `apps`' CI, minted with `repositories: apps`,
cannot touch `deployments` — the compromise is bounded to the repo it
leaked from, regardless of what the App itself is installed on. This
belongs in the workflow wiring, not the ruleset; see the runbook (§4) for
where it lands.

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
tripped it. That makes this control directly falsifiable against history:
run it against `platform`'s merged PRs and #112 fails, #63
(dependabot-authored, no human co-author trailer) passes, and any future PR
opened correctly through `jdwlabs-agent-bot` passes because its author *is*
the agent identity, not a human one carrying an agent trailer.

### What this check cannot catch

#112 trips this check only because the trailer happened to be present. The
trailer is unauthenticated, self-asserted text in a commit message — a
human (or an agent instructed to) can author a change under a personal
identity and simply omit it, and the check has nothing to see. That is
exactly the failure mode ADR 0015 named: "an agent that pushes under
`jdwillmsen`'s own token" — an omitted trailer produces precisely that,
passing this check while never having gone through the agent identity at
all. This control catches an *accidental* leak of the wrong identity (the
trailer present because agent tooling adds it by convention, as it already
does in this org, while the PR was opened under the wrong account) — it is
not, and cannot be, an enforcement mechanism against a *deliberate* or
careless bypass of the App identity. Treat it as a tripwire for the
common-case accident, not as proof the identity discipline is enforced;
the actual enforcement is procedural (only ever open agent PRs through the
App token) same as ADR 0015 already said, and this check is a cheap,
partial backstop for when that procedure isn't followed — not a
replacement for it.

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
  5. Verify both secrets landed on all four repos (`gh secret list`). The
     script deliberately stops there — minting an actual installation
     token needs a workflow run with the `create-github-app-token` step
     from the next section, which doesn't exist until that follow-up PR
     lands, so nothing in this script can exercise it yet.

None of this is executed by this ADR or this PR — the script is committed
for Jake to run once, and to re-run unmodified for the eventual key
rotation.

### After the App exists (separate future PR, not this one)

- Add an `actions/create-github-app-token` step to whichever workflow opens
  agent-authored PRs, mirroring `deployments/.github/workflows/promote-prd.yml`'s
  `Generate promotion bot token` step, keyed on `AGENT_APP_ID` /
  `AGENT_APP_PRIVATE_KEY` instead of `RELEASE_APP_ID` / `RELEASE_APP_PRIVATE_KEY`.
  Per §2's "real cost" analysis: pass `repositories: <owning-repo-only>` and
  explicit `permission-contents` / `permission-pull-requests` inputs on
  every one of the four repos' steps, even though the App itself is
  installed on all four — each repo mints a token scoped to itself alone,
  so a leak in one repo's CI can't reach the other three.
- Implement and land `agent-identity / co-author-check` (§3), prove it
  against `platform` PR #112 (should fail) and #63 (should pass) as a
  regression fixture, then add it to `required_status_checks` per the draft
  diff above and run `apply.sh`. Document its limits (§3, "What this check
  cannot catch") alongside it so it isn't relied on as more than a
  tripwire.
- Separately, re-verify what (if anything) `jdwlabs-release-bot`'s
  `Integration` bypass on `deployments`' three rulesets actually does live
  (§1), and either remove it or document the real mechanism — before
  deciding whether `jdwlabs-agent-bot` needs any bypass grant of its own
  (current recommendation: it doesn't, see §3's ruleset diff).

## Consequences

**Accepted cost.** One new App registration and one new pair of per-repo
secrets (`AGENT_APP_ID` / `AGENT_APP_PRIVATE_KEY`), on top of the three the
org already manages. Key rotation is a repeat of the human-only runbook. One
shared private key across four repos' secret stores, whose blast radius on
compromise is only bounded by per-workflow token scoping (§2) that has to
be wired correctly in every one of the four repos' workflows — an ongoing
discipline requirement, not a one-time setup step.

**Not addressed here.** The `agent-identity / co-author-check` job's actual
implementation (a script, not just a ruleset entry) and its CI wiring,
including the documented limitation that an omitted trailer defeats it
entirely (§3); the workflow change that adds `actions/create-github-app-token`
(with per-repo `repositories`/`permission-*` scoping) where agent PRs are
opened; running the human runbook itself; and re-verifying — not just
re-explaining — what `jdwlabs-release-bot`'s `Integration` bypass on
`deployments`' three rulesets actually does today, which §1 leaves as "looks
vestigial" rather than confirmed dead. All are follow-up work gated on Jake
completing the App registration.

**Deliberately unchanged.** `jdwlabs-release-bot`'s existing bypass and
installation shape (§1 explains and questions it, doesn't touch it — no
ruleset in this org is edited by this ADR); the `OrganizationAdmin`
break-glass bypass (ADR 0015); every repo's existing
`required_approving_review_count` / `require_code_owner_review` shape (§3 —
no ruleset edits needed there, only a new required check once it exists).
