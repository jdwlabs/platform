#!/usr/bin/env python3
"""Report admin-bypass merges among agent-authored PRs, across all four repos.

JDWLABS-181's first success metric is "0 routine admin-bypass merges for
agent-authored PRs (author != approver enforced natively)" — currently
unmeasured anywhere. This queries merged-PR history via `gh` and reports it.

A merge is flagged as bypassed when its repo's Baseline ruleset requires at
least one approving review (checked live, not assumed — `deployments`
requires zero by design per ADR 0018 §1, so an unapproved merge there is
compliant, not a bypass) and the PR's `reviewDecision` is not `APPROVED`.
GitHub does not surface "this merge used --admin" directly (no audit-log API
access on this org's plan); an already-merged PR that still reads as
unapproved could only have landed by bypassing the requirement, which is the
same thing from the metric's perspective regardless of which bypass
mechanism was used.

A PR counts as agent-authored when any of its commits carries a
`Co-Authored-By: ... <noreply@anthropic.com>` trailer — the same signal
ADR 0018 §3 names for the not-yet-built `agent-identity / co-author-check`.
This is a detection heuristic, not proof: a session instructed to omit the
trailer would pass through this tool unnoticed, same limitation ADR 0018 §3
already documents for the CI check version.

Usage:
    python3 tools/audit-admin-bypass.py [--since YYYY-MM-DD] [--list]

    --since   Only count PRs merged on or after this date (default: 7 days ago)
    --list    Print each flagged PR's number, title and URL, not just the count

Exit code is always 0 — this is a report, not a CI gate.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone

REPOS = ["apps", "platform", "infrastructure", "deployments"]
AGENT_TRAILER = re.compile(r"Co-Authored-By:.*<[^>]*@anthropic\.com>", re.IGNORECASE)


def gh_json(args: list[str]) -> object:
    result = subprocess.run(["gh", *args], capture_output=True, text=True)
    if result.returncode != 0:
        print(f"gh {' '.join(args)}\n{result.stderr}", file=sys.stderr)
        raise SystemExit(1)
    return json.loads(result.stdout)


def required_review_count(repo: str) -> int:
    rulesets = gh_json(["api", f"repos/jdwlabs/{repo}/rulesets"])
    for rs in rulesets:
        if rs["name"] != "Baseline":
            continue
        detail = gh_json(["api", f"repos/jdwlabs/{repo}/rulesets/{rs['id']}"])
        for rule in detail["rules"]:
            if rule["type"] == "pull_request":
                return rule["parameters"]["required_approving_review_count"]
    raise RuntimeError(f"{repo}: no Baseline ruleset with a pull_request rule found")


def merged_prs_since(repo: str, since: datetime) -> list[dict]:
    # --limit is capped well below gh's own max: requesting the nested
    # commits.authors connection multiplies node cost per PR — a repo with
    # heavier per-PR commit counts (apps) blew GitHub's 500k-node budget at
    # limit 50. 25 comfortably covers a week's merge volume in this org;
    # raise only if a --since window genuinely needs it, and expect to lower
    # it again for a repo with unusually large PRs.
    query = f"is:merged merged:>={since.date().isoformat()}"
    return gh_json(
        [
            "pr", "list", "--repo", f"jdwlabs/{repo}",
            "--search", query, "--state", "merged", "--limit", "25",
            "--json", "number,title,url,mergedAt,reviewDecision,commits",
        ]
    )


def is_agent_authored(pr: dict) -> bool:
    return any(
        AGENT_TRAILER.search(c.get("messageBody", "") or "")
        for c in pr.get("commits", [])
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--since", type=str, default=None)
    parser.add_argument("--list", action="store_true")
    args = parser.parse_args()

    since = (
        datetime.strptime(args.since, "%Y-%m-%d").replace(tzinfo=timezone.utc)
        if args.since
        else datetime.now(timezone.utc) - timedelta(days=7)
    )

    total_agent = 0
    total_bypassed = 0
    flagged: list[tuple[str, dict]] = []

    for repo in REPOS:
        required = required_review_count(repo)
        prs = merged_prs_since(repo, since)
        agent_prs = [pr for pr in prs if is_agent_authored(pr)]
        bypassed = [
            pr for pr in agent_prs
            if required >= 1 and pr["reviewDecision"] != "APPROVED"
        ]
        total_agent += len(agent_prs)
        total_bypassed += len(bypassed)
        flagged.extend((repo, pr) for pr in bypassed)

        print(
            f"{repo}: required_reviews={required} "
            f"agent-authored={len(agent_prs)} bypassed={len(bypassed)}"
        )

    ratio = f"{total_bypassed}/{total_agent}" if total_agent else "0/0"
    print(f"\nTOTAL bypassed/agent-authored: {ratio} since {since.date().isoformat()}")
    print("(JDWLABS-181 success metric 1 target: 0 routine admin-bypass merges)")

    if args.list and flagged:
        print("\nFlagged:")
        for repo, pr in flagged:
            print(f"  {repo}#{pr['number']}  {pr['title']}  {pr['url']}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
