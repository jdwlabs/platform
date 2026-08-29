#!/usr/bin/env python3
"""Regression tests for tools/check-remote-chart-image-pins.py.

The anchor case is JDWLABS-369: chart democratic-csi 0.15.1 defaults its
controller and node driver image tags to `latest`, neither tenant release
overrode it, and tools/check-image-pins.py could not see it because the tag
lives in a remote chart's values.yaml rather than in this repo. Every test
here is either that shape or a way of getting it wrong.

No test reaches the network — chart defaults are injected.

Run with:

    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import importlib.util
import io
import sys
import tarfile
import tempfile
import textwrap
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent.parent


def _load(module_name: str, filename: str):
    spec = importlib.util.spec_from_file_location(module_name, TOOLS_DIR / filename)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


checker = _load("check_remote_chart_image_pins", "check-remote-chart-image-pins.py")
# The exact module object the checker calls into, not whichever copy of it the
# sibling test module happens to have left in sys.modules first.
pins = checker._pins

DIGEST = "sha256:" + "0" * 64

# The chart shape that motivated this check: the whole repository path lives
# in `registry`, there is no `repository` key, and the template renders
# "{{ .registry }}:{{ .tag }}".
DEMOCRATIC_CSI_DEFAULTS = {
    "values": {
        "controller": {"driver": {"image": {"registry": "ghcr.io/democratic-csi/democratic-csi", "tag": "latest"}}},
        "node": {"driver": {"image": {"registry": "ghcr.io/democratic-csi/democratic-csi", "tag": "latest"}}},
    },
    "appVersion": "1.9.5",
}


def chart_archive(chart: str, values: str, chart_yaml: str = "", extra: dict | None = None) -> bytes:
    """A real .tgz laid out the way a packaged Helm chart is."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        members = {f"{chart}/values.yaml": values}
        if chart_yaml:
            members[f"{chart}/Chart.yaml"] = chart_yaml
        members.update(extra or {})
        for name, body in members.items():
            payload = body.encode("utf-8")
            info = tarfile.TarInfo(name=name)
            info.size = len(payload)
            tar.addfile(info, io.BytesIO(payload))
    return buffer.getvalue()


class RemoteCheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic tenant tree and injected charts."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self._saved = (checker.REPO_ROOT, checker.ALLOWLIST_FILE)
        checker.REPO_ROOT = self.root
        checker.ALLOWLIST_FILE = self.root / "tools/remote-chart-image-pin-allowlist.yaml"
        self.charts = {}
        self.addCleanup(self._restore)
        self.write_allowlist("exceptions: []")

    def _restore(self):
        checker.REPO_ROOT, checker.ALLOWLIST_FILE = self._saved
        self._tmp.cleanup()

    def write(self, relpath: str, content: str) -> Path:
        path = self.root / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(content), encoding="utf-8")
        return path

    def write_allowlist(self, body: str) -> None:
        self.write("tools/remote-chart-image-pin-allowlist.yaml", body)

    def write_tenant(self, body: str, tenant: str = "platform") -> None:
        self.write(f"tenants/{tenant}/tenant.yaml", body)

    def fake_fetch(self, repo, chart, revision):
        try:
            return self.charts[(repo, chart, revision)]
        except KeyError:
            raise ValueError(f"chart '{chart}' is not published in {repo}") from None

    def run_checker(self):
        """Returns (exit_code, stdout)."""
        allowlist = pins.load_allowlist(checker.ALLOWLIST_FILE)
        releases = checker.discover_releases()
        checked, allowed, violations, errors, consumed = checker.collect(
            releases, allowlist, fetch=self.fake_fetch
        )
        stale = sorted(set(allowlist) - consumed)
        exit_code = 1 if (violations or errors or stale) else 0
        return exit_code, {
            "checked": checked,
            "allowed": allowed,
            "violations": [ref for _release, ref in violations],
            "errors": errors,
            "stale": stale,
        }

    def setup_democratic_csi(self, overlay_nfs: str, overlay_iscsi: str = "{}") -> None:
        repo = "https://democratic-csi.github.io/charts/"
        self.charts[(repo, "democratic-csi", "0.15.1")] = DEMOCRATIC_CSI_DEFAULTS
        self.write_tenant(
            """
            services:
              - name: democratic-csi
                chart: democratic-csi
                repo: https://democratic-csi.github.io/charts/
                revision: 0.15.1
              - name: democratic-csi-iscsi
                chart: democratic-csi
                repo: https://democratic-csi.github.io/charts/
                revision: 0.15.1
            """
        )
        self.write("tenants/platform/services/democratic-csi/values.yaml", overlay_nfs)
        self.write("tenants/platform/services/democratic-csi-iscsi/values.yaml", overlay_iscsi)


