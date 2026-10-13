#!/usr/bin/env python3
"""Tests for tools/render-remote-charts.py.

No test reaches the network or runs helm; only the command construction and
the failure classification are covered here. The render itself is the CI job.
"""
import importlib.util
import subprocess
import unittest
from pathlib import Path
from unittest import mock

TOOLS_DIR = Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location(
    "render_remote_charts", TOOLS_DIR / "render-remote-charts.py"
)
renderer = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(renderer)


def release(**overrides):
    base = {
        "tenant": "platform", "name": "svc", "chart": "thing",
        "repo": "https://charts.example.com/", "revision": "1.2.3",
        "namespace": "ns", "overlay": "tenants/platform/services/missing/values.yaml",
    }
    return {**base, **overrides}


class ChartArgsTest(unittest.TestCase):
    def test_http_repo_uses_repo_flag_without_trailing_slash(self):
        self.assertEqual(
            renderer.chart_args(release()),
            ["thing", "--repo", "https://charts.example.com", "--version", "1.2.3"],
        )

    def test_bare_registry_path_is_an_oci_reference(self):
        self.assertEqual(
            renderer.chart_args(release(repo="ghcr.io/org/charts")),
            ["oci://ghcr.io/org/charts/thing", "--version", "1.2.3"],
        )


class RenderTest(unittest.TestCase):
    def run_render(self, **completed):
        done = subprocess.CompletedProcess([], completed.get("code", 0), completed.get("out", ""), completed.get("err", ""))
        with mock.patch.object(renderer.subprocess, "run", return_value=done) as run:
            return renderer.render(release(), "v1.34.0"), run.call_args.args[0]

    def test_nonzero_exit_fails_and_keeps_stderr(self):
        result, _ = self.run_render(code=1, err="Error: nil pointer")
        self.assertFalse(result["ok"])
        self.assertIn("nil pointer", result["detail"])

    def test_empty_render_fails_even_on_exit_zero(self):
        result, _ = self.run_render(out="  \n")
        self.assertFalse(result["ok"])

    def test_success_and_command_carries_release_namespace(self):
        result, command = self.run_render(out="kind: Pod\n")
        self.assertTrue(result["ok"])
        self.assertEqual(command[:3], ["helm", "template", "svc"])
        self.assertEqual(command[command.index("--namespace") + 1], "ns")
        self.assertNotIn("-f", command)

    def test_timeout_is_a_failure(self):
        with mock.patch.object(
            renderer.subprocess, "run", side_effect=subprocess.TimeoutExpired("helm", 1)
        ):
            self.assertFalse(renderer.render(release(), "v1.34.0")["ok"])


if __name__ == "__main__":
    unittest.main()
