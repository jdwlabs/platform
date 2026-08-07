#!/usr/bin/env python3
"""Regression tests for tools/check-adr-numbering.py.

Every test here corresponds to a duplicate the checker used to miss (or a
directory state it used to pass silently). Run with:

    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location(
    "check_adr_numbering", TOOLS_DIR / "check-adr-numbering.py"
)
checker = importlib.util.module_from_spec(_spec)
sys.modules["check_adr_numbering"] = checker
_spec.loader.exec_module(checker)


class CheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic docs/adr tree instead of this repo."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self._saved_adr_dir = checker.ADR_DIR
        checker.ADR_DIR = self.root / "docs/adr"
        self.addCleanup(self._restore)

    def _restore(self):
        checker.ADR_DIR = self._saved_adr_dir
        self._tmp.cleanup()

    def touch(self, *names: str) -> None:
        checker.ADR_DIR.mkdir(parents=True, exist_ok=True)
        for name in names:
            (checker.ADR_DIR / name).write_text("", encoding="utf-8")

    def test_unique_prefixes_pass(self):
        self.touch("0001-first.md", "0002-second.md", "README.md")
        self.assertEqual(checker.main(), 0)

    def test_same_width_duplicate_fails(self):
        self.touch("0011-out-of-band.md", "0011-tenant-model.md")
        self.assertEqual(checker.main(), 1)

    def test_mismatched_width_duplicate_fails(self):
        # 11 and 0011 name the same ADR number with different zero-padding —
        # the prior string-keyed implementation missed this.
        self.touch("11-legacy-rename.md", "0011-out-of-band.md")
        self.assertEqual(checker.main(), 1)

    def test_extra_leading_zero_duplicate_fails(self):
        self.touch("0012-alpha.md", "00012-beta.md")
        self.assertEqual(checker.main(), 1)

    def test_missing_adr_dir_fails(self):
        # No mkdir — the directory legitimately does not exist.
        self.assertEqual(checker.main(), 1)

    def test_empty_adr_dir_fails(self):
        checker.ADR_DIR.mkdir(parents=True, exist_ok=True)
        self.assertEqual(checker.main(), 1)

    def test_only_unnumbered_files_fails(self):
        self.touch("README.md", "TEMPLATE.md")
        self.assertEqual(checker.main(), 1)


if __name__ == "__main__":
    unittest.main()
