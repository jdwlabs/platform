# ADR: Agent identity verified via a real bot-authored test PR

Status: accepted. Extends
[agentic-app-topology](0018-agentic-app-topology.md) and
[agent-app-installed-org-wide](0020-agent-app-installed-org-wide.md). This
record itself is the artifact being verified — the PR that carries it is
authored under the `jdwlabs-agent-bot` identity this ADR chain exists to
establish, not under a human identity, which is the entire test.

## What this proves

JDWLABS-307's own Definition of Done names the acceptance test directly:
confirm, with a real bot-authored test PR, that the repo's approving human
can approve it without hitting GitHub's self-review wall — mirroring
`platform` PR #63 (`app/dependabot`-authored, approved and merged normally)
against the failure case, PR #112 (`jdwillmsen`-authored, stuck at
`REVIEW_REQUIRED`, needed an `--admin` bypass). #112 is the incident this
entire ADR chain (0015, 0018, 0020, this record) exists to stop recurring.

## Mechanism used

No CI-triggered workflow that opens agent-authored PRs exists in this org
yet — `create-github-app-token` (0018 §4's specified integration point) is
an Actions-only mechanism with nothing to attach to today. This PR was
opened by direct token exchange instead: a JWT signed with the App's
private key (`iss` = App ID, 10-minute expiry), exchanged for a
short-lived installation access token via
`POST /app/installations/{id}/access_tokens`, used to authenticate both
the `git push` and the `gh pr create` call that opened this PR — nothing
routed through the interactive session's own `gh` identity at any point in
either operation.

This is a different code path from what 0018 §4 anticipated (a workflow
step), but the same identity and the same resulting GitHub-side behavior:
the PR is attributed to `jdwlabs-agent-bot[bot]`, not to a human account.
Building the workflow-integrated path is follow-up work, tracked against
JDWLABS-309 rather than repeated here — this record only needed to prove
the identity clears review, not to build its eventual CI-triggered home.

## Verification

- Commit authored as `jdwlabs-agent-bot[bot] <4589889+jdwlabs-agent-bot[bot]@users.noreply.github.com>`
- PR opened via the installation token, not the interactive session's own credentials
- `required_approving_review_count: 1` on `platform`'s `Baseline` ruleset — unchanged, no bypass entry added for this App (0018 §3's decision, reaffirmed: agent PRs clear review normally, they don't bypass it)
- Approval and merge evidence: recorded on JDWLABS-307 once reviewed, not baked into this record — an ADR documents a decision or a proof, not a running log of what happens to the PR that carries it after the fact
