#!/usr/bin/env python3
"""Regression tests for tools/check-image-pins.py.

Every test here corresponds to a reference the checker used to miss (or a
crash it used to raise). Run with:

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
    "check_image_pins", TOOLS_DIR / "check-image-pins.py"
)
checker = importlib.util.module_from_spec(_spec)
sys.modules["check_image_pins"] = checker
_spec.loader.exec_module(checker)

PINNED_NGINX = (
    "docker.io/library/nginx:1.29.4"
    "@sha256:0000000000000000000000000000000000000000000000000000000000000000"
)


class CheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic repo tree instead of this repo."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self._saved = (checker.REPO_ROOT, checker.ALLOWLIST_FILE)
        checker.REPO_ROOT = self.root
        checker.ALLOWLIST_FILE = self.root / "tools/image-pin-allowlist.yaml"
        self.addCleanup(self._restore)

    def _restore(self):
        checker.REPO_ROOT, checker.ALLOWLIST_FILE = self._saved
        self._tmp.cleanup()

    def write(self, relpath: str, content: str) -> Path:
        path = self.root / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(content).lstrip("\n"), encoding="utf-8")
        return path

    def write_allowlist(self, body: str) -> None:
        self.write("tools/image-pin-allowlist.yaml", body)

    def run_checker(self):
        """Returns (exit_code, violation_refs, allowed_refs)."""
        allowlist = checker.load_allowlist()
        _, allowed, violations, consumed = checker.collect(
            checker.discover_files(), allowlist
        )
        stale = set(allowlist) - consumed
        exit_code = 1 if (violations or stale) else 0
        return (
            exit_code,
            [ref for _, ref in violations],
            [ref for _, ref, _ in allowed],
        )

    def refs_in(self, relpath: str):
        return [r["full_ref"] for r in checker.extract_refs(self.root / relpath)]


class FloatTagCrash(CheckerHarness):
    """Defect 1: an unquoted `tag: 1.0` parses as a float and used to raise
    TypeError ("'in <string>' requires string as left operand, not float"),
    turning a policy violation into a red CI traceback."""

    def test_unquoted_float_tag_is_reported_not_raised(self):
        self.write(
            "tenants/probe/services/svc/values.yaml",
            """
            image:
              repository: docker.io/library/nginx
              tag: 1.0
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["docker.io/library/nginx:1.0"])

    def test_float_tag_keeps_its_literal_text(self):
        # `1.10` would round-trip through Python float as `1.1`, silently
        # reporting (and allowlisting) a tag that does not exist.
        self.write(
            "tenants/probe/services/svc/values.yaml",
            """
            image:
              repository: docker.io/library/nginx
              tag: 1.10
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["docker.io/library/nginx:1.10"])

    def test_unquoted_integer_tag_is_reported_not_raised(self):
        self.write(
            "tenants/probe/services/svc/postInstall/job.yaml",
            """
            image:
              repository: docker.io/library/postgres
              tag: 17
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["docker.io/library/postgres:17"])

    def test_as_str_never_raises_on_non_string_scalars(self):
        self.assertEqual(checker.as_str(1.0), "1.0")
        self.assertEqual(checker.as_str(17), "17")
        self.assertEqual(checker.as_str(None), "")
        self.assertEqual(checker.as_str(True), "true")


