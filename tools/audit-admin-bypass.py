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
    either ran and did not succeed, or is absent while the window shows it was
    running. Minus anything covered by a declared hold in
    tools/admin-bypass-holds.yaml.

Absence needs that liveness test, and without it this audit is unusable. The
required contexts are read *live*, so every PR that merged before a check was
introduced reads as "missing" it — and this org introduces checks often.
Measured over a 21-day window, treating any absent context as a finding
flagged 43 of 63 `apps` merges, 22 of 100 on `deployments` and 15 of 61 on
`infrastructure`, and every single one of those was a check that did not exist
at merge time rather than a gate anyone stepped over. The docstring warning
below — that a ruleset changed since a PR merged is judged against today's
rule — is precisely the trap.

So an absent context is only a finding when some other PR in the same window
produced it *before* this one merged. That is the window calibrating itself:
no ruleset history to reconstruct, and a check that did not exist yet has no
earlier producer. An absent context that no earlier PR produced is reported as
`unevaluable` rather than passed over in silence.

Required-check state is read from the check-runs API, never from the legacy
combined-status endpoint: this org posts only check runs, so
`commits/<sha>/status` reports `state: "pending"` with an empty `statuses`
array on a commit whose every check succeeded. A tool reading that would
grade every merge as bypassed.

What `deployments`' zero does and does not do
--------------------------------------------

`deployments`' Baseline requires zero approvals. That zero is a decision with
a narrow purpose rather than a policy that the repo needs no review: ADR 0018
§1 records that a release-bot pull request dropped
`required_approving_review_count` to 0 on Baseline and narrowed CODEOWNERS so
the bot's own non-owned-path changes needed no reviewer, and concludes the
surrounding bypass grant "looks vestigial, not intentional".

It does **not** exempt the repo from this audit, and it is worth being precise
because the opposite is easy to assume. The requirement is the maximum across
every ruleset covering the ref, and `deployments`' "PRD Promotion Review Gate"
carries `require_code_owner_review: true`, which scores as one approval — so
the effective requirement resolves to 1 and every unapproved merge there is
evaluated. Verified live: `required_gates("deployments")` returns 1, from
Baseline 0 / PRD Promotion Review Gate 1 / Production Gates 0.

The inaccuracy runs the other way. That owner gate only covers the CODEOWNERS
paths the promotion workflow touches (`values-prd.yaml`, `argocd/prd/`), while
this tool applies the requirement to every PR on the ref — so a merge on an
unowned path is judged against a rule that did not actually apply to it. That
is deliberate: `reviewDecision` already folds CODEOWNERS in, and scoring the
flag as zero would exempt every path an owner-gated ruleset protects. It makes
this audit stricter than the rules on some paths, never blinder.

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


def split_required(
    required: set[str], conclusions: dict[str, str | None]
) -> tuple[list[str], list[str]]:
    """Split `required` into contexts that ran and failed, and contexts absent.

    The two are not the same evidence and must not be judged the same way. A
    context that ran and did not succeed is a red gate someone merged over, and
    it says so on its own. A context with no check run at all says only that
    nothing of that name reported — which is what a bypass looks like, and also
    what a check that did not exist yet looks like.
    """
    failed, absent = [], []
    for context in sorted(required):
        if context not in conclusions:
            absent.append(context)
            continue
        conclusion = conclusions[context]
        if conclusion not in SATISFYING_CONCLUSIONS:
            failed.append(f"{context}={conclusion or 'INCOMPLETE'}")
    return failed, absent


