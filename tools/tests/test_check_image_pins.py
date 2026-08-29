#!/usr/bin/env python3
"""Regression tests for tools/check-image-pins.py.

This file is vendored byte-for-byte alongside the checker in every repo that
runs it, so it must not assume a repo layout: the checker is located by
walking up from this file, and every test builds its own throwaway repo
tree with its own config. Each test pins one behaviour the check previously
got wrong in one repo or the other — a crash, a false negative that let a
mutable reference through, or an exemption that outlived the reference it
was written for. Run with either of:

    python3 -m unittest discover -s tools -p 'test_*.py'
    python3 -m unittest discover -s tools/tests -t tools/tests
"""
import importlib.util
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path


def _locate_checker() -> Path:
    here = Path(__file__).resolve().parent
    for directory in (here, here.parent):
        candidate = directory / "check-image-pins.py"
        if candidate.exists():
            return candidate
    raise FileNotFoundError("check-image-pins.py not found next to or above this test file")


MODULE_PATH = _locate_checker()
_spec = importlib.util.spec_from_file_location("check_image_pins", MODULE_PATH)
cip = importlib.util.module_from_spec(_spec)
sys.modules["check_image_pins"] = cip
_spec.loader.exec_module(cip)

DIGEST_A = "sha256:" + "a" * 64
DIGEST_B = "sha256:" + "b" * 64
PINNED_TAG = f"1.0.0@{DIGEST_A}"
OTHER_DIGEST = f"@{DIGEST_B}"
PINNED_NGINX = f"docker.io/library/nginx:1.29.4@{DIGEST_A}"

BASE_VALUES = textwrap.dedent(
    """\
    image:
      repository: jdwlabs/app
      pullPolicy: IfNotPresent
      tag: ""
    """
)

CHARTS_CONFIG = """\
    allowlist: tools/image-pin-allowlist.yaml
    sources:
      - kind: helm-overlays
        charts: charts
      - kind: tree
        globs:
          - charts/*/templates/**/*
    """

TREE_CONFIG = """\
    allowlist: tools/image-pin-allowlist.yaml
    sources:
      - kind: tree
        globs:
          - tenants/**/*.yaml
          - tenants/**/*.yml
          - helm-charts/**/*.yaml
          - helm-charts/**/*.yml
          - helm-charts/**/templates/**/*.tpl
    """


class RepoFixture:
    """Builds a throwaway repo tree in whichever shape a test needs."""

    def __init__(self, root: Path, config: str):
        self.root = root
        self.write(cip.CONFIG_RELPATH, config)

    def write(self, relpath: str, body: str) -> "RepoFixture":
        path = self.root / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(body).lstrip("\n"), encoding="utf-8")
        return self

    def allowlist(self, body: str) -> "RepoFixture":
        return self.write(cip.DEFAULT_ALLOWLIST_RELPATH, body)

    def check(self) -> cip.Report:
        return cip.check(self.root)

    def violations(self) -> list[str]:
        return [r.full_ref for r in self.check().violations]

    # --- the charts/<chart>/values-<env>.yaml layout ---------------------

    def chart(self, name: str, base: str = BASE_VALUES, library: bool = False) -> "RepoFixture":
        chart_dir = self.root / "charts" / name
        chart_dir.mkdir(parents=True, exist_ok=True)
        kind = "type: library\n" if library else ""
        (chart_dir / "Chart.yaml").write_text(f"name: {name}\nversion: 0.1.0\n{kind}", encoding="utf-8")
        if base is not None:
            (chart_dir / "values.yaml").write_text(base, encoding="utf-8")
        return self

    def overlay(self, chart: str, env: str, body: str) -> "RepoFixture":
        return self.write(f"charts/{chart}/values-{env}.yaml", body)

    def template(self, chart: str, name: str, body: str) -> "RepoFixture":
        return self.write(f"charts/{chart}/templates/{name}", body)


class FixtureTest(unittest.TestCase):
    CONFIG = CHARTS_CONFIG

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = RepoFixture(Path(self._tmp.name), self.CONFIG)

    def rendered(self, report):
        return [r.rendered for r in report.violations]


# ===========================================================================
# Chart overlays (the charts/<chart>/values-<env>.yaml layout)
# ===========================================================================