class Jdwlabs369(RemoteCheckerHarness):
    """The bug this check exists to catch."""

    def test_unpinned_latest_in_remote_chart_defaults_is_caught(self):
        self.setup_democratic_csi(overlay_nfs="csiDriver:\n  name: org.democratic-csi.nfs\n")
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 1)
        # Once per release: both overrode nothing, so both are exposed.
        self.assertEqual(
            result["violations"],
            ["ghcr.io/democratic-csi/democratic-csi:latest"] * 2,
        )

    def test_pinning_the_tag_in_the_overlay_clears_it(self):
        pinned = f"controller:\n  driver:\n    image:\n      tag: latest@{DIGEST}\nnode:\n  driver:\n    image:\n      tag: latest@{DIGEST}\n"
        self.setup_democratic_csi(overlay_nfs=pinned, overlay_iscsi=pinned)
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 0, result)
        self.assertEqual(result["violations"], [])

    def test_pinning_only_the_controller_still_reports_the_node(self):
        """Both keys default independently — pinning one must not mask the other."""
        self.setup_democratic_csi(
            overlay_nfs=f"controller:\n  driver:\n    image:\n      tag: latest@{DIGEST}\n",
            overlay_iscsi=f"controller:\n  driver:\n    image:\n      tag: latest@{DIGEST}\n"
            f"node:\n  driver:\n    image:\n      tag: latest@{DIGEST}\n",
        )
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["violations"], ["ghcr.io/democratic-csi/democratic-csi:latest"])

    def test_registry_only_image_block_is_extracted_at_all(self):
        """The chart has no `repository` key; keying on it alone saw nothing."""
        refs = pins.refs_in_tree(DEMOCRATIC_CSI_DEFAULTS["values"])
        self.assertEqual(
            sorted(r.full_ref for r in refs),
            ["ghcr.io/democratic-csi/democratic-csi:latest"] * 2,
        )


class MutableTagClassification(unittest.TestCase):
    def test_moving_names_are_mutable(self):
        for tag in ("latest", "main", "master", "stable", "edge", "nightly", "LATEST"):
            self.assertTrue(checker.is_mutable_tag(tag), tag)

    def test_suffixed_moving_names_are_mutable(self):
        for tag in ("stable-alpine", "latest-debian", "main-ubuntu"):
            self.assertTrue(checker.is_mutable_tag(tag), tag)

    def test_truncated_versions_are_mutable(self):
        for tag in ("1", "v1", "1.2", "v4.4"):
            self.assertTrue(checker.is_mutable_tag(tag), tag)

    def test_full_versions_are_not_mutable(self):
        for tag in ("1.2.3", "v4.4.0", "v3.6.11", "16.1.0-debian-11-r20", "0.0.31"):
            self.assertFalse(checker.is_mutable_tag(tag), tag)

    def test_a_release_candidate_version_is_not_confused_with_the_bare_word(self):
        self.assertTrue(checker.is_mutable_tag("rc"))
        self.assertFalse(checker.is_mutable_tag("1.2.3-rc1"))

    def test_empty_tag_is_mutable_before_appversion_resolution(self):
        self.assertTrue(checker.is_mutable_tag(""))
        self.assertTrue(checker.is_mutable_tag("   "))


class TagExtraction(unittest.TestCase):
    def test_registry_port_is_not_mistaken_for_a_tag(self):
        self.assertEqual(checker.tag_of("registry.local:5000/team/app"), "")
        self.assertEqual(checker.tag_of("registry.local:5000/team/app:1.2.3"), "1.2.3")

    def test_digest_is_stripped_before_reading_the_tag(self):
        self.assertEqual(checker.tag_of(f"repo/app:1.2.3@{DIGEST}"), "1.2.3")

    def test_tagless_reference_has_no_tag(self):
        self.assertEqual(checker.tag_of("docker.io/busybox"), "")