def first_produced(prs: list[dict]) -> dict[str, str]:
    """Earliest `mergedAt` at which any PR in the window produced each context.

    This is the window's own evidence about which checks were actually running,
    and it is what keeps an absent context from being read as a bypass by
    default. Required contexts are read live, so every PR merged before a check
    was introduced reads as "missing" it — measured over 21 days that mistake
    flagged 43 of 63 `apps` merges and 22 of 100 on `deployments`, every one of
    them a check that simply did not exist yet.
    """
    earliest: dict[str, str] = {}
    for pr in prs:
        merged_at = pr.get("mergedAt") or ""
        for context, conclusion in pr.get("conclusions", {}).items():
            if context not in earliest or merged_at < earliest[context]:
                earliest[context] = merged_at
    return earliest


def judge_absent(
    absent: list[str], earliest: dict[str, str], merged_at: str
) -> tuple[list[str], list[str]]:
    """Split absent contexts into reportable misses and unevaluable ones.

    A context is *live* for this PR when some other PR in the window produced
    it **before** this one merged. Then its absence here is a real miss: the
    check was running, and this merge does not have it.

    Deliberately "at least one earlier producer" rather than a majority of the
    window. The two shapes that need exempting are a check that did not exist
    yet and a check introduced mid-window, and both are temporal — neither has
    an earlier producer, whatever the majority says. A majority test would
    instead exempt a genuinely live check whenever the window happened to be
    dominated by merges that skipped it, which is the failure that matters more
    here: it hides real misses rather than adding noise. Measured across all
    four repos, the two rules pick the same findings, so the stricter reading
    costs nothing.

    Earlier, not merely present: a check introduced mid-window is produced by
    later PRs and by none of the earlier ones, and reading "present anywhere"
    would flag every merge that preceded it.

    A context absent here and never produced by any earlier PR is not judged at
    all. It is reported as unevaluable rather than dropped — an audit that
    cannot reach a verdict on a context has to say so, the same way a run that
    could not execute exits 2 instead of 0.
    """
    reportable, unevaluable = [], []
    for context in absent:
        seen_at = earliest.get(context)
        if seen_at is not None and seen_at < merged_at:
            reportable.append(f"{context}=MISSING")
        else:
            unevaluable.append(context)
    return reportable, unevaluable


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
    unevaluable: dict[str, set[str]] = {}
    seen: set[tuple[str, int]] = set()
    held: set[tuple[str, int]] = set()
    per_repo: list[str] = []
    total_agent = 0

    for repo in repos:
        required_reviews, required_contexts = required_gates(repo)
        prs = merged_prs_since(repo, since)

        # Check-run state is fetched for *every* merged PR in the window, not
        # only the agent-authored ones, because these are also the peers that
        # establish which required checks were running at all. A human PR
        # carrying `signatures / signatures` is proof the check existed just as
        # good as an agent one.
        for pr in prs:
            pr["conclusions"] = check_conclusions_for(repo, pr["headRefOid"])
        earliest = first_produced(prs)

        agent_prs = [pr for pr in prs if is_agent_authored(pr)]
        total_agent += len(agent_prs)

        repo_routine = 0
        repo_reportable = 0
        repo_unevaluable: set[str] = set()
        for pr in agent_prs:
            key = (repo, pr["number"])
            seen.add(key)
            if required_reviews < 1 or pr["reviewDecision"] == "APPROVED":
                continue
            failed, absent = split_required(required_contexts, pr["conclusions"])
            missed, not_judged = judge_absent(
                absent, earliest, pr.get("mergedAt") or ""
            )
            repo_unevaluable |= set(not_judged)
            findings = failed + missed
            if not findings:
                repo_routine += 1
                routine.append((repo, pr))
                continue
            if key in holds:
                held.add(key)
                continue
            repo_reportable += 1
            reportable.append((repo, pr, findings))

        if repo_unevaluable:
            unevaluable[repo] = repo_unevaluable

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
        "unevaluable": unevaluable,
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

    for repo, contexts in sorted(result["unevaluable"].items()):
        for context in sorted(contexts):
            print(
                f"\nunevaluable: {repo} '{context}' (no PR in this window produced "
                f"it before the merges missing it — cannot tell a bypass from a "
                f"check that was not running yet)"
            )

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
