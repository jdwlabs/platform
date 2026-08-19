#!/usr/bin/env python3
"""Report admin-bypass merges among agent-authored PRs, across all four repos.

JDWLABS-181's first success metric is "0 routine admin-bypass merges for
agent-authored PRs (author != approver enforced natively)" — currently
unmeasured anywhere. This queries merged-PR history via `gh` and reports it.

A merge is *unapproved* when the target ref's most restrictive active ruleset
requires at least one approving review — every ruleset whose
`conditions.ref_name.include` covers the ref is consulted, not just the one
named "Baseline"; a repo can carry several (`platform` also has "Production
Gates", `deployments` adds "PRD Promotion Review Gate") and GitHub enforces
whichever is strictest — and the PR's `reviewDecision` is not `APPROVED`.
A ruleset that requires code-owner review counts as requiring one approval
even where its own count is zero, because that is how such gates are written
here: `deployments`' "PRD Promotion Review Gate" and `platform`'s "Change
Class Review Gate" both leave the count at zero and carry the entire
requirement on the code-owner flag. GitHub does not surface "this merge used
--admin" directly (no audit-log API access on this org's plan); an
already-merged PR that still reads as unapproved could only have landed by
bypassing the requirement in force *at merge time* — this tool reads the
requirement live, so a ruleset changed since a PR merged will be judged
against today's rule, not the one that actually applied.

Unapproved is not, on its own, reportable
-----------------------------------------

Measured across the four repos, 187 of 200 sampled merged PRs carry zero
approving reviews; every merged PR sampled on this repo reads
`reviewDecision: REVIEW_REQUIRED`. That is not 187 lapses. This is a two-seat
org with a catch-all CODEOWNERS, and GitHub forbids approving your own pull
request — so for the account that opens nearly every PR, "unapproved" is the
only state a merge can be in. A check that failed on it would fire on ~93% of
merges and be filtered within a week, which is the exact failure this tool
exists to avoid: a control nobody reads is indistinguishable from no control.

What a two-seat org *can* enforce is the machine gate. Merging green but
unapproved is the operating mode. Merging while a required status check is
red, or never ran at all, is the escape hatch being used against a gate that
needs no second human to satisfy — nothing about the seat count excuses it,
and it is rare. So:

  * **routine** — unapproved, every required status check satisfied on the
    PR's head commit. Counted and printed, never fails the run. This is the
    number the success metric above is actually about.
  * **reportable** — unapproved *and* at least one required status check
    missing or not successful on the head commit, and not covered by a
    declared hold in tools/admin-bypass-holds.yaml.

Required-check state is read from the check-runs API, never from the legacy
combined-status endpoint: this org posts only check runs, so
`commits/<sha>/status` reports `state: "pending"` with an empty `statuses`
array on a commit whose every check succeeded. A tool reading that would
grade every merge as bypassed.

Where this is blind
-------------------

`deployments`' Baseline requires zero approvals, so no merge there can be
unapproved by this tool's definition and the whole repo is exempt from the
review half of the audit. That zero is a decision with a narrow purpose, not
a policy that the repo needs no review: ADR 0018 §1 records that a release-bot
pull request dropped `required_approving_review_count` to 0 on Baseline and
narrowed CODEOWNERS so the bot's own non-owned-path changes needed no
reviewer, and concludes the surrounding bypass grant "looks vestigial, not
intentional". The `required == 0 → nothing to bypass` logic below is
mechanically correct and stays; what it costs is that this audit says nothing
about `deployments` until that count changes. The ruleset-reconciliation work
is where the count is being reconsidered — that is the change that un-blinds
this, not a change here.

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

Exit codes:
    0   no reportable bypass — routine unapproved merges may still be counted
    1   at least one reportable bypass, or a stale hold
    2   the audit could not run (bad input, gh/API failure, unreadable holds)

1 and 2 are deliberately distinct. "Found nothing" and "could not look" are
different statements, and a caller that conflates them reports a clean bill of
health for a check that never executed.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import date, timedelta
from pathlib import Path

REPOS = ["apps", "platform", "infrastructure", "deployments"]
TARGET_REF = "refs/heads/main"
AGENT_TRAILER = re.compile(r"Co-Authored-By:.*<[^>]*@anthropic\.com>", re.IGNORECASE)

HOLDS_RELPATH = "tools/admin-bypass-holds.yaml"

# A check run that GitHub itself accepts as satisfying a required context.
# `neutral` and `skipped` are successes for this purpose — a path-filtered job
# reports `skipped` and GitHub treats the requirement as met, so grading them
# as failures would flag every docs-only merge.
SATISFYING_CONCLUSIONS = frozenset({"success", "neutral", "skipped"})

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


def ruleset_applies(detail: dict, ref: str) -> bool:
    """Whether `detail` is an active ruleset covering `ref`.

    Simplification: only checks for `ref` appearing literally in the include
    list — none of this org's rulesets currently use the `~ALL` /
    `~DEFAULT_BRANCH` special values on a branch target, so a repo that starts
    using them would need this extended.
    """
    if detail["enforcement"] != "active":
        return False
    includes = detail.get("conditions", {}).get("ref_name", {}).get("include", [])
    return ref in includes


def ruleset_review_requirement(detail: dict, ref: str) -> int | None:
    """Approvals `detail` demands on `ref`, or None if it does not apply.

    A code-owner requirement scores as one approval even when the ruleset's
    own count is zero. GitHub folds CODEOWNERS into `reviewDecision`, so a PR
    needing an owner's sign-off cannot read APPROVED without one; scoring the
    flag as zero would rate an owner-gated ruleset as demanding nothing and
    silently exempt every path it protects from this audit.
    """
    if not ruleset_applies(detail, ref):
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


def ruleset_required_contexts(detail: dict, ref: str) -> set[str] | None:
    """Status-check contexts `detail` demands on `ref`, or None if it does not apply.

    Returns an empty set for a ruleset that applies but carries no
    `required_status_checks` rule — distinct from None, which means the
    ruleset has nothing to say about this ref at all.
    """
    if not ruleset_applies(detail, ref):
        return None

    contexts: set[str] = set()
    for rule in detail["rules"]:
        if rule["type"] != "required_status_checks":
            continue
        for check in rule["parameters"]["required_status_checks"]:
            contexts.add(check["context"])
    return contexts


def required_gates(repo: str, ref: str = TARGET_REF) -> tuple[int, set[str]]:
    """Strictest approval count and the union of required contexts covering `ref`.

    One pass over the repo's rulesets serves both halves. Approvals take the
    maximum because GitHub enforces the strictest; contexts take the union
    because every applicable ruleset's checks must pass, not just one's.
    """
    rulesets = gh_json(["api", f"repos/jdwlabs/{repo}/rulesets"])
    counts: list[int] = []
    contexts: set[str] = set()
    for rs in rulesets:
        detail = gh_json(["api", f"repos/jdwlabs/{repo}/rulesets/{rs['id']}"])
        required = ruleset_review_requirement(detail, ref)
        if required is not None:
            counts.append(required)
        found = ruleset_required_contexts(detail, ref)
        if found is not None:
            contexts |= found
    if not counts:
        raise ToolError(f"{repo}: no active pull_request-rule ruleset covers {ref}")
    return max(counts), contexts


def latest_check_conclusions(runs: list[dict]) -> dict[str, str | None]:
    """Collapse a commit's check runs to one conclusion per name.

    A re-run adds a second check run under the same name rather than replacing
    the first, and GitHub evaluates the requirement against the newest. Taking
    the first match instead would let a since-fixed failure condemn a merge
    that was green when it landed.
    """
    latest: dict[str, dict] = {}
    for run in runs:
        name = run["name"]
        current = latest.get(name)
        if current is None or (run.get("started_at") or "") >= (
            current.get("started_at") or ""
        ):
            latest[name] = run
    return {name: run.get("conclusion") for name, run in latest.items()}


def unsatisfied_contexts(required: set[str], conclusions: dict[str, str | None]) -> list[str]:
    """Required contexts that did not succeed, as `name=<conclusion|MISSING>`.

    A context with no check run at all is `MISSING`, and that is the more
    serious of the two shapes: a red check is a decision to merge anyway, an
    absent one means the gate never even reported.
    """
    unsatisfied = []
    for context in sorted(required):
        if context not in conclusions:
            unsatisfied.append(f"{context}=MISSING")
            continue
        conclusion = conclusions[context]
        if conclusion not in SATISFYING_CONCLUSIONS:
            unsatisfied.append(f"{context}={conclusion or 'INCOMPLETE'}")
    return unsatisfied


def check_conclusions_for(repo: str, sha: str) -> dict[str, str | None]:
    """Every check run on `sha`, collapsed to one conclusion per name.

    Deliberately not `commits/<sha>/status`: this org posts check runs and no
    legacy statuses, so that endpoint answers `pending` with an empty
    `statuses` array however green the commit is.
    """
    payload = gh_json(
        ["api", f"repos/jdwlabs/{repo}/commits/{sha}/check-runs?per_page=100"]
    )
    return latest_check_conclusions(payload.get("check_runs", []))


def load_holds(holds_file: Path) -> dict[tuple[str, int], str]:
    """Parse the holds file into {(repo, pr number): reason}."""
    try:
        import yaml
    except ModuleNotFoundError as exc:  # pragma: no cover - environment guard
        raise ToolError(f"PyYAML is required to read {HOLDS_RELPATH}: {exc}") from exc

    try:
        document = yaml.safe_load(holds_file.read_text()) or {}
    except FileNotFoundError as exc:
        raise ToolError(f"{HOLDS_RELPATH} is missing: {exc}") from exc
    except yaml.YAMLError as exc:
        raise ToolError(f"{HOLDS_RELPATH} is not valid YAML: {exc}") from exc

    entries = document.get("holds") or []
    if not isinstance(entries, list):
        raise ToolError(f"{HOLDS_RELPATH}: 'holds' must be a list of entries.")

    holds: dict[tuple[str, int], str] = {}
    for entry in entries:
        if not isinstance(entry, dict):
            raise ToolError(f"{HOLDS_RELPATH}: every hold must be a mapping.")
        repo = entry.get("repo")
        number = entry.get("pr")
        reason = entry.get("reason")
        if repo not in REPOS:
            raise ToolError(
                f"{HOLDS_RELPATH}: 'repo' must be one of {', '.join(REPOS)}, got {repo!r}."
            )
        if not isinstance(number, int):
            raise ToolError(f"{HOLDS_RELPATH}: {repo} hold needs an integer 'pr'.")
        if not reason or not str(reason).strip():
            raise ToolError(
                f"{HOLDS_RELPATH}: {repo}#{number} needs a 'reason' saying why the "
                f"bypass was intended and what closes it."
            )
        holds[(repo, number)] = str(reason).strip()
    return holds


def merged_prs_since(repo: str, since: date, ref: str = TARGET_REF) -> list[dict]:
    prs = gh_json(
        [
            "pr", "list", "--repo", f"jdwlabs/{repo}",
            "--search", f"merged:>={since.isoformat()}", "--state", "merged",
            "--limit", str(LIST_LIMIT),
            "--json", "number,title,url,mergedAt,reviewDecision,baseRefName,headRefOid",
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


def audit(since: date, holds: dict[tuple[str, int], str], repos: list[str]) -> dict:
    """Classify every agent-authored merge in the window. Raises ToolError."""
    routine: list[tuple[str, dict]] = []
    reportable: list[tuple[str, dict, list[str]]] = []
    seen: set[tuple[str, int]] = set()
    held: set[tuple[str, int]] = set()
    per_repo: list[str] = []
    total_agent = 0

    for repo in repos:
        required_reviews, required_contexts = required_gates(repo)
        agent_prs = [pr for pr in merged_prs_since(repo, since) if is_agent_authored(pr)]
        total_agent += len(agent_prs)

        repo_routine = 0
        repo_reportable = 0
        for pr in agent_prs:
            key = (repo, pr["number"])
            seen.add(key)
            if required_reviews < 1 or pr["reviewDecision"] == "APPROVED":
                continue
            unsatisfied = unsatisfied_contexts(
                required_contexts, check_conclusions_for(repo, pr["headRefOid"])
            )
            if not unsatisfied:
                repo_routine += 1
                routine.append((repo, pr))
                continue
            if key in holds:
                held.add(key)
                continue
            repo_reportable += 1
            reportable.append((repo, pr, unsatisfied))

        per_repo.append(
            f"{repo}: required_reviews={required_reviews} "
            f"required_checks={len(required_contexts)} "
            f"agent-authored={len(agent_prs)} "
            f"routine-unapproved={repo_routine} reportable={repo_reportable}"
        )

    # A hold covering a PR this window never fetched is simply not evaluated:
    # holds are keyed on a PR number, and every PR eventually falls out of a
    # rolling --since window. Failing on those would make the file impossible
    # to keep clean. A hold whose PR *was* inspected and did not need holding
    # is a different thing — an exception outliving the condition it excused —
    # and that is a finding, the same way prd-drift treats a stale hold.
    stale = sorted(key for key in holds if key in seen and key not in held)

    return {
        "per_repo": per_repo,
        "routine": routine,
        "reportable": reportable,
        "held": sorted(held),
        "stale": stale,
        "total_agent": total_agent,
    }


def render(result: dict, since: date, holds: dict[tuple[str, int], str], list_flagged: bool) -> None:
    for line in result["per_repo"]:
        print(line)

    reportable = result["reportable"]
    routine = result["routine"]
    print(
        f"\nTOTAL reportable/routine-unapproved/agent-authored: "
        f"{len(reportable)}/{len(routine)}/{result['total_agent']} "
        f"since {since.isoformat()}"
    )
    print("(JDWLABS-181 success metric 1 target: 0 routine admin-bypass merges)")

    if result["held"]:
        print(f"\nHeld (declared in {HOLDS_RELPATH}, not reported):")
        for repo, number in result["held"]:
            print(f"  {repo}#{number}  {holds[(repo, number)]}")

    if reportable:
        print("\nREPORTABLE — merged unapproved with a required check unsatisfied:")
        for repo, pr, unsatisfied in reportable:
            print(f"  {repo}#{pr['number']}  {pr['title']}  {pr['url']}")
            print(f"      unsatisfied: {', '.join(unsatisfied)}")

    if result["stale"]:
        print("\nSTALE HOLDS — inspected in this window and not reportable:")
        for repo, number in result["stale"]:
            print(f"  {repo}#{number}  {holds[(repo, number)]}")
        print(f"Remove them from {HOLDS_RELPATH}; an exception must not outlive its condition.")

    if list_flagged and routine:
        print("\nRoutine unapproved merges (all required checks green — not a finding):")
        for repo, pr in routine:
            print(f"  {repo}#{pr['number']}  {pr['title']}  {pr['url']}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        formatter_class=argparse.RawDescriptionHelpFormatter, description=__doc__
    )
    parser.add_argument(
        "--since", type=parse_since, default=None,
        help="only count PRs merged on or after this date (default: 7 days ago)",
    )
    parser.add_argument(
        "--list", action="store_true",
        help="also print every routine unapproved merge, not just the reportable ones",
    )
    parser.add_argument(
        "--repo", action="append", choices=REPOS, default=None, metavar="REPO",
        help="audit only this repo; repeatable (default: all four)",
    )
    parser.add_argument(
        "--holds", type=Path, default=None,
        help=f"path to the holds file (default: {HOLDS_RELPATH} beside this script)",
    )
    args = parser.parse_args(argv)
    since = args.since or (date.today() - timedelta(days=7))
    holds_file = args.holds or (Path(__file__).resolve().parent.parent / HOLDS_RELPATH)

    try:
        holds = load_holds(holds_file)
        result = audit(since, holds, args.repo or REPOS)
    except ToolError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    render(result, since, holds, args.list)
    return 1 if (result["reportable"] or result["stale"]) else 0


if __name__ == "__main__":
    sys.exit(main())