class GlobCoverage(CheckerHarness):
    """Defect 2: TARGET_GLOBS covered only tenant overlays and only *.yaml,
    so the whole helm-charts/ tree — which four platform services deploy from
    via chartPath — and every *.yml file were invisible to the gate."""

    def test_helm_chart_values_are_scanned(self):
        self.write(
            "helm-charts/db-ui/values.yaml",
            """
            image:
              repository: adminer
              tag: "5.5.0"
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["adminer:5.5.0"])

    def test_vendored_subchart_values_are_scanned(self):
        self.write(
            "helm-charts/litellm-helm/charts/redis/values.yaml",
            """
            image:
              registry: docker.io
              repository: bitnami/redis
              tag: 7.2.4-debian-12-r9
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["docker.io/bitnami/redis:7.2.4-debian-12-r9"])

    def test_yml_extension_is_scanned(self):
        self.write(
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
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["flyway/flyway:11.2"])

    def test_tenant_files_outside_the_services_layout_are_scanned(self):
        self.write(
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
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["busybox:1.36"])

    def test_hardcoded_image_in_a_go_templated_chart_file_is_caught(self):
        # templates/ cannot be YAML-parsed, but a literal (non-templated)
        # reference there still reaches the cluster.
        self.write(
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
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["busybox"])

    def test_templated_image_expression_is_not_reported(self):
        # `{{ .Values.image.repository }}` is covered by the values.yaml it
        # reads from; reporting it here would be an unfixable violation.
        self.write(
            "helm-charts/probe/templates/deployment.yaml",
            """
            spec:
              containers:
                - name: c
                  image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])


class ImageKeyNames(CheckerHarness):
    """Defect 3: only the literal key `image` was read, so initImage /
    sidecarImage / dbReadyImage and friends were invisible — including a live
    dbReadyImage inside a file the old globs already covered."""

    def test_db_ready_image_is_found(self):
        self.write(
            "tenants/probe/services/litellm/values.yaml",
            """
            db:
              dbReadyImage: docker.io/bitnamilegacy/postgresql
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["docker.io/bitnamilegacy/postgresql"])

    def test_init_and_sidecar_image_keys_are_found(self):
        self.write(
            "tenants/probe/services/svc/values.yaml",
            """
            initImage: docker.io/library/busybox:1.36
            sidecarImage: docker.io/library/envoy:v1.31
            init_image: docker.io/library/alpine:3.20
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(
            sorted(violations),
            [
                "docker.io/library/alpine:3.20",
                "docker.io/library/busybox:1.36",
                "docker.io/library/envoy:v1.31",
            ],
        )

    def test_split_image_and_tag_keys_compose_into_one_reference(self):
        # The chart template joins these as `<image>:<tag>`; neither key on
        # its own is the reference that gets deployed.
        self.write(
            "tenants/probe/services/litellm/values.yaml",
            """
            db:
              dbReadyImage: docker.io/bitnamilegacy/postgresql
              dbReadyTag: "16.1.0-debian-11-r20"
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(
            violations, ["docker.io/bitnamilegacy/postgresql:16.1.0-debian-11-r20"]
        )

    def test_image_pull_policy_and_secrets_are_not_image_references(self):
        self.write(
            "tenants/probe/services/svc/values.yaml",
            """
            imagePullPolicy: IfNotPresent
            imagePullSecrets: []
            imageRegistry: docker.io
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])

    def test_image_key_holding_a_non_reference_is_ignored(self):
        self.write(
            "tenants/probe/services/svc/values.yaml",
            """
            useImage: true
            logoImage: /assets/logo.png
            templatedImage: "{{ .Values.foo }}"
            versionImage: 1.2.3
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])


class StructuredImageBlocks(CheckerHarness):
    """Defect 4: a {repository: ...} block with no `tag` key was skipped
    entirely, even though a missing tag is the *most* mutable case — the
    chart falls back to appVersion or to an implicit `latest`."""

    def test_repository_without_a_tag_key_is_reported(self):
        self.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              repository: ghcr.io/berriai/litellm-database
              pullPolicy: IfNotPresent
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["ghcr.io/berriai/litellm-database"])

    def test_empty_tag_is_reported(self):
        self.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              repository: ghcr.io/berriai/litellm-database
              tag: ""
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["ghcr.io/berriai/litellm-database"])

    def test_registry_field_is_folded_into_the_reference(self):
        self.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              registry: docker.io
              repository: bitnami/os-shell
              tag: 12-debian-12-r16
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["docker.io/bitnami/os-shell:12-debian-12-r16"])

    def test_digest_field_counts_as_a_pin(self):
        self.write(
            "helm-charts/probe/values.yaml",
            f"""
            image:
              registry: docker.io
              repository: library/nginx
              tag: 1.29.4
              digest: "{PINNED_NGINX.split('@')[1]}"
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])

    def test_registry_holding_the_whole_path_with_no_repository_is_reported(self):
        """Defect 5 (JDWLABS-369): the democratic-csi chart has no
        `repository` key at all and renders "{{ .registry }}:{{ .tag }}", so
        keying only on `repository` made the block invisible."""
        self.write(
            "helm-charts/probe/values.yaml",
            """
            controller:
              driver:
                image:
                  registry: ghcr.io/democratic-csi/democratic-csi
                  tag: latest
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["ghcr.io/democratic-csi/democratic-csi:latest"])

    def test_a_registry_holding_only_a_host_is_not_a_reference_on_its_own(self):
        self.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              registry: docker.io
              pullPolicy: IfNotPresent
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])

    def test_a_registry_and_repository_pair_is_still_reported_once(self):
        """The registry-only fallback must not double-count the common shape."""
        self.write(
            "helm-charts/probe/values.yaml",
            """
            image:
              registry: docker.io/library
              repository: nginx
              tag: 1.29.4
            """,
        )
        self.assertEqual(
            self.refs_in("helm-charts/probe/values.yaml"), ["docker.io/library/nginx:1.29.4"]
        )

    def test_a_registry_only_block_carrying_a_digest_is_pinned(self):
        self.write(
            "helm-charts/probe/values.yaml",
            f"""
            image:
              registry: ghcr.io/democratic-csi/democratic-csi
              tag: latest@{PINNED_NGINX.split('@')[1]}
            """,
        )
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])

    def test_helm_chart_repository_url_is_not_an_image(self):
        # Chart.yaml dependencies use the same key name for an HTTP/OCI repo.
        self.write(
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
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])