class AppVersionFallback(RemoteCheckerHarness):
    """`tag: ""` means "track .Chart.AppVersion", which the pinned chart
    revision fixes — judged on what it resolves to, not assumed the worst."""

    def _run_with(self, tag, app_version):
        repo = "https://example.invalid/charts"
        self.charts[(repo, "widget", "1.0.0")] = {
            "values": {"image": {"repository": "example.com/widget", "tag": tag}},
            "appVersion": app_version,
        }
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
            """
        )
        self.write("tenants/platform/services/widget/values.yaml", "{}")
        return self.run_checker()

    def test_empty_tag_with_a_concrete_appversion_is_not_reported(self):
        exit_code, result = self._run_with("", "v3.2.1")
        self.assertEqual(exit_code, 0, result)

    def test_empty_tag_with_no_appversion_is_reported(self):
        exit_code, result = self._run_with("", "")
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["violations"], ["example.com/widget"])

    def test_empty_tag_with_a_mutable_appversion_is_reported_as_resolved(self):
        exit_code, result = self._run_with("", "latest")
        self.assertEqual(exit_code, 1)
        # Reported with the tag it actually renders, not the empty source value.
        self.assertEqual(result["violations"], ["example.com/widget:latest"])

    def test_an_explicit_tag_ignores_appversion(self):
        exit_code, result = self._run_with("latest", "v3.2.1")
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["violations"], ["example.com/widget:latest"])


class OverlayMerge(unittest.TestCase):
    def test_maps_merge_key_by_key(self):
        merged = checker.deep_merge({"a": {"x": 1, "y": 2}}, {"a": {"y": 3}})
        self.assertEqual(merged, {"a": {"x": 1, "y": 3}})

    def test_lists_are_replaced_not_appended(self):
        merged = checker.deep_merge({"args": ["a", "b"]}, {"args": ["c"]})
        self.assertEqual(merged, {"args": ["c"]})

    def test_explicit_null_deletes_a_default(self):
        merged = checker.deep_merge({"image": {"tag": "latest"}}, {"image": None})
        self.assertEqual(merged, {})

    def test_the_base_is_not_mutated(self):
        base = {"a": {"x": 1}}
        checker.deep_merge(base, {"a": {"x": 2}})
        self.assertEqual(base, {"a": {"x": 1}})

    def test_overlay_only_keys_are_kept(self):
        self.assertEqual(checker.deep_merge({"a": 1}, {"b": 2}), {"a": 1, "b": 2})


class ReleaseDiscovery(RemoteCheckerHarness):
    def test_local_and_raw_services_are_skipped(self):
        self.write_tenant(
            """
            services:
              - name: remote-one
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
              - name: in-repo-chart
                repo: https://github.com/jdwlabs/platform.git
                chartPath: helm-charts/db-ui
                revision: main
              - name: raw
                rawManifests: true
            """
        )
        names = [r["name"] for r in checker.discover_releases()]
        self.assertEqual(names, ["remote-one"])

    def test_the_overlay_path_points_at_where_the_fix_goes(self):
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
            """,
            tenant="jdwlabs",
        )
        release = checker.discover_releases()[0]
        self.assertEqual(release["overlay"], "tenants/jdwlabs/services/widget/values.yaml")

    def test_a_missing_overlay_file_is_treated_as_no_overrides(self):
        repo = "https://example.invalid/charts"
        self.charts[(repo, "widget", "1.0.0")] = {
            "values": {"image": {"repository": "example.com/widget", "tag": "latest"}},
            "appVersion": "1.0.0",
        }
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
            """
        )
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["violations"], ["example.com/widget:latest"])


class UnreadableCharts(RemoteCheckerHarness):
    def test_a_fetch_failure_fails_the_run_rather_than_passing_quietly(self):
        """Silently trusting what could not be read is the failure this check
        exists to prevent, so an unfetchable chart is never a pass."""
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 9.9.9
            """
        )
        self.write("tenants/platform/services/widget/values.yaml", "{}")
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["checked"], 0)
        self.assertEqual(len(result["errors"]), 1)
        self.assertIn("not published", result["errors"][0][1])