class ChartOverlayBaseline(FixtureTest):
    def test_digest_pinned_tag_passes(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        report = self.repo.check()
        self.assertTrue(report.ok, cip.render(report))
        self.assertEqual(1, len(report.pinned))

    def test_report_renders_a_definitive_success_line(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.assertIn("0 issues", cip.render(self.repo.check()))

    def test_library_chart_needs_no_overlay(self):
        self.repo.chart("common", base=None, library=True)
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.assertTrue(self.repo.check().ok)


class UnquotedTags(FixtureTest):
    """An unquoted `tag: 1.0` parses as a float in Helm's own YAML parser
    as well as here. One repo's checker crashed on it (TypeError on
    re.search); the other reported it as an ordinary violation showing the
    lossy `1.1`. It is malformed: the literal text is preserved in the
    report, and no allowlist entry can excuse it."""

    def test_unquoted_float_tag_is_reported_not_raised(self):
        self.repo.chart("app").overlay("app", "prd", "image:\n  tag: 1.0\n")
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(1, len(report.malformed))
        self.assertIn("unquoted YAML number", report.malformed[0].problem)

    def test_float_tag_keeps_its_literal_text(self):
        self.repo.chart("app").overlay("app", "prd", "image:\n  tag: 1.10\n")
        report = self.repo.check()
        self.assertEqual("1.10", report.malformed[0].tag)
        self.assertEqual("jdwlabs/app:1.10", report.malformed[0].full_ref)

    def test_unquoted_int_tag_is_reported_not_raised(self):
        self.repo.chart("app").overlay("app", "prd", "image:\n  tag: 3\n")
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(1, len(report.malformed))

    def test_unquoted_numeric_tag_is_not_allowlistable(self):
        self.repo.chart("app").overlay("app", "prd", "image:\n  tag: 1.0\n")
        self.repo.allowlist(
            """\
            exceptions:
              - path: charts/app/values-prd.yaml
                ref: "jdwlabs/app:1.0"
                reason: trying to excuse a malformed reference
            """
        )
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(1, len(report.malformed))
        self.assertEqual([], report.allowed)

    def test_quoted_numeric_tag_is_an_ordinary_violation(self):
        self.repo.chart("app").overlay("app", "prd", 'image:\n  tag: "1.10"\n')
        report = self.repo.check()
        self.assertEqual([], report.malformed)
        self.assertEqual(["jdwlabs/app:1.10"], self.rendered(report))

    def test_as_str_never_raises_on_non_string_scalars(self):
        self.assertEqual(cip.as_str(1.0), "1.0")
        self.assertEqual(cip.as_str(17), "17")
        self.assertEqual(cip.as_str(None), "")
        self.assertEqual(cip.as_str(True), "true")


class OverlayDiscovery(FixtureTest):
    """Environments were once hard-coded to non/prd, so a values-dev.yaml
    was silently unexamined."""

    def test_unlisted_environment_overlay_is_checked(self):
        self.repo.chart("app").overlay("app", "dev", 'image:\n  tag: "latest"\n')
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(["jdwlabs/app:latest"], self.rendered(report))

    def test_every_overlay_is_checked_independently(self):
        self.repo.chart("app")
        self.repo.overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.overlay("app", "dev", 'image:\n  tag: "latest"\n')
        self.repo.overlay("app", "qa", 'image:\n  tag: "nightly"\n')
        report = self.repo.check()
        self.assertEqual(
            {"charts/app/values-dev.yaml", "charts/app/values-qa.yaml"},
            {r.path for r in report.violations},
        )

    def test_chart_with_no_overlay_is_reported_not_skipped(self):
        self.repo.chart("app")
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(["charts/app"], report.unchecked_charts)


class NestedReferencesInOverlays(FixtureTest):
    """Only the top-level image key was once read."""

    def test_sidecar_image_map_is_checked(self):
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            sidecar:
              image:
                repository: jdwlabs/sidecar
                tag: latest
            """,
        )
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(["jdwlabs/sidecar:latest"], self.rendered(report))
        self.assertEqual("sidecar.image", report.violations[0].where)

    def test_raw_image_string_is_checked(self):
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            initContainer:
              image: busybox:latest
            """,
        )
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(["busybox:latest"], self.rendered(report))

    def test_image_reference_inside_a_list_is_checked(self):
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            extraContainers:
              - name: proxy
                image: envoyproxy/envoy:v1.29
            """,
        )
        self.assertEqual(["envoyproxy/envoy:v1.29"], self.rendered(self.repo.check()))

    def test_nested_image_map_without_a_tag_key_is_checked(self):
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            sidecar:
              image:
                repository: jdwlabs/sidecar
            """,
        )
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(1, len(report.violations))

    def test_pinned_nested_reference_passes(self):
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            sidecar:
              image: "busybox:1.37{OTHER_DIGEST}"
            """,
        )
        self.assertTrue(self.repo.check().ok)

    def test_registry_port_is_not_mistaken_for_a_tag(self):
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            sidecar:
              image: registry.local:5000/team/tool:v2
            """,
        )
        report = self.repo.check()
        self.assertEqual("registry.local:5000/team/tool", report.violations[0].repository)
        self.assertEqual("v2", report.violations[0].tag)
        self.assertTrue(cip.has_tag_or_digest("registry.local:5000/team/app:v1"))
        self.assertFalse(cip.has_tag_or_digest("registry.local:5000/team/app"))


