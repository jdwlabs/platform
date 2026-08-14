#!/usr/bin/env python3
"""Regression tests for tools/audit-admin-bypass.py's pure functions.

Run with:
    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import importlib.util
import sys
import unittest
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


if __name__ == "__main__":
    unittest.main()
