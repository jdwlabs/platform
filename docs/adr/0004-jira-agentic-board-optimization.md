# Spec: Jira board optimization for agentic workflows

Status: proposed. Requires Jira site-admin actions (custom field + automation
rule creation) that the MCP integration's `read:jira-work`/`write:jira-work`
scopes cannot perform — issue-level writes only, no admin API. Everything
below needs to be applied by hand in Jira Settings.

## Problem

Agents (Holmes/AI-SRE relay, Claude Code sessions) read and write JDWLABS
issues but have no structured signal for:

- which repo a fix belongs in (agents currently grep 4 repos to find out)
- whether an issue is safe to act on unattended vs. needs a human call
- whether an "AI-SRE: <AlertName>" issue is a genuine new occurrence or a
  relay dedup miss (label-based dedup exists — `amfp-<fingerprint>` — but
  nothing stops a second issue with a *different* summary format for the
  same underlying alert from slipping past a label match)

## Changes

### 1. Custom field: `Repo` (single-select)

Options: `apps`, `platform`, `infrastructure`, `deployments`, `n/a` (org/process
issues with no single home repo).

Required on the `Task` issue type. Lets an agent filter its own worklist with
`project = JDWLABS AND Repo = platform AND labels = agent-actionable` instead
of reading every open issue's description to figure out where it lives.

### 2. Label taxonomy (already rolled out to the 22 non-epic, non-AI-SRE
backlog issues as of this spec; apply going forward to new issues)

- `agent-actionable` — clear technical fix, no judgment call, an agent can
  open a PR unattended.
- `needs-human-decision` — policy, billing, credential rotation, or a
  scale-gated architecture call. Agents should comment with findings/options
  and stop short of executing.

Rationale for stopping at two labels instead of a wider taxonomy: more
categories fragment the JQL an agent has to write to find its queue, for
marginal extra precision. Revisit if `agent-actionable` starts needing
sub-splits (e.g. "needs cluster access" vs. "code-only") once volume justifies it.

### 3. Automation rule: pre-create dedup guard for `ai-sre` issues

Trigger: issue created, `project = JDWLABS`.
Condition: `labels` contains a value matching `amfp-*`.
Action: JQL search `project = JDWLABS AND labels = <matched-amfp-label> AND
key != <the new issue's key>`. If any result — comment linking the
duplicate, transition the new issue to Done with resolution "Duplicate".

This is belt-and-suspenders on top of the relay-side fix (`ai-sre-relay`
commit `18173894`, reopens a Done issue by fingerprint instead of creating a
new one). The relay fix covers the relay's own create path; this rule covers
any other path that might create an `ai-sre`-labeled issue (manual creation,
a future second alert source) without knowing the relay's reopen logic.

## Non-goals

- No changes to issue *type* scheme or workflow states — current
  Backlog/Ready/In Progress/Review/Done set is sufficient signal for agents.
- Not adding a `Runbook-Link` field yet — no runbook repo/format exists to
  link to. Revisit once `docs/OPERATIONS.md` §5 (symptom→fix table) has
  enough entries to be worth a per-issue pointer instead of a search.