class AllowlistKeying(CheckerHarness):
    """Defect 5: the allowlist key was (path, repository), so an allowlisted
    reference kept its exemption after its TAG changed — redis:7.4-alpine to
    redis:latest passed silently."""

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
        self.write(
            "tenants/probe/services/litellm-redis/postInstall/redis.yaml",
            self.REDIS_VALUES.format(ref=ref),
        )

    def _write_redis_allowlist(self, ref: str) -> None:
        self.write_allowlist(
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
        exit_code, violations, allowed = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])
        self.assertEqual(allowed, ["redis:7.4-alpine"])

    def test_changing_the_tag_loses_the_exemption(self):
        self._write_redis("redis:latest")
        self._write_redis_allowlist("redis:7.4-alpine")
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["redis:latest"])

    def test_the_superseded_entry_is_also_reported_stale(self):
        self._write_redis("redis:latest")
        self._write_redis_allowlist("redis:7.4-alpine")
        allowlist = checker.load_allowlist()
        _, _, _, consumed = checker.collect(checker.discover_files(), allowlist)
        self.assertEqual(set(allowlist) - consumed, {
            ("tenants/probe/services/litellm-redis/postInstall/redis.yaml",
             "redis:7.4-alpine")
        })

    def test_same_path_different_images_are_independent_entries(self):
        self.write(
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
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/probe/services/backup/postInstall/cronjob.yaml
                ref: postgres:17
                reason: pre-existing CronJob image, pinning is follow-up work
            """
        )
        exit_code, violations, allowed = self.run_checker()
        self.assertEqual(exit_code, 1)
        self.assertEqual(violations, ["rclone/rclone:latest"])
        self.assertEqual(allowed, ["postgres:17"])

    def test_entry_without_a_ref_is_rejected(self):
        self._write_redis("redis:7.4-alpine")
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/probe/services/litellm-redis/postInstall/redis.yaml
                repository: redis
                reason: keyed on the bare repository, which is no longer accepted
            """
        )
        with self.assertRaises(SystemExit) as caught:
            checker.load_allowlist()
        self.assertIn("'ref'", str(caught.exception))

    def test_entry_without_a_reason_is_rejected(self):
        self._write_redis("redis:7.4-alpine")
        self.write_allowlist(
            """
            exceptions:
              - path: tenants/probe/services/litellm-redis/postInstall/redis.yaml
                ref: redis:7.4-alpine
                reason: "   "
            """
        )
        with self.assertRaises(SystemExit) as caught:
            checker.load_allowlist()
        self.assertIn("reason", str(caught.exception))


class PreservedBehaviour(CheckerHarness):
    """Shapes the original checker already handled — kept so the widened
    walker does not regress them."""

    def test_digest_pinned_reference_passes(self):
        self.write(
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
        exit_code, violations, _ = self.run_checker()
        self.assertEqual(exit_code, 0)
        self.assertEqual(violations, [])

    def test_image_embedded_in_a_configmap_string_is_walked(self):
        self.write(
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
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["busybox"])

    def test_registry_port_is_not_mistaken_for_a_tag(self):
        self.assertTrue(checker.has_tag_or_digest("registry.local:5000/team/app:v1"))
        self.assertFalse(checker.has_tag_or_digest("registry.local:5000/team/app"))

    def test_a_split_tag_is_appended_after_a_ported_registry_host(self):
        self.write(
            "tenants/probe/services/svc/values.yaml",
            """
            db:
              dbReadyImage: registry.local:5000/team/app
              dbReadyTag: "v1.2.3"
            """,
        )
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["registry.local:5000/team/app:v1.2.3"])

    def test_a_reference_repeated_in_one_file_is_reported_once(self):
        self.write(
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
        _, violations, _ = self.run_checker()
        self.assertEqual(violations, ["busybox:1.36"])


class RepositoryAllowlistIntegrity(unittest.TestCase):
    """The real repo must stay green and its allowlist must stay minimal."""

    def test_repository_scan_exits_clean(self):
        allowlist = checker.load_allowlist()
        _, _, violations, consumed = checker.collect(
            checker.discover_files(), allowlist
        )
        self.assertEqual([f"{ref}" for _, ref in violations], [])
        self.assertEqual(sorted(set(allowlist) - consumed), [])


if __name__ == "__main__":
    unittest.main()
