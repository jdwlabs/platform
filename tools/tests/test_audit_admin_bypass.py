#!/usr/bin/env python3
"""Regression tests for tools/audit-admin-bypass.py's pure functions.

Run with:
    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import contextlib
import importlib.util
import io
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location(
    "audit_admin_bypass", TOOLS_DIR / "audit-admin-bypass.py"
)
audit = importlib.util.module_from_spec(_spec)
sys.modules["audit_admin_bypass"] = audit
_spec.loader.exec_module(audit)


def _pr(*message_bodies: str) -> dict:
    return {"commits": [{"messageBody": m} for m in message_bodies]}


class IsAgentAuthoredTests(unittest.TestCase):
    def test_matches_the_established_trailer(self):
        pr = _pr("some commit\n\nCo-Authored-By: Claude Opus 5 <noreply@anthropic.com>")
        self.assertTrue(audit.is_agent_authored(pr))

    def test_case_insensitive(self):
        pr = _pr("co-authored-by: Claude <noreply@ANTHROPIC.com>")
        self.assertTrue(audit.is_agent_authored(pr))

    def test_any_commit_in_the_pr_counts(self):
        pr = _pr(
            "human commit, no trailer",
            "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>",
        )
        self.assertTrue(audit.is_agent_authored(pr))

    def test_no_trailer_is_not_agent_authored(self):
        pr = _pr("a perfectly ordinary human commit")
        self.assertFalse(audit.is_agent_authored(pr))

    def test_co_authored_by_a_human_does_not_match(self):
        pr = _pr("Co-Authored-By: Jake Willmsen <jdwillmsen@gmail.com>")
        self.assertFalse(audit.is_agent_authored(pr))

    def test_no_commits_is_not_agent_authored(self):
        self.assertFalse(audit.is_agent_authored({"commits": []}))

    def test_missing_commits_key_is_not_agent_authored(self):
        self.assertFalse(audit.is_agent_authored({}))

    def test_none_message_body_does_not_raise(self):
        # gh occasionally returns null rather than "" for an empty body —
        # is_agent_authored's `or ""` guard exists for exactly this.
        pr = {"commits": [{"messageBody": None}]}
        self.assertFalse(audit.is_agent_authored(pr))


class ParseSinceTests(unittest.TestCase):
    def test_valid_date_parses(self):
        import datetime

        self.assertEqual(audit.parse_since("2026-08-07"), datetime.date(2026, 8, 7))

    def test_invalid_date_raises_argument_type_error(self):
        import argparse

        with self.assertRaises(argparse.ArgumentTypeError):
            audit.parse_since("not-a-date")


def _ruleset(
    *,
    count: int = 0,
    code_owner: bool = False,
    enforcement: str = "active",
    includes: tuple[str, ...] = ("refs/heads/main",),
    rule_type: str = "pull_request",
) -> dict:
    return {
        "enforcement": enforcement,
        "conditions": {"ref_name": {"include": list(includes), "exclude": []}},
        "rules": [
            {
                "type": rule_type,
                "parameters": {
                    "required_approving_review_count": count,
                    "require_code_owner_review": code_owner,
                },
            }
        ],
    }


class RulesetReviewRequirementTests(unittest.TestCase):
    REF = "refs/heads/main"

    def test_plain_approval_count_is_reported(self):
        got = audit.ruleset_review_requirement(_ruleset(count=1), self.REF)
        self.assertEqual(got, 1)

    def test_code_owner_gate_with_zero_count_still_requires_one(self):
        # The shape both real owner-gates use; scored as 0 this audit would
        # exempt every path they protect.
        got = audit.ruleset_review_requirement(
            _ruleset(count=0, code_owner=True), self.REF
        )
        self.assertEqual(got, 1)

    def test_code_owner_gate_does_not_lower_a_higher_count(self):
        got = audit.ruleset_review_requirement(
            _ruleset(count=2, code_owner=True), self.REF
        )
        self.assertEqual(got, 2)

    def test_zero_count_without_code_owner_stays_zero(self):
        got = audit.ruleset_review_requirement(_ruleset(count=0), self.REF)
        self.assertEqual(got, 0)

    def test_inactive_ruleset_does_not_apply(self):
        got = audit.ruleset_review_requirement(
            _ruleset(count=1, enforcement="disabled"), self.REF
        )
        self.assertIsNone(got)

    def test_ruleset_not_covering_the_ref_does_not_apply(self):
        got = audit.ruleset_review_requirement(
            _ruleset(count=1, includes=("refs/heads/release/**",)), self.REF
        )
        self.assertIsNone(got)

    def test_ruleset_without_a_pull_request_rule_does_not_apply(self):
        got = audit.ruleset_review_requirement(
            _ruleset(count=1, rule_type="deletion"), self.REF
        )
        self.assertIsNone(got)


def _full_ruleset(
    *,
    count: int = 1,
    contexts: tuple[str, ...] = (),
    enforcement: str = "active",
    includes: tuple[str, ...] = ("refs/heads/main",),
) -> dict:
    """A ruleset carrying both a pull_request rule and required status checks."""
    return {
        "enforcement": enforcement,
        "conditions": {"ref_name": {"include": list(includes), "exclude": []}},
        "rules": [
            {
                "type": "pull_request",
                "parameters": {
                    "required_approving_review_count": count,
                    "require_code_owner_review": False,
                },
            },
            {
                "type": "required_status_checks",
                "parameters": {
                    "required_status_checks": [{"context": c} for c in contexts]
                },
            },
        ],
    }


class RulesetRequiredContextsTests(unittest.TestCase):
    REF = "refs/heads/main"

    def test_contexts_are_collected(self):
        got = audit.ruleset_required_contexts(
            _full_ruleset(contexts=("go-lint", "helm-lint")), self.REF
        )
        self.assertEqual(got, {"go-lint", "helm-lint"})

    def test_applicable_ruleset_without_status_checks_is_an_empty_set(self):
        # Distinct from None: the ruleset covers the ref and simply demands no
        # checks. Returning None here would drop a real approval requirement.
        got = audit.ruleset_required_contexts(_ruleset(count=1), self.REF)
        self.assertEqual(got, set())

    def test_inactive_ruleset_does_not_apply(self):
        got = audit.ruleset_required_contexts(
            _full_ruleset(contexts=("go-lint",), enforcement="disabled"), self.REF
        )
        self.assertIsNone(got)

    def test_ruleset_not_covering_the_ref_does_not_apply(self):
        got = audit.ruleset_required_contexts(
            _full_ruleset(contexts=("go-lint",), includes=("refs/heads/release/**",)),
            self.REF,
        )
        self.assertIsNone(got)


class LatestCheckConclusionsTests(unittest.TestCase):
    def test_newest_run_of_a_name_wins(self):
        # A re-run adds a second check run under the same name rather than
        # replacing the first; grading on the older one would condemn a merge
        # that was green when it landed.
        runs = [
            {"name": "go-lint", "conclusion": "failure", "started_at": "2026-08-19T01:00:00Z"},
            {"name": "go-lint", "conclusion": "success", "started_at": "2026-08-19T02:00:00Z"},
        ]
        self.assertEqual(audit.latest_check_conclusions(runs), {"go-lint": "success"})

    def test_missing_started_at_does_not_raise(self):
        runs = [{"name": "go-lint", "conclusion": "success"}]
        self.assertEqual(audit.latest_check_conclusions(runs), {"go-lint": "success"})

    def test_no_runs_is_empty(self):
        self.assertEqual(audit.latest_check_conclusions([]), {})


class SplitRequiredTests(unittest.TestCase):
    def test_success_satisfies(self):
        self.assertEqual(audit.split_required({"go-lint"}, {"go-lint": "success"}), ([], []))

    def test_neutral_and_skipped_satisfy(self):
        # A path-filtered job reports `skipped` and GitHub treats the
        # requirement as met; grading it as a failure would flag every
        # docs-only merge.
        got = audit.split_required(
            {"go-lint", "helm-lint"}, {"go-lint": "neutral", "helm-lint": "skipped"}
        )
        self.assertEqual(got, ([], []))

    def test_failure_is_a_failed_context(self):
        self.assertEqual(
            audit.split_required({"go-lint"}, {"go-lint": "failure"}),
            (["go-lint=failure"], []),
        )

    def test_still_running_check_is_a_failed_context(self):
        self.assertEqual(
            audit.split_required({"go-lint"}, {"go-lint": None}),
            (["go-lint=INCOMPLETE"], []),
        )

    def test_absent_check_run_is_absent_not_failed(self):
        # The whole point of the split: absence is weaker evidence than a red
        # check and has to be judged against the window, not on its own.
        self.assertEqual(
            audit.split_required({"signatures / signatures"}, {"go-lint": "success"}),
            ([], ["signatures / signatures"]),
        )

    def test_no_required_contexts_is_always_satisfied(self):
        self.assertEqual(audit.split_required(set(), {}), ([], []))


class FirstProducedTests(unittest.TestCase):
    def test_earliest_producer_wins(self):
        prs = [
            {"mergedAt": "2026-08-19T10:00:00Z", "conclusions": {"go-lint": "success"}},
            {"mergedAt": "2026-08-17T10:00:00Z", "conclusions": {"go-lint": "success"}},
        ]
        self.assertEqual(audit.first_produced(prs), {"go-lint": "2026-08-17T10:00:00Z"})

    def test_a_context_nobody_produced_is_absent_from_the_map(self):
        prs = [{"mergedAt": "2026-08-19T10:00:00Z", "conclusions": {"go-lint": "success"}}]
        self.assertNotIn("signatures / signatures", audit.first_produced(prs))

    def test_a_failing_run_still_proves_the_check_was_running(self):
        prs = [{"mergedAt": "2026-08-17T10:00:00Z", "conclusions": {"go-lint": "failure"}}]
        self.assertEqual(audit.first_produced(prs), {"go-lint": "2026-08-17T10:00:00Z"})


class JudgeAbsentTests(unittest.TestCase):
    LATER = "2026-08-19T10:00:00Z"
    EARLIER = "2026-08-17T10:00:00Z"

    def test_absent_with_a_live_peer_is_reportable(self):
        got = audit.judge_absent(["sig"], {"sig": self.EARLIER}, self.LATER)
        self.assertEqual(got, (["sig=MISSING"], []))

    def test_absent_with_no_peer_at_all_is_unevaluable(self):
        # The check did not exist yet — 43 of 63 apps merges looked like this.
        got = audit.judge_absent(["sig"], {}, self.LATER)
        self.assertEqual(got, ([], ["sig"]))

    def test_a_peer_that_only_merged_later_does_not_convict(self):
        # A check introduced mid-window is produced by later PRs and none of
        # the earlier ones; "present anywhere" would flag every merge before it.
        got = audit.judge_absent(["sig"], {"sig": self.LATER}, self.EARLIER)
        self.assertEqual(got, ([], ["sig"]))

    def test_a_peer_merged_at_the_same_instant_does_not_convict(self):
        got = audit.judge_absent(["sig"], {"sig": self.LATER}, self.LATER)
        self.assertEqual(got, ([], ["sig"]))

    def test_nothing_absent_is_nothing_judged(self):
        self.assertEqual(audit.judge_absent([], {"sig": self.EARLIER}, self.LATER), ([], []))


def _write_holds(body: str) -> Path:
    handle = tempfile.NamedTemporaryFile(
        "w", suffix=".yaml", delete=False, encoding="utf-8"
    )
    handle.write(body)
    handle.close()
    return Path(handle.name)


class LoadHoldsTests(unittest.TestCase):
    def test_empty_holds_list_parses(self):
        self.assertEqual(audit.load_holds(_write_holds("holds: []\n")), {})

    def test_a_valid_hold_parses(self):
        path = _write_holds(
            "holds:\n"
            "  - repo: platform\n"
            "    pr: 299\n"
            "    reason: merged during an incident, tracked elsewhere\n"
        )
        self.assertEqual(
            audit.load_holds(path),
            {("platform", 299): "merged during an incident, tracked elsewhere"},
        )

    def test_a_hold_without_a_reason_is_rejected(self):
        path = _write_holds("holds:\n  - repo: platform\n    pr: 299\n")
        with self.assertRaises(audit.ToolError):
            audit.load_holds(path)

    def test_a_hold_naming_an_unknown_repo_is_rejected(self):
        path = _write_holds(
            "holds:\n  - repo: nope\n    pr: 1\n    reason: because\n"
        )
        with self.assertRaises(audit.ToolError):
            audit.load_holds(path)

    def test_a_hold_without_an_integer_pr_is_rejected(self):
        path = _write_holds(
            "holds:\n  - repo: platform\n    pr: '299'\n    reason: because\n"
        )
        with self.assertRaises(audit.ToolError):
            audit.load_holds(path)

    def test_holds_must_be_a_list(self):
        path = _write_holds("holds: not-a-list\n")
        with self.assertRaises(audit.ToolError):
            audit.load_holds(path)


AGENT_COMMIT = {
    "messageBody": "body\n\nCo-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
}

GREEN = {"go-lint": "success", "signatures / signatures": "success"}
MISSING_SIGNATURES = {"go-lint": "success"}
RED = {"go-lint": "failure", "signatures / signatures": "success"}


def _pr_fixture(number, merged_at, conclusions, review="REVIEW_REQUIRED"):
    return {
        "number": number,
        "title": f"pull request {number}",
        "url": f"https://github.com/jdwlabs/platform/pull/{number}",
        "mergedAt": merged_at,
        "reviewDecision": review,
        "baseRefName": "main",
        "headRefOid": f"sha{number}",
        "_conclusions": conclusions,
    }


def _fake_gh(prs: list[dict]):
    """A gh_json stand-in for one repo and an arbitrary set of merged PRs."""

    def dispatch(args: list[str]) -> object:
        joined = " ".join(args)
        if joined.endswith("/rulesets"):
            return [{"id": 1}]
        if "/rulesets/1" in joined:
            return _full_ruleset(count=1, contexts=("go-lint", "signatures / signatures"))
        if args[0] == "pr" and args[1] == "list":
            return [{k: v for k, v in pr.items() if k != "_conclusions"} for pr in prs]
        if args[0] == "pr" and args[1] == "view":
            return {"commits": [AGENT_COMMIT]}
        if "/check-runs" in joined:
            sha = joined.split("/commits/")[1].split("/")[0]
            pr = next(p for p in prs if p["headRefOid"] == sha)
            return {
                "check_runs": [
                    {"name": n, "conclusion": c, "started_at": pr["mergedAt"]}
                    for n, c in pr["_conclusions"].items()
                ]
            }
        raise AssertionError(f"unexpected gh call: {joined}")

    return dispatch


def _run_main(gh, holds_body: str) -> tuple[int, str]:
    holds = _write_holds(holds_body)
    buffer = io.StringIO()
    with unittest.mock.patch.object(audit, "gh_json", gh):
        with contextlib.redirect_stdout(buffer):
            code = audit.main(["--repo", "platform", "--holds", str(holds), "--since", "2026-08-01"])
    return code, buffer.getvalue()


# A peer that merged earlier and produced every required check — this is what
# turns an absent context on a later PR into evidence rather than a guess.
PEER = _pr_fixture(300, "2026-08-17T10:00:00Z", GREEN)


class VerdictTests(unittest.TestCase):
    """The whole point of the rewrite: a finding and a clean run must differ."""

    def test_unapproved_but_fully_green_is_routine_and_exits_zero(self):
        # 187 of 200 merged PRs org-wide are unapproved. Failing on this shape
        # would fire on almost every merge and be filtered within a week.
        code, out = _run_main(
            _fake_gh([PEER, _pr_fixture(299, "2026-08-19T10:00:00Z", GREEN)]), "holds: []\n"
        )
        self.assertEqual(code, 0)
        self.assertIn("routine-unapproved=2", out)
        self.assertIn("reportable=0", out)

    def test_approved_merge_is_not_counted_at_all(self):
        code, out = _run_main(
            _fake_gh([
                PEER,
                _pr_fixture(299, "2026-08-19T10:00:00Z", MISSING_SIGNATURES, review="APPROVED"),
            ]),
            "holds: []\n",
        )
        self.assertEqual(code, 0)
        self.assertIn("reportable=0", out)

    def test_absent_context_with_a_live_peer_is_reportable(self):
        # The real platform#299 shape: peers carry `signatures / signatures`,
        # this merge does not.
        code, out = _run_main(
            _fake_gh([PEER, _pr_fixture(299, "2026-08-19T10:00:00Z", MISSING_SIGNATURES)]),
            "holds: []\n",
        )
        self.assertEqual(code, 1)
        self.assertIn("REPORTABLE", out)
        self.assertIn("signatures / signatures=MISSING", out)

    def test_absent_context_with_no_peer_is_exempt_and_reported_unevaluable(self):
        # The check did not exist yet. Judging this as a bypass flagged 43 of
        # 63 apps merges; it must be exempt, and it must not read as a pass.
        code, out = _run_main(
            _fake_gh([
                _pr_fixture(298, "2026-08-17T10:00:00Z", MISSING_SIGNATURES),
                _pr_fixture(299, "2026-08-19T10:00:00Z", MISSING_SIGNATURES),
            ]),
            "holds: []\n",
        )
        self.assertEqual(code, 0)
        self.assertNotIn("REPORTABLE", out)
        self.assertIn("unevaluable", out)
        self.assertIn("signatures / signatures", out)

    def test_a_single_pr_window_infers_nothing_from_absence(self):
        code, out = _run_main(
            _fake_gh([_pr_fixture(299, "2026-08-19T10:00:00Z", MISSING_SIGNATURES)]),
            "holds: []\n",
        )
        self.assertEqual(code, 0)
        self.assertIn("unevaluable", out)

    def test_a_failed_required_check_is_reportable_regardless_of_peers(self):
        # Present-and-red needs no liveness test: the check demonstrably ran.
        code, out = _run_main(
            _fake_gh([_pr_fixture(290, "2026-08-19T10:00:00Z", RED)]), "holds: []\n"
        )
        self.assertEqual(code, 1)
        self.assertIn("go-lint=failure", out)

    def test_a_declared_hold_suppresses_the_finding(self):
        code, out = _run_main(
            _fake_gh([PEER, _pr_fixture(299, "2026-08-19T10:00:00Z", MISSING_SIGNATURES)]),
            "holds:\n"
            "  - repo: platform\n"
            "    pr: 299\n"
            "    reason: signatures job was being replaced; tracked separately\n",
        )
        self.assertEqual(code, 0)
        self.assertIn("Held", out)
        self.assertNotIn("REPORTABLE", out)

    def test_a_hold_over_a_clean_merge_is_stale_and_fails(self):
        # An exception must not outlive the condition it excused.
        code, out = _run_main(
            _fake_gh([PEER, _pr_fixture(299, "2026-08-19T10:00:00Z", GREEN)]),
            "holds:\n  - repo: platform\n    pr: 299\n    reason: no longer needed\n",
        )
        self.assertEqual(code, 1)
        self.assertIn("STALE HOLDS", out)

    def test_a_hold_for_a_pr_outside_the_window_is_inert(self):
        # PR-number holds cannot expire on their own, so one whose PR the
        # window never fetched is simply not evaluated rather than stale.
        code, out = _run_main(
            _fake_gh([PEER, _pr_fixture(299, "2026-08-19T10:00:00Z", GREEN)]),
            "holds:\n  - repo: apps\n    pr: 1\n    reason: long gone\n",
        )
        self.assertEqual(code, 0)
        self.assertNotIn("STALE HOLDS", out)

    def test_an_audit_that_cannot_run_exits_two_not_one(self):
        # "Found nothing" and "could not look" must not be the same exit code.
        def explode(args):
            raise audit.ToolError("gh api failed")

        holds = _write_holds("holds: []\n")
        buffer = io.StringIO()
        with unittest.mock.patch.object(audit, "gh_json", explode):
            with contextlib.redirect_stdout(buffer):
                with contextlib.redirect_stderr(io.StringIO()):
                    code = audit.main(["--repo", "platform", "--holds", str(holds)])
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