class AllowlistBehaviour(RemoteCheckerHarness):
    def _widget(self, tag="latest"):
        repo = "https://example.invalid/charts"
        self.charts[(repo, "widget", "1.0.0")] = {
            "values": {"image": {"repository": "example.com/widget", "tag": tag}},
            "appVersion": "1.0.0",
        }
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
            """
        )
        self.write("tenants/platform/services/widget/values.yaml", "{}")

    def test_an_exempted_reference_passes(self):
        self._widget()
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/platform/services/widget/values.yaml
                ref: example.com/widget:latest
                reason: not-rendered, gated off by default
            """
        )
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 0, result)
        self.assertEqual(len(result["allowed"]), 1)

    def test_an_entry_for_a_different_tag_does_not_transfer(self):
        self._widget(tag="main")
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/platform/services/widget/values.yaml
                ref: example.com/widget:latest
                reason: not-rendered, gated off by default
            """
        )
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["violations"], ["example.com/widget:main"])
        self.assertEqual(result["stale"], [("tenants/platform/services/widget/values.yaml", "example.com/widget:latest")])

    def test_an_entry_that_no_longer_matches_is_stale(self):
        self._widget(tag="1.2.3")
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/platform/services/widget/values.yaml
                ref: example.com/widget:latest
                reason: not-rendered, gated off by default
            """
        )
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(result["violations"], [])
        self.assertEqual(len(result["stale"]), 1)

    def test_an_entry_without_a_reason_is_rejected(self):
        self._widget()
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/platform/services/widget/values.yaml
                ref: example.com/widget:latest
            """
        )
        with self.assertRaises(SystemExit):
            self.run_checker()


class ChartArchiveExtraction(unittest.TestCase):
    def test_values_and_appversion_are_read_from_the_top_level(self):
        archive = chart_archive(
            "widget",
            "image:\n  repository: example.com/widget\n  tag: latest\n",
            "name: widget\nversion: 1.0.0\nappVersion: v3.2.1\n",
        )
        result = checker.values_from_chart_archive(archive, "widget")
        self.assertEqual(result["appVersion"], "v3.2.1")
        self.assertEqual(result["values"]["image"]["tag"], "latest")

    def test_a_subchart_values_file_is_not_read_as_the_charts_own(self):
        archive = chart_archive(
            "widget",
            "image:\n  repository: example.com/widget\n  tag: 1.2.3\n",
            "name: widget\nversion: 1.0.0\nappVersion: v3.2.1\n",
            extra={"widget/charts/sub/values.yaml": "image:\n  repository: sub\n  tag: latest\n"},
        )
        result = checker.values_from_chart_archive(archive, "widget")
        self.assertEqual(result["values"]["image"]["tag"], "1.2.3")
        self.assertNotIn("repository", result["values"].get("sub", {}))

    def test_a_chart_without_an_appversion_yields_an_empty_string(self):
        archive = chart_archive(
            "widget", "image: {}\n", "name: widget\nversion: 1.0.0\n"
        )
        self.assertEqual(checker.values_from_chart_archive(archive, "widget")["appVersion"], "")

    def test_an_archive_without_values_is_an_error_not_an_empty_pass(self):
        archive = chart_archive("widget", "", "name: widget\n")
        # values.yaml exists but is empty -> parses to {}, which is legitimate.
        self.assertEqual(checker.values_from_chart_archive(archive, "widget")["values"], {})

    def test_a_numeric_tag_keeps_its_literal_text(self):
        """`tag: 1.10` must not become the float 1.1 and read as pinned."""
        archive = chart_archive(
            "widget",
            "image:\n  repository: example.com/widget\n  tag: 1.10\n",
            "name: widget\nversion: 1.0.0\n",
        )
        result = checker.values_from_chart_archive(archive, "widget")
        self.assertEqual(result["values"]["image"]["tag"], "1.10")


class DigestAnchor(RemoteCheckerHarness):
    def test_a_digest_pins_even_a_latest_tag(self):
        repo = "https://example.invalid/charts"
        self.charts[(repo, "widget", "1.0.0")] = {
            "values": {"image": {"repository": "example.com/widget", "tag": f"latest@{DIGEST}"}},
            "appVersion": "1.0.0",
        }
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
            """
        )
        self.write("tenants/platform/services/widget/values.yaml", "{}")
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 0, result)

    def test_a_separate_digest_key_also_pins(self):
        repo = "https://example.invalid/charts"
        self.charts[(repo, "widget", "1.0.0")] = {
            "values": {
                "image": {"repository": "example.com/widget", "tag": "latest", "digest": DIGEST}
            },
            "appVersion": "1.0.0",
        }
        self.write_tenant(
            """
            services:
              - name: widget
                chart: widget
                repo: https://example.invalid/charts
                revision: 1.0.0
            """
        )
        self.write("tenants/platform/services/widget/values.yaml", "{}")
        exit_code, result = self.run_checker()
        self.assertEqual(exit_code, 0, result)


class RepositoryAllowlistIntegrity(unittest.TestCase):
    """The committed allowlist must parse and carry a reason on every entry."""

    def test_committed_allowlist_is_well_formed(self):
        allowlist = pins.load_allowlist(checker.ALLOWLIST_FILE)
        self.assertTrue(allowlist, "expected the committed allowlist to have entries")
        for (path, ref), entry in allowlist.items():
            self.assertTrue(path.startswith("tenants/"), path)
            self.assertTrue(entry["reason"].strip(), ref)


if __name__ == "__main__":
    unittest.main()
