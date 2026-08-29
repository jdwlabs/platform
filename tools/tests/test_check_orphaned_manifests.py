#!/usr/bin/env python3
"""Regression tests for tools/check-orphaned-manifests.py.

Each failing case is a path shape an auto-remediation PR actually proposed,
or a wiring gap the services ApplicationSet template leaves unread. Run with:

    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import importlib.util
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location(
    "check_orphaned_manifests", TOOLS_DIR / "check-orphaned-manifests.py"
)
checker = importlib.util.module_from_spec(_spec)
sys.modules["check_orphaned_manifests"] = checker
_spec.loader.exec_module(checker)

TENANT = textwrap.dedent(
    """
    name: platform
    services:
      - name: vault
        chart: vault
        postInstall: true
      - name: grafana
        chart: grafana
      - name: ai-sre-relay
        rawManifests: true
    """
)


class CheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic repository tree."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.addCleanup(self._tmp.cleanup)
        self.write("tenants/platform/tenant.yaml", TENANT)

    def write(self, rel: str, body: str = "kind: ConfigMap\n") -> None:
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body, encoding="utf-8")

    def orphans(self, allowlist: str | None = None):
        allow = self.root / "tools/orphaned-manifest-allowlist.yaml"
        if allowlist is not None:
            self.write(allow.relative_to(self.root).as_posix(), allowlist)
        return [p for p, _ in checker.find_orphans(self.root, allow)]

    def test_watched_layout_passes(self):
        self.write("bootstrap/root-app.yaml")
        self.write("bootstrap/crds/foo.yaml")
        self.write("helm-charts/tenant-envelope/templates/x.yaml")
        self.write("tenants/platform/services/vault/values.yaml")
        self.write("tenants/platform/services/vault/postInstall/externalsecret.yaml")
        self.write("tenants/platform/services/grafana/values.yaml")
        self.write("tenants/platform/services/ai-sre-relay/postInstall/deploy.yaml")
        self.write("tools/allowlist.yaml")
        self.write("tests/prometheus-rules/x_test.yaml")
        self.write(".github/workflows/validate.yml")
        self.write(".yamllint.yml")
        self.assertEqual(self.orphans(), [])

    def test_invented_top_level_directories_fail(self):
        for rel in (
            "manifests/vault-auto-unseal-cronjob.yaml",
            "cluster/grafana/gitsync.yaml",
            "clusters/jdwlabs/longhorn/instance-manager.yaml",
            "apps/jdwillmsen/minecraft/job.yaml",
            "argocd/values.yaml",
            "platform/vault/statefulset.yaml",
        ):
            self.write(rel)
        self.assertEqual(sorted(self.orphans()), sorted([
            "apps/jdwillmsen/minecraft/job.yaml",
            "argocd/values.yaml",
            "cluster/grafana/gitsync.yaml",
            "clusters/jdwlabs/longhorn/instance-manager.yaml",
            "manifests/vault-auto-unseal-cronjob.yaml",
            "platform/vault/statefulset.yaml",
        ]))

    def test_unlisted_release_fails(self):
        self.write("tenants/platform/services/longhorn/values.yaml")
        self.assertEqual(self.orphans(), ["tenants/platform/services/longhorn/values.yaml"])

    def test_post_install_without_flag_fails(self):
        self.write("tenants/platform/services/grafana/postInstall/dashboard.yaml")
        self.assertEqual(self.orphans(), ["tenants/platform/services/grafana/postInstall/dashboard.yaml"])

    def test_values_for_raw_manifests_release_fails(self):
        self.write("tenants/platform/services/ai-sre-relay/values.yaml")
        self.assertEqual(self.orphans(), ["tenants/platform/services/ai-sre-relay/values.yaml"])

    def test_nested_post_install_fails(self):
        self.write("tenants/platform/services/vault/postInstall/nested/x.yaml")
        self.assertEqual(self.orphans(), ["tenants/platform/services/vault/postInstall/nested/x.yaml"])

    def test_stray_yaml_beside_tenant_fails(self):
        self.write("tenants/platform/values.yaml")
        self.write("tenants/platform/services/vault/extra.yaml")
        self.assertEqual(sorted(self.orphans()), [
            "tenants/platform/services/vault/extra.yaml",
            "tenants/platform/values.yaml",
        ])

    def test_unknown_tenant_fails(self):
        self.write("tenants/ghost/services/vault/values.yaml")
        self.assertEqual(self.orphans(), ["tenants/ghost/services/vault/values.yaml"])

    def test_allowlisted_prefix_passes_with_reason(self):
        self.write("tenants/platform/services/arc-systems/values.yaml")
        self.write("tenants/platform/services/arc-systems/postInstall/role.yaml")
        self.write("tenants/platform/services/longhorn/values.yaml")
        got = self.orphans(textwrap.dedent(
            """
            allow:
              - path: tenants/platform/services/arc-systems
                reason: dormant, commented out in tenant.yaml
            """
        ))
        self.assertEqual(got, ["tenants/platform/services/longhorn/values.yaml"])

    def test_allowlist_entry_without_reason_is_refused(self):
        self.write("tenants/platform/services/arc-systems/values.yaml")
        with self.assertRaises(SystemExit):
            self.orphans("allow:\n  - path: tenants/platform/services/arc-systems\n")

    def test_main_reports_exit_code(self):
        saved = checker.REPO_ROOT, checker.ALLOWLIST
        checker.REPO_ROOT, checker.ALLOWLIST = self.root, self.root / "missing.yaml"
        try:
            self.assertEqual(checker.main(), 0)
            self.write("manifests/x.yaml")
            self.assertEqual(checker.main(), 1)
        finally:
            checker.REPO_ROOT, checker.ALLOWLIST = saved


if __name__ == "__main__":
    unittest.main()
