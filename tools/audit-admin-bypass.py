#!/usr/bin/env python3
"""Report admin-bypass merges among agent-authored PRs, across all four repos.

JDWLABS-181's first success metric is "0 routine admin-bypass merges for
agent-authored PRs (author != approver enforced natively)" — currently
unmeasured anywhere. This queries merged-PR history via `gh` and reports it.

A merge is flagged as bypassed when the target ref's *most restrictive*
active ruleset requires at least one approving review — every ruleset whose
`conditions.ref_name.include` covers the ref is consulted, not just the one
named "Baseline"; a repo can carry several (`platform` also has "Production
Gates", `deployments` adds "PRD Promotion Review Gate") and GitHub enforces
whichever is strictest — and the PR's `reviewDecision` is not `APPROVED`.
A ruleset that requires code-owner review counts as requiring one approval
even where its own count is zero, because that is how such gates are written
here: `deployments`' "PRD Promotion Review Gate" and `platform`'s "Change
Class Review Gate" both leave the count at zero and carry the entire
requirement on the code-owner flag. `deployments`' Baseline still requires
zero by design (ADR 0018 §1), so an unapproved merge on an unowned path
there is compliant, not a bypass. GitHub does not surface
"this merge used --admin" directly (no audit-log API access on this org's
plan); an already-merged PR that still reads as unapproved could only have
landed by bypassing the requirement in force *at merge time* — this tool
reads the requirement live, so a ruleset changed since a PR merged will be
judged against today's rule, not the one that actually applied.

A PR counts as agent-authored when any of its commits carries a
`Co-Authored-By: ... <noreply@anthropic.com>` trailer — the same signal
ADR 0018 §3 names for the not-yet-built `agent-identity / co-author-check`.
This is a heuristic in both directions: a session told to omit the trailer
passes through unnoticed (false negative, same limitation ADR 0018 §3
already documents), and a human-authored PR that merges the base branch
mid-flight can pull in another commit's trailer and be misclassified (false
positive) — this tool does not attempt to exclude base-reachable commits.

Usage:
    python3 tools/audit-admin-bypass.py [--since YYYY-MM-DD] [--list]

    --since   Only count PRs merged on or after this date (default: 7 days ago)
    --list    Print each flagged PR's number, title and URL, not just the count

Exit code reflects tool errors only (bad --since, gh/API failures) — it
never depends on how many bypasses were found; this is a report, not a CI
gate.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import date, timedelta

REPOS = ["apps", "platform", "infrastructure", "deployments"]
TARGET_REF = "refs/heads/main"
AGENT_TRAILER = re.compile(r"Co-Authored-By:.*<[^>]*@anthropic\.com>", re.IGNORECASE)

# gh pr list's --limit is capped well under gh's own max: requesting the
# nested commits.authors connection multiplies node cost per PR, and that
# query blows GitHub's 500k-node budget past roughly 200 PRs with commit
# data attached. Fetching commits per-PR afterward (see merged_prs_since)
# sidesteps that entirely, so this limit only bounds the PR *list* itself —
# confirmed safe at 100 with no commits field requested.
LIST_LIMIT = 100


class ToolError(Exception):
    """A gh/API failure or bad input — distinct from "found some bypasses"."""


def gh_json(args: list[str]) -> object:
    result = subprocess.run(["gh", *args], capture_output=True, text=True)
    if result.returncode != 0:
        raise ToolError(f"gh {' '.join(args)}\n{result.stderr.strip()}")
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise ToolError(f"gh {' '.join(args)} returned non-JSON output: {exc}") from exc


def ruleset_review_requirement(detail: dict, ref: str) -> int | None:
    """Approvals `detail` demands on `ref`, or None if it does not apply.

    A code-owner requirement scores as one approval even when the ruleset's
    own count is zero. GitHub folds CODEOWNERS into `reviewDecision`, so a PR
    needing an owner's sign-off cannot read APPROVED without one; scoring the
    flag as zero would rate an owner-gated ruleset as demanding nothing and
    silently exempt every path it protects from this audit.

    Simplification: only checks for `ref` appearing literally in the include
    list — none of this org's rulesets currently use the `~ALL` /
    `~DEFAULT_BRANCH` special values, so a repo that starts using them would
    need this extended.
    """
    if detail["enforcement"] != "active":
        return None
    includes = detail.get("conditions", {}).get("ref_name", {}).get("include", [])
    if ref not in includes:
        return None

    required = None
    for rule in detail["rules"]:
        if rule["type"] != "pull_request":
            continue
        params = rule["parameters"]
        count = params["required_approving_review_count"]
        if params.get("require_code_owner_review"):
            count = max(count, 1)
        required = count if required is None else max(required, count)
    return required


def required_review_count(repo: str, ref: str = TARGET_REF) -> int:
    """Strictest approval requirement across every ruleset covering `ref`."""
    rulesets = gh_json(["api", f"repos/jdwlabs/{repo}/rulesets"])
    counts = []
    for rs in rulesets:
        detail = gh_json(["api", f"repos/jdwlabs/{repo}/rulesets/{rs['id']}"])
        required = ruleset_review_requirement(detail, ref)
        if required is not None:
            counts.append(required)
    if not counts:
        raise ToolError(f"{repo}: no active pull_request-rule ruleset covers {ref}")
    return max(counts)


def merged_prs_since(repo: str, since: date, ref: str = TARGET_REF) -> list[dict]:
    prs = gh_json(
        [
            "pr", "list", "--repo", f"jdwlabs/{repo}",
            "--search", f"merged:>={since.isoformat()}", "--state", "merged",
            "--limit", str(LIST_LIMIT),
            "--json", "number,title,url,mergedAt,reviewDecision,baseRefName",
        ]
    )
    if len(prs) == LIST_LIMIT:
        print(
            f"WARNING: {repo} hit the {LIST_LIMIT}-PR fetch cap for this "
            f"window — counts are a lower bound; narrow --since to trust them",
            file=sys.stderr,
        )
    prs = [pr for pr in prs if pr["baseRefName"] == ref.removeprefix("refs/heads/")]

    # commits fetched per-PR, one gh call each, rather than in the list call
    # above — that's what avoids the node-budget ceiling LIST_LIMIT's
    # comment describes. Slower (one extra round trip per PR) but correct
    # regardless of window size.
    for pr in prs:
        detail = gh_json(
            ["pr", "view", str(pr["number"]), "--repo", f"jdwlabs/{repo}", "--json", "commits"]
        )
        pr["commits"] = detail["commits"]
    return prs


def is_agent_authored(pr: dict) -> bool:
    return any(
        AGENT_TRAILER.search(c.get("messageBody", "") or "")
        for c in pr.get("commits", [])
    )


def parse_since(value: str) -> date:
    try:
        return date.fromisoformat(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"{value!r} is not a YYYY-MM-DD date") from exc


def main() -> int:
    parser = argparse.ArgumentParser(
        formatter_class=argparse.RawDescriptionHelpFormatter, description=__doc__
    )
    parser.add_argument(
        "--since", type=parse_since, default=None,
        help="only count PRs merged on or after this date (default: 7 days ago)",
    )
    parser.add_argument(
        "--list", action="store_true",
        help="print each flagged PR's number, title and URL, not just the count",
    )
    args = parser.parse_args()
    since = args.since or (date.today() - timedelta(days=7))

    total_agent = 0
    total_bypassed = 0
    flagged: list[tuple[str, dict]] = []

    try:
        for repo in REPOS:
            required = required_review_count(repo)
            agent_prs = [pr for pr in merged_prs_since(repo, since) if is_agent_authored(pr)]
            bypassed = (
                [pr for pr in agent_prs if pr["reviewDecision"] != "APPROVED"]
                if required >= 1
                else []
            )
            total_agent += len(agent_prs)
            total_bypassed += len(bypassed)
            flagged.extend((repo, pr) for pr in bypassed)

            print(
                f"{repo}: required_reviews={required} "
                f"agent-authored={len(agent_prs)} bypassed={len(bypassed)}"
            )
    except ToolError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    print(
        f"\nTOTAL bypassed/agent-authored: {total_bypassed}/{total_agent} "
        f"since {since.isoformat()}"
    )
    print("(JDWLABS-181 success metric 1 target: 0 routine admin-bypass merges)")

    if args.list and flagged:
        print("\nFlagged:")
        for repo, pr in flagged:
            print(f"  {repo}#{pr['number']}  {pr['title']}  {pr['url']}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
