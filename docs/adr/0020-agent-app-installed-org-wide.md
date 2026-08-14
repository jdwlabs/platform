# ADR: `jdwlabs-agent-bot` installed org-wide, not on the four named repos

Status: accepted. Extends
[agentic-app-topology](0018-agentic-app-topology.md). Records a deviation
from that record's §2, decided at registration time, not a finding —
correcting the record to match what was actually done, per this repo's own
rule that a disproved or superseded premise gets written down rather than
left silently wrong.

## What 0018 decided and what actually happened

0018 §2, "Where this is installed narrower than precedent," specified
`repository_selection: "selected"` on exactly `apps`, `platform`,
`infrastructure`, `deployments` — deliberately narrower than
`jdwlabs-release-bot`'s org-wide install, which that same section names as
an unintentional default worth not repeating.

`jdwlabs-agent-bot` was registered and installed with `repository_selection:
"all"` — confirmed live via `gh api orgs/jdwlabs/installations`. Operator
decision, made directly at install time: keep it on all repositories rather
than manage a narrower, hand-maintained list. Not a technical finding, not a
default clicked past — a deliberate choice to accept 0018's named cost in
full rather than mitigate it at the installation layer.

## What this changes

0018's blast-radius argument (§2, "The real cost this topology accepts, and
how to bound it") is unaffected in its reasoning, only in its scope: it
already said a compromised CI job anywhere the App is installed yields a
`contents:write` + `pull_requests:write` credential everywhere the App is
installed. That was four repos including `deployments`. It is now every
repository in the org, present and future — any new repo created under
`jdwlabs` inherits this App's access without a further installation step,
since `all` tracks the org's repository list rather than a fixed set chosen
once.

0018's proposed mitigation — `actions/create-github-app-token`'s
`repositories` input, scoping each workflow's minted token down to the one
repo it runs in even though the App's own installation spans more — was
already the right control regardless of installation width; a narrow
install would have made it defense-in-depth on top of a narrow blast
radius, redundant in the case that mattered least. With the install now
org-wide, that per-workflow token scoping is no longer redundant with
anything: it is the entire boundary between one compromised job in any
repo in the org and a credential valid everywhere. 0018 §4's follow-up —
adding the `create-github-app-token` step with explicit `repositories:` and
`permission-*` inputs to every workflow that opens agent-authored PRs — is
correspondingly more load-bearing than it was when this record was
written, and should not ship without that scoping present from its first
version.

## Decision

Accept the org-wide install as-is. Do not re-narrow it after the fact — the
operator's choice was explicit, not accidental, and narrowing it later
without being asked would relitigate a decision that was already made
directly. The correction this record makes is to the written record, so a
future reader of 0018 doesn't trust a scope claim that stopped being true
at registration.

## Consequences

**No repo in the org needs a further installation step to be covered.** A
repo created after this record still inherits `jdwlabs-agent-bot` access
automatically — this is the direct effect of `all`, not a gap to close.

**Per-workflow token scoping is now mandatory, not optional hardening.**
Every future PR wiring `create-github-app-token` for this App must pass
`repositories: <owning-repo>` and explicit `permission-contents` /
`permission-pull-requests` inputs. A workflow that omits the `repositories`
input mints a token valid across the entire org's repository list, not just
its own — silently, with no error, since the action's default behavior is
exactly that.

**0018's own text is now inaccurate about installation scope** in the
places it describes `jdwlabs-agent-bot` as narrower than `jdwlabs-release-bot`.
That text is not edited — this record is the correction, referenced forward
per the append-only convention.
