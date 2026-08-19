#!/usr/bin/env python3
"""Regression tests for tools/check-gitsync-tenant-folders.py.

Each test is a drift that is silent in the cluster: the apply Job exits 0 and
ArgoCD reports green while a tenant folder is world-readable, or while a
tenant's folder-rbac hook waits on a folder nothing will create. Run with:

    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import importlib.util
import json
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location(
    "check_gitsync_tenant_folders", TOOLS_DIR / "check-gitsync-tenant-folders.py"
)
checker = importlib.util.module_from_spec(_spec)
sys.modules["check_gitsync_tenant_folders"] = checker
_spec.loader.exec_module(checker)


def repository(name: str, path: str) -> str:
    return json.dumps(
        {
            "apiVersion": "provisioning.grafana.app/v0alpha1",
            "kind": "Repository",
            "metadata": {"name": name, "namespace": "default"},
            "spec": {
                "sync": {"target": "folder"},
                "github": {"path": path},
                "connection": {"name": "jdwlabs-platform-github"},
            },
        }
    )


class CheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic tree instead of this repo."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self._saved = (checker.REPO_ROOT, checker.APPLY_JOB, checker.RESOURCES, checker.TENANTS_DIR)
        checker.REPO_ROOT = self.root
        checker.APPLY_JOB = self.root / "apply-job.yaml"
        checker.RESOURCES = self.root / "resources.yaml"
        checker.TENANTS_DIR = self.root / "tenants"
        self.addCleanup(self._restore)

    def _restore(self):
        checker.REPO_ROOT, checker.APPLY_JOB, checker.RESOURCES, checker.TENANTS_DIR = self._saved
        self._tmp.cleanup()

    def write_job(self, required: str, tenant_entries: list) -> None:
        entries = "\n              ".join(tenant_entries)
        script = textwrap.dedent(
            f"""\
            set -eu
            REQUIRED_REPOSITORY="{required}"
            TENANT_REPOSITORIES="{entries}"
            """
        )
        checker.APPLY_JOB.write_text(
            yaml_job(script),
            encoding="utf-8",
        )

    def write_resources(self, definitions: dict) -> None:
        body = {"apiVersion": "v1", "kind": "ConfigMap", "data": definitions}
        import yaml as _yaml

        checker.RESOURCES.write_text(_yaml.safe_dump(body), encoding="utf-8")

    def write_tenant(self, name: str, git_sync_folder=None, dashboards=True) -> None:
        doc = {"name": name, "observability": {"tenantId": name, "grafana": {"folder": name}}}
        if git_sync_folder:
            doc["observability"]["grafana"]["gitSyncFolder"] = git_sync_folder
        import yaml as _yaml

        directory = checker.TENANTS_DIR / name
        directory.mkdir(parents=True, exist_ok=True)
        (directory / "tenant.yaml").write_text(_yaml.safe_dump(doc), encoding="utf-8")
        if dashboards:
            (self.root / "observability/dashboards" / name).mkdir(parents=True, exist_ok=True)

    def healthy_tree(self) -> None:
        self.write_job(
            "platform-dashboards:/etc/gitsync/repository.json",
            ["jdwlabs-dashboards:/etc/gitsync/repository-jdwlabs.json"],
        )
        self.write_resources(
            {
                "repository.json": repository("platform-dashboards", "observability/dashboards/platform"),
                "repository-jdwlabs.json": repository("jdwlabs-dashboards", "observability/dashboards/jdwlabs"),
            }
        )
        self.write_tenant("jdwlabs", "jdwlabs-dashboards")

    def test_agreeing_tree_passes(self):
        self.healthy_tree()
        self.assertEqual(checker.main(), 0)

    def test_repository_without_a_claiming_tenant_fails(self):
        # The world-readable case: folder created, nothing grants the team.
        self.healthy_tree()
        self.write_tenant("jdwlabs", git_sync_folder=None)
        self.assertEqual(checker.main(), 1)

    def test_claim_without_a_repository_fails(self):
        # The mirror image: the folder-rbac hook would wait, then fail.
        self.healthy_tree()
        self.write_tenant("dotablaze-tech", "dotablaze-tech-dashboards")
        self.assertEqual(checker.main(), 1)

    def test_name_disagreeing_with_definition_fails(self):
        self.healthy_tree()
        self.write_resources(
            {
                "repository.json": repository("platform-dashboards", "observability/dashboards/platform"),
                "repository-jdwlabs.json": repository("jdwlabs", "observability/dashboards/jdwlabs"),
            }
        )
        self.assertEqual(checker.main(), 1)

    def test_missing_configmap_key_fails(self):
        self.healthy_tree()
        self.write_resources(
            {"repository.json": repository("platform-dashboards", "observability/dashboards/platform")}
        )
        self.assertEqual(checker.main(), 1)

    def test_definition_no_list_creates_fails(self):
        self.healthy_tree()
        self.write_job("platform-dashboards:/etc/gitsync/repository.json", [])
        self.write_tenant("jdwlabs", git_sync_folder=None)
        self.assertEqual(checker.main(), 1)

    def test_folder_granted_to_a_tenant_but_syncing_another_path_fails(self):
        self.healthy_tree()
        self.write_resources(
            {
                "repository.json": repository("platform-dashboards", "observability/dashboards/platform"),
                "repository-jdwlabs.json": repository("jdwlabs-dashboards", "observability/dashboards/dotablaze-tech"),
            }
        )
        self.assertEqual(checker.main(), 1)

    def test_synced_path_missing_from_the_tree_fails(self):
        self.healthy_tree()
        (self.root / "observability/dashboards/jdwlabs").rmdir()
        self.assertEqual(checker.main(), 1)

    def test_two_tenants_claiming_one_folder_fails(self):
        self.healthy_tree()
        self.write_tenant("dotablaze-tech", "jdwlabs-dashboards")
        self.assertEqual(checker.main(), 1)

    def test_restructured_job_fails_loudly(self):
        # A renamed list must not read as "no tenant folders configured".
        self.healthy_tree()
        checker.APPLY_JOB.write_text(yaml_job('set -eu\nREPOSITORIES="a:/etc/gitsync/b.json"\n'), encoding="utf-8")
        with self.assertRaises(SystemExit):
            checker.main()


def yaml_job(script: str) -> str:
    import yaml as _yaml

    return _yaml.safe_dump(
        {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "spec": {"template": {"spec": {"containers": [{"name": "apply", "command": ["/bin/sh", "-c", script]}]}}},
        }
    )


if __name__ == "__main__":
    unittest.main()