class MalformedReferences(FixtureTest):
    """A digest-only tag passes a naive "contains a digest" test but renders
    as `repo:@sha256:…`, which only the kubelet rejects."""

    def test_digest_only_tag_is_rejected(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{OTHER_DIGEST}"\n')
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual([], report.pinned)
        self.assertEqual(1, len(report.malformed))
        self.assertIn("digest-only tag", report.malformed[0].problem)

    def test_digest_only_tag_is_not_allowlistable(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{OTHER_DIGEST}"\n')
        self.repo.allowlist(
            f"""\
            exceptions:
              - path: charts/app/values-prd.yaml
                ref: "jdwlabs/app:{OTHER_DIGEST}"
                reason: trying to excuse an unpullable reference
            """
        )
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual([], report.allowed)

    def test_raw_reference_with_a_digest_and_no_tag_is_valid(self):
        # `repo@sha256:…` is well-formed only when the digest arrived
        # already embedded in one raw string. The structured {repository,
        # tag} map and the sibling-scalar image/tag shape both render into
        # junk when the tag is digest-only — see
        # test_digest_only_tag_is_rejected and
        # test_digest_only_sibling_tag_is_malformed.
        self.repo.chart("app").overlay(
            "app",
            "prd",
            f"""\
            image:
              tag: "{PINNED_TAG}"
            sidecar:
              image: "busybox{OTHER_DIGEST}"
            """,
        )
        report = self.repo.check()
        self.assertTrue(report.ok, cip.render(report))
        self.assertIn(f"busybox{OTHER_DIGEST}", [r.full_ref for r in report.pinned])

    def test_truncated_digest_is_rejected(self):
        self.repo.chart("app").overlay("app", "prd", 'image:\n  tag: "1.0.0@sha256:abc123"\n')
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(1, len(report.malformed))
        self.assertIn("digest component", report.malformed[0].problem)

    def test_tag_starting_with_a_dot_is_rejected(self):
        self.repo.chart("app").overlay("app", "prd", 'image:\n  tag: ".1.0.0"\n')
        report = self.repo.check()
        self.assertEqual(1, len(report.malformed))
        self.assertIn("not a valid OCI tag", report.malformed[0].problem)


class OverlayAllowlistKeying(FixtureTest):
    """The allowlist key was once path-only, so an exemption kept applying
    after the reference it excused changed."""

    ALLOWLIST_FOR_EMPTY_TAG = """\
        exceptions:
          - path: charts/app/values-non.yaml
            ref: jdwlabs/app
            reason: non deliberately tracks Chart.appVersion under pullPolicy Always
        """

    def test_allowlist_entry_covers_the_reference_it_describes(self):
        self.repo.chart("app").overlay("app", "non", "image:\n  pullPolicy: Always\n")
        self.repo.allowlist(self.ALLOWLIST_FOR_EMPTY_TAG)
        report = self.repo.check()
        self.assertTrue(report.ok, cip.render(report))
        self.assertEqual(1, len(report.allowed))

    def test_allowlist_entry_does_not_cover_a_changed_repository(self):
        self.repo.chart("app").overlay(
            "app", "non", "image:\n  repository: attacker/other\n  pullPolicy: Always\n"
        )
        self.repo.allowlist(self.ALLOWLIST_FOR_EMPTY_TAG)
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(
            ["attacker/other (no tag — falls back to Chart.appVersion)"], self.rendered(report)
        )

    def test_allowlist_entry_does_not_cover_a_changed_tag(self):
        self.repo.chart("app").overlay("app", "non", 'image:\n  tag: "latest"\n')
        self.repo.allowlist(self.ALLOWLIST_FOR_EMPTY_TAG)
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(["jdwlabs/app:latest"], self.rendered(report))

    def test_allowlist_entry_does_not_leak_to_another_environment(self):
        self.repo.chart("app")
        self.repo.overlay("app", "non", "image:\n  pullPolicy: Always\n")
        self.repo.overlay("app", "prd", 'image:\n  tag: ""\n')
        self.repo.allowlist(self.ALLOWLIST_FOR_EMPTY_TAG)
        report = self.repo.check()
        self.assertEqual(["charts/app/values-prd.yaml"], [r.path for r in report.violations])

    def test_unused_allowlist_entry_is_stale(self):
        self.repo.chart("app").overlay("app", "non", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.allowlist(self.ALLOWLIST_FOR_EMPTY_TAG)
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual([("charts/app/values-non.yaml", "jdwlabs/app")], report.stale)

    def test_allowlist_entry_without_a_ref_is_rejected(self):
        self.repo.chart("app").overlay("app", "non", "image:\n  pullPolicy: Always\n")
        self.repo.allowlist(
            """\
            exceptions:
              - path: charts/app/values-non.yaml
                repository: jdwlabs/app
                tag: ""
                reason: the repository + tag shape this check no longer accepts
            """
        )
        with self.assertRaises(SystemExit) as caught:
            self.repo.check()
        self.assertIn("'ref'", str(caught.exception))

    def test_allowlist_entry_without_a_reason_is_rejected(self):
        self.repo.chart("app").overlay("app", "non", "image:\n  pullPolicy: Always\n")
        self.repo.allowlist(
            """\
            exceptions:
              - path: charts/app/values-non.yaml
                ref: jdwlabs/app
                reason: "   "
            """
        )
        with self.assertRaises(SystemExit) as caught:
            self.repo.check()
        self.assertIn("reason", str(caught.exception))

    def test_allowlist_entry_with_an_unquoted_numeric_ref_is_rejected(self):
        self.repo.chart("app").overlay("app", "non", 'image:\n  tag: "1.0"\n')
        self.repo.allowlist(
            """\
            exceptions:
              - path: charts/app/values-non.yaml
                ref: 1.0
                reason: an unquoted ref here would never key against anything
            """
        )
        with self.assertRaises(SystemExit):
            self.repo.check()

    def test_duplicate_allowlist_entries_are_rejected(self):
        self.repo.chart("app").overlay("app", "non", "image:\n  pullPolicy: Always\n")
        self.repo.allowlist(
            """\
            exceptions:
              - path: charts/app/values-non.yaml
                ref: jdwlabs/app
                reason: first entry
              - path: charts/app/values-non.yaml
                ref: jdwlabs/app
                reason: second entry for the same reference
            """
        )
        with self.assertRaises(SystemExit):
            self.repo.check()


class ChartTemplates(FixtureTest):
    def test_literal_template_image_is_checked(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.template("app", "job.yaml", "spec:\n  containers:\n    - image: busybox\n")
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual(["busybox (no tag — implicit :latest)"], self.rendered(report))
        self.assertEqual("charts/app/templates/job.yaml:3", report.violations[0].location)

    def test_templated_image_line_is_left_to_the_values_scan(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.template(
            "app",
            "deployment.yaml",
            'spec:\n  containers:\n    - image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"\n',
        )
        self.assertTrue(self.repo.check().ok)

    def test_image_pull_policy_line_is_not_an_image_reference(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.template("app", "deployment.yaml", "spec:\n  imagePullPolicy: Always\n")
        self.assertTrue(self.repo.check().ok)

    def test_library_chart_templates_are_still_scanned(self):
        self.repo.chart("common", base=None, library=True)
        self.repo.template("common", "_test.yaml", "spec:\n  containers:\n    - image: busybox\n")
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        report = self.repo.check()
        self.assertEqual(["charts/common/templates/_test.yaml"], [r.path for r in report.violations])

    def test_tpl_partials_are_scanned(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.template("app", "_helpers.tpl", "spec:\n  containers:\n    - image: busybox\n")
        report = self.repo.check()
        self.assertEqual(["charts/app/templates/_helpers.tpl"], [r.path for r in report.violations])

    def test_commented_template_image_value_is_trimmed(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.template("app", "job.yaml", "spec:\n  containers:\n    - image: busybox:1.37  # wget\n")
        report = self.repo.check()
        self.assertEqual("busybox", report.violations[0].repository)
        self.assertEqual("1.37", report.violations[0].tag)

    def test_suffixed_image_key_in_a_template_is_checked(self):
        self.repo.chart("app").overlay("app", "prd", f'image:\n  tag: "{PINNED_TAG}"\n')
        self.repo.template("app", "job.yaml", "spec:\n  initImage: busybox:1.37\n")
        self.assertEqual(["busybox:1.37"], self.repo.violations())


class ValuesMergeSemantics(FixtureTest):
    def test_overlay_inherits_the_base_repository(self):
        self.repo.chart("app").overlay("app", "prd", 'image:\n  tag: "latest"\n')
        self.assertEqual("jdwlabs/app", self.repo.check().violations[0].repository)

    def test_overlay_that_never_mentions_image_still_yields_a_reference(self):
        self.repo.chart("app").overlay("app", "prd", "replicaCount: 2\n")
        report = self.repo.check()
        self.assertEqual(1, len(report.violations))
        self.assertEqual("charts/app/values-prd.yaml", report.violations[0].path)

    def test_overlay_null_tag_falls_back_like_helm(self):
        self.repo.chart("app", base=BASE_VALUES.replace('tag: ""', 'tag: "1.2.3"'))
        self.repo.overlay("app", "prd", "image:\n  tag: null\n")
        self.assertEqual("", self.repo.check().violations[0].tag)


# ===========================================================================
# Tree scans (the tenants/ + helm-charts/ layout)
# ===========================================================================


class TreeTest(FixtureTest):
    CONFIG = TREE_CONFIG


class TreeUnquotedTags(TreeTest):
    def test_unquoted_float_tag_is_malformed_with_its_literal_text(self):
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            """
            image:
              repository: docker.io/library/nginx
              tag: 1.10
            """,
        )
        report = self.repo.check()
        self.assertFalse(report.ok)
        self.assertEqual([], report.violations)
        self.assertEqual(["docker.io/library/nginx:1.10"], [r.full_ref for r in report.malformed])

    def test_unquoted_integer_tag_is_malformed(self):
        self.repo.write(
            "tenants/probe/services/svc/postInstall/job.yaml",
            """
            image:
              repository: docker.io/library/postgres
              tag: 17
            """,
        )
        report = self.repo.check()
        self.assertEqual(["docker.io/library/postgres:17"], [r.full_ref for r in report.malformed])

    def test_unquoted_sibling_tag_is_malformed(self):
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            """
            db:
              dbReadyImage: docker.io/library/postgres
              dbReadyTag: 17
            """,
        )
        report = self.repo.check()
        self.assertEqual(["docker.io/library/postgres:17"], [r.full_ref for r in report.malformed])

    def test_digest_only_sibling_tag_is_malformed(self):
        # A digest-only value in the sibling-scalar shape (image/tag as two
        # separate keys, e.g. litellm's dbReadyImage/dbReadyTag) renders
        # exactly the same unpullable "repo:@sha256:..." the structured
        # {repository, tag} map produces — it must be rejected the same way,
        # not read as a bare `repo@sha256:...` digest reference.
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            f"""
            db:
              dbReadyImage: docker.io/library/postgres
              dbReadyTag: "{OTHER_DIGEST}"
            """,
        )
        report = self.repo.check()
        self.assertEqual([], report.pinned)
        self.assertEqual(1, len(report.malformed))
        self.assertIn("digest-only tag", report.malformed[0].problem)


class GlobCoverage(TreeTest):
    def test_helm_chart_values_are_scanned(self):
        self.repo.write(
            "helm-charts/db-ui/values.yaml",
            """
            image:
              repository: adminer
              tag: "5.5.0"
            """,
        )
        self.assertEqual(["adminer:5.5.0"], self.repo.violations())

    def test_vendored_subchart_values_are_scanned(self):
        self.repo.write(
            "helm-charts/litellm-helm/charts/redis/values.yaml",
            """
            image:
              registry: docker.io
              repository: bitnami/redis
              tag: 7.2.4-debian-12-r9
            """,
        )
        self.assertEqual(["docker.io/bitnami/redis:7.2.4-debian-12-r9"], self.repo.violations())

    def test_yml_extension_is_scanned(self):
        self.repo.write(
            "tenants/probe/services/svc/postInstall/job.yml",
            """
            apiVersion: batch/v1
            kind: Job
            spec:
              template:
                spec:
                  containers:
                    - name: c
                      image: flyway/flyway:11.2
            """,
        )
        self.assertEqual(["flyway/flyway:11.2"], self.repo.violations())

    def test_tenant_files_outside_the_services_layout_are_scanned(self):
        self.repo.write(
            "tenants/probe/extras/nested/deep/manifest.yaml",
            """
            apiVersion: v1
            kind: Pod
            spec:
              containers:
                - name: c
                  image: busybox:1.36
            """,
        )
        self.assertEqual(["busybox:1.36"], self.repo.violations())

    def test_files_outside_the_globs_are_not_scanned(self):
        self.repo.write("docs/example.yaml", "image: busybox:latest\n")
        self.assertTrue(self.repo.check().ok)

    def test_hardcoded_image_in_a_go_templated_chart_file_is_caught(self):
        self.repo.write(
            "helm-charts/probe/templates/tests/test-connection.yaml",
            """
            apiVersion: v1
            kind: Pod
            metadata:
              name: "{{ include "probe.fullname" . }}-test"
            spec:
              containers:
                - name: wget
                  image: busybox
            """,
        )
        self.assertEqual(["busybox"], self.repo.violations())

    def test_templated_image_expression_is_not_reported(self):
        self.repo.write(
            "helm-charts/probe/templates/deployment.yaml",
            """
            spec:
              containers:
                - name: c
                  image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
            """,
        )
        report = self.repo.check()
        self.assertTrue(report.ok, cip.render(report))

    def test_commented_template_image_line_is_still_caught(self):
        self.repo.write(
            "helm-charts/probe/templates/job.yaml",
            "spec:\n  containers:\n    - image: busybox:1.37  # wget\n",
        )
        self.assertEqual(["busybox:1.37"], self.repo.violations())


class ImageKeyNames(TreeTest):
    def test_db_ready_image_is_found(self):
        self.repo.write(
            "tenants/probe/services/litellm/values.yaml",
            """
            db:
              dbReadyImage: docker.io/bitnamilegacy/postgresql
            """,
        )
        self.assertEqual(["docker.io/bitnamilegacy/postgresql"], self.repo.violations())

    def test_init_and_sidecar_image_keys_are_found(self):
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            """
            initImage: docker.io/library/busybox:1.36
            sidecarImage: docker.io/library/envoy:v1.31
            init_image: docker.io/library/alpine:3.20
            """,
        )
        self.assertEqual(
            [
                "docker.io/library/alpine:3.20",
                "docker.io/library/busybox:1.36",
                "docker.io/library/envoy:v1.31",
            ],
            sorted(self.repo.violations()),
        )

    def test_split_image_and_tag_keys_compose_into_one_reference(self):
        self.repo.write(
            "tenants/probe/services/litellm/values.yaml",
            """
            db:
              dbReadyImage: docker.io/bitnamilegacy/postgresql
              dbReadyTag: "16.1.0-debian-11-r20"
            """,
        )
        self.assertEqual(
            ["docker.io/bitnamilegacy/postgresql:16.1.0-debian-11-r20"], self.repo.violations()
        )

    def test_a_split_tag_is_appended_after_a_ported_registry_host(self):
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            """
            db:
              dbReadyImage: registry.local:5000/team/app
              dbReadyTag: "v1.2.3"
            """,
        )
        self.assertEqual(["registry.local:5000/team/app:v1.2.3"], self.repo.violations())

    def test_image_pull_policy_and_secrets_are_not_image_references(self):
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            """
            imagePullPolicy: IfNotPresent
            imagePullSecrets: []
            imageRegistry: docker.io
            """,
        )
        self.assertTrue(self.repo.check().ok)

    def test_image_key_holding_a_non_reference_is_ignored(self):
        self.repo.write(
            "tenants/probe/services/svc/values.yaml",
            """
            useImage: true
            logoImage: /assets/logo.png
            templatedImage: "{{ .Values.foo }}"
            versionImage: 1.2.3
            """,
        )
        self.assertTrue(self.repo.check().ok)


class StructuredImageBlocks(TreeTest):
    def test_repository_without_a_tag_key_is_reported(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              repository: ghcr.io/berriai/litellm-database
              pullPolicy: IfNotPresent
            """,
        )
        self.assertEqual(["ghcr.io/berriai/litellm-database"], self.repo.violations())

    def test_empty_tag_is_reported(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              repository: ghcr.io/berriai/litellm-database
              tag: ""
            """,
        )
        self.assertEqual(["ghcr.io/berriai/litellm-database"], self.repo.violations())

    def test_registry_field_is_folded_into_the_reference(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              registry: docker.io
              repository: bitnami/os-shell
              tag: 12-debian-12-r16
            """,
        )
        self.assertEqual(["docker.io/bitnami/os-shell:12-debian-12-r16"], self.repo.violations())

    def test_digest_field_counts_as_a_pin(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            f"""
            image:
              registry: docker.io
              repository: library/nginx
              tag: 1.29.4
              digest: "{DIGEST_A}"
            """,
        )
        report = self.repo.check()
        self.assertTrue(report.ok, cip.render(report))
        self.assertEqual([PINNED_NGINX], [r.full_ref for r in report.pinned])

    def test_malformed_digest_field_is_rejected(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              repository: library/nginx
              tag: 1.29.4
              digest: "sha256:abc"
            """,
        )
        report = self.repo.check()
        self.assertEqual(1, len(report.malformed))
        self.assertEqual([], report.pinned)

    def test_registry_holding_the_whole_path_with_no_repository_is_reported(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            controller:
              driver:
                image:
                  registry: ghcr.io/democratic-csi/democratic-csi
                  tag: latest
            """,
        )
        self.assertEqual(["ghcr.io/democratic-csi/democratic-csi:latest"], self.repo.violations())

    def test_a_registry_holding_only_a_host_is_not_a_reference_on_its_own(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              registry: docker.io
              pullPolicy: IfNotPresent
            """,
        )
        self.assertTrue(self.repo.check().ok)

    def test_a_registry_and_repository_pair_is_still_reported_once(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              registry: docker.io/library
              repository: nginx
              tag: 1.29.4
            """,
        )
        self.assertEqual(["docker.io/library/nginx:1.29.4"], self.repo.violations())

    def test_a_registry_only_block_carrying_a_digest_is_pinned(self):
        self.repo.write(
            "helm-charts/probe/values.yaml",
            f"""
            image:
              registry: ghcr.io/democratic-csi/democratic-csi
              tag: latest@{DIGEST_A}
            """,
        )
        self.assertTrue(self.repo.check().ok)

    def test_digest_only_tag_in_a_structured_block_is_malformed(self):
        # Composes to `repo:@sha256:…`, which contains a well-formed digest
        # and previously read as pinned.
        self.repo.write(
            "helm-charts/probe/values.yaml",
            f"""
            image:
              repository: library/nginx
              tag: "@{DIGEST_A}"
            """,
        )
        report = self.repo.check()
        self.assertEqual([], report.pinned)
        self.assertEqual(1, len(report.malformed))

    def test_helm_chart_repository_url_is_not_an_image(self):
        self.repo.write(
            "helm-charts/probe/Chart.yaml",
            """
            apiVersion: v2
            name: probe
            dependencies:
              - name: redis
                repository: oci://registry-1.docker.io/bitnamicharts
                version: ">=18.0.0"
              - name: postgresql
                repository: https://charts.bitnami.com/bitnami
                version: ">=13.3.0"
            """,
        )
        self.assertTrue(self.repo.check().ok)


class TreeAllowlistKeying(TreeTest):
    REDIS_VALUES = """
    apiVersion: apps/v1
    kind: StatefulSet
    spec:
      template:
        spec:
          containers:
            - name: redis
              image: {ref}
    """

    def _write_redis(self, ref: str) -> None:
        self.repo.write(
            "tenants/probe/services/litellm-redis/postInstall/redis.yaml",
            self.REDIS_VALUES.format(ref=ref),
        )

    def _write_redis_allowlist(self, ref: str) -> None:
        self.repo.allowlist(
            f"""
            exceptions:
              - path: tenants/probe/services/litellm-redis/postInstall/redis.yaml
                ref: {ref}
                reason: pre-existing StatefulSet image, pinning is follow-up work
            """
        )

    def test_exempted_reference_is_allowed(self):
        self._write_redis("redis:7.4-alpine")
        self._write_redis_allowlist("redis:7.4-alpine")
        report = self.repo.check()
        self.assertTrue(report.ok, cip.render(report))
        self.assertEqual(["redis:7.4-alpine"], [r.full_ref for r, _ in report.allowed])

    def test_changing_the_tag_loses_the_exemption_and_reports_it_stale(self):
        self._write_redis("redis:latest")
        self._write_redis_allowlist("redis:7.4-alpine")
        report = self.repo.check()
        self.assertEqual(["redis:latest"], [r.full_ref for r in report.violations])
        self.assertEqual(
            [("tenants/probe/services/litellm-redis/postInstall/redis.yaml", "redis:7.4-alpine")],
            report.stale,
        )

    def test_same_path_different_images_are_independent_entries(self):
        self.repo.write(
            "tenants/probe/services/backup/postInstall/cronjob.yaml",
            """
            apiVersion: batch/v1
            kind: CronJob
            spec:
              jobTemplate:
                spec:
                  template:
                    spec:
                      containers:
                        - name: dump
                          image: postgres:17
                        - name: upload
                          image: rclone/rclone:latest
            """,
        )
        self.repo.allowlist(
            """
            exceptions:
              - path: tenants/probe/services/backup/postInstall/cronjob.yaml
                ref: postgres:17
                reason: pre-existing CronJob image, pinning is follow-up work
            """
        )
        report = self.repo.check()
        self.assertEqual(["rclone/rclone:latest"], [r.full_ref for r in report.violations])
        self.assertEqual(["postgres:17"], [r.full_ref for r, _ in report.allowed])


class TreePreservedBehaviour(TreeTest):
    def test_digest_pinned_reference_passes(self):
        self.repo.write(
            "tenants/probe/services/svc/postInstall/pod.yaml",
            f"""
            apiVersion: v1
            kind: Pod
            spec:
              containers:
                - name: c
                  image: {PINNED_NGINX}
            """,
        )
        self.assertTrue(self.repo.check().ok)

    def test_image_embedded_in_a_configmap_string_is_walked(self):
        self.repo.write(
            "tenants/probe/services/lpp/postInstall/lpp.yaml",
            """
            apiVersion: v1
            kind: ConfigMap
            metadata:
              name: local-path-config
            data:
              helperPod.yaml: |-
                apiVersion: v1
                kind: Pod
                spec:
                  containers:
                    - name: helper-pod
                      image: busybox
            """,
        )
        self.assertEqual(["busybox"], self.repo.violations())

    def test_a_reference_repeated_in_one_file_is_reported_once(self):
        self.repo.write(
            "tenants/probe/services/svc/postInstall/pods.yaml",
            """
            apiVersion: v1
            kind: Pod
            spec:
              initContainers:
                - name: a
                  image: busybox:1.36
              containers:
                - name: b
                  image: busybox:1.36
            """,
        )
        self.assertEqual(["busybox:1.36"], self.repo.violations())

    def test_multi_document_files_are_walked(self):
        self.repo.write(
            "tenants/probe/services/svc/postInstall/all.yaml",
            """
            apiVersion: v1
            kind: ConfigMap
            ---
            apiVersion: v1
            kind: Pod
            spec:
              containers:
                - image: busybox:1.36
            """,
        )
        self.assertEqual(["busybox:1.36"], self.repo.violations())


# ===========================================================================
# Configuration
# ===========================================================================


class Configuration(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)

    def _config(self, body: str) -> None:
        path = self.root / cip.CONFIG_RELPATH
        path.parent.mkdir(parents=True)
        path.write_text(textwrap.dedent(body), encoding="utf-8")

    def test_missing_config_is_a_hard_failure(self):
        with self.assertRaises(SystemExit) as caught:
            cip.check(self.root)
        self.assertIn(cip.CONFIG_RELPATH, str(caught.exception))

    def test_unknown_source_kind_is_rejected(self):
        self._config("sources:\n  - kind: mystery\n")
        with self.assertRaises(SystemExit) as caught:
            cip.load_config(self.root)
        self.assertIn("mystery", str(caught.exception))

    def test_tree_source_needs_globs(self):
        self._config("sources:\n  - kind: tree\n")
        with self.assertRaises(SystemExit):
            cip.load_config(self.root)

    def test_allowlist_location_is_configurable(self):
        self._config(
            """\
            allowlist: policy/exceptions.yaml
            sources:
              - kind: tree
                globs: ["manifests/**/*.yaml"]
            """
        )
        (self.root / "manifests").mkdir()
        (self.root / "manifests/pod.yaml").write_text("image: busybox:1.36\n", encoding="utf-8")
        (self.root / "policy").mkdir()
        (self.root / "policy/exceptions.yaml").write_text(
            "exceptions:\n  - path: manifests/pod.yaml\n    ref: busybox:1.36\n    reason: test\n",
            encoding="utf-8",
        )
        report = cip.check(self.root)
        self.assertTrue(report.ok, cip.render(report))
        self.assertIn("policy/exceptions.yaml", cip.render(report))

    def test_main_exit_code_follows_the_report(self):
        self._config('sources:\n  - kind: tree\n    globs: ["manifests/**/*.yaml"]\n')
        (self.root / "manifests").mkdir()
        (self.root / "manifests/pod.yaml").write_text("image: busybox:1.36\n", encoding="utf-8")
        import contextlib
        import io

        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(1, cip.main(["--repo-root", str(self.root)]))
        (self.root / "manifests/pod.yaml").write_text(f"image: {PINNED_NGINX}\n", encoding="utf-8")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(0, cip.main(["--repo-root", str(self.root)]))


class RepositoryAllowlistIntegrity(unittest.TestCase):
    """The real repo this file is vendored into must stay green and its
    allowlist must stay minimal. Skipped where there is no per-repo config
    (the canonical copy's own repo)."""

    def test_repository_scan_exits_clean(self):
        repo_root = MODULE_PATH.parent.parent
        if not (repo_root / cip.CONFIG_RELPATH).exists():
            self.skipTest(f"no {cip.CONFIG_RELPATH} in {repo_root}")
        report = cip.check(repo_root)
        self.assertEqual([], [r.rendered for r in report.violations])
        self.assertEqual([], [r.rendered for r in report.malformed])
        self.assertEqual([], report.unchecked_charts)
        self.assertEqual([], report.stale)


if __name__ == "__main__":
    unittest.main()
