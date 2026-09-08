#!/usr/bin/env python3
"""Regression tests for tools/sync-gateway-api-crds.py.

Every test here is a way the vendored Gateway API bundle can rot while the
cluster stays green: content edited by hand, a pin bumped without a
regeneration, a CRD the new release ships that nobody decided about, or a
regeneration that walks over the prometheus-operator CRDs sharing the file.
Run with:

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
    "sync_gateway_api_crds", TOOLS_DIR / "sync-gateway-api-crds.py"
)
checker = importlib.util.module_from_spec(_spec)
sys.modules["sync_gateway_api_crds"] = checker
_spec.loader.exec_module(checker)

GATEWAY_CRDS = ["gateways.gateway.networking.k8s.io", "httproutes.gateway.networking.k8s.io"]
MONITORING_CRD = "servicemonitors.monitoring.coreos.com"


def gateway_doc(name: str, version: str = "v1.1.0", channel: str = "experimental",
                scope: str = "Namespaced") -> str:
    return textwrap.dedent(
        f"""\
        apiVersion: apiextensions.k8s.io/v1
        kind: CustomResourceDefinition
        metadata:
          annotations:
            gateway.networking.k8s.io/bundle-version: {version}
            gateway.networking.k8s.io/channel: {channel}
          name: {name}
        spec:
          group: gateway.networking.k8s.io
          scope: {scope}
        """
    )


def monitoring_doc() -> str:
    return textwrap.dedent(
        f"""\
        apiVersion: apiextensions.k8s.io/v1
        kind: CustomResourceDefinition
        metadata:
          annotations:
            operator.prometheus.io/version: 0.93.1
          name: {MONITORING_CRD}
        spec:
          group: monitoring.coreos.com
          scope: Namespaced
        """
    )


def admission_policy() -> str:
    return textwrap.dedent(
        """\
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: safe-upgrades.gateway.networking.k8s.io
        """
    )


def release(docs: list[str]) -> str:
    # Mirrors the upstream artifact: a licence header, then one document per
    # CRD each prefixed with the config/crd path it was generated from.
    header = "# Copyright\n#\n# Gateway API Experimental channel install\n#\n"
    body = "".join(f"---\n#\n# config/crd/{index}.yaml\n#\n{doc}" for index, doc in enumerate(docs))
    return header + body


def bundle(docs: list[str]) -> str:
    return docs[0] + "".join(f"---\n{doc}" for doc in docs[1:])


class CheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic bundle and a stubbed release."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self._saved = (checker.PIN_FILE, checker.BUNDLE_FILE, checker.fetch_release, sys.argv)
        checker.PIN_FILE = self.root / "gateway-api-crd-pin.yaml"
        checker.BUNDLE_FILE = self.root / "foundation-crds.yaml"
        checker.fetch_release = lambda version, channel: self.release_text
        # main() parses sys.argv, which under `unittest discover` still holds
        # the discovery flags.
        sys.argv = ["sync-gateway-api-crds.py"]
        self.release_text = release([gateway_doc(name) for name in GATEWAY_CRDS])
        self.write_pin(GATEWAY_CRDS)
        self.write_bundle([gateway_doc(name) for name in GATEWAY_CRDS] + [monitoring_doc()])
        self.addCleanup(self._restore)

    def _restore(self):
        checker.PIN_FILE, checker.BUNDLE_FILE, checker.fetch_release, sys.argv = self._saved
        self._tmp.cleanup()

    def write_pin(self, vendored: list[str], not_vendored: dict[str, str] | None = None,
                  version: str = "v1.1.0", channel: str = "experimental") -> None:
        lines = [f"version: {version}", f"channel: {channel}", "vendored:"]
        lines += [f"  - {name}" for name in vendored]
        if not_vendored:
            lines.append("notVendored:")
            lines += [f"  {name}: {reason}" for name, reason in not_vendored.items()]
        checker.PIN_FILE.write_text("\n".join(lines) + "\n", encoding="utf-8")

    def write_bundle(self, docs: list[str]) -> None:
        checker.BUNDLE_FILE.write_text(bundle(docs), encoding="utf-8")

    def bundle_text(self) -> str:
        return checker.BUNDLE_FILE.read_text(encoding="utf-8")

    def run_checker(self, *flags: str) -> int:
        sys.argv = ["sync-gateway-api-crds.py", *flags]
        return checker.main()

    def test_matching_bundle_passes(self):
        self.assertEqual(self.run_checker(), 0)

    def test_edited_field_fails(self):
        self.write_bundle(
            [gateway_doc(GATEWAY_CRDS[0], scope="Cluster"), gateway_doc(GATEWAY_CRDS[1]),
             monitoring_doc()]
        )
        self.assertEqual(self.run_checker(), 1)

    def test_pin_bumped_without_regeneration_fails(self):
        self.write_pin(GATEWAY_CRDS, version="v1.6.1", channel="standard")
        self.release_text = release(
            [gateway_doc(name, version="v1.6.1", channel="standard") for name in GATEWAY_CRDS]
        )
        self.assertEqual(self.run_checker(), 1)

    def test_write_regenerates_an_edited_document(self):
        self.write_bundle(
            [gateway_doc(GATEWAY_CRDS[0], scope="Cluster"), gateway_doc(GATEWAY_CRDS[1]),
             monitoring_doc()]
        )
        self.assertEqual(self.run_checker("--write"), 0)
        self.assertEqual(self.run_checker(), 0)

    def test_write_leaves_the_monitoring_tail_untouched(self):
        self.write_bundle(
            [gateway_doc(GATEWAY_CRDS[0], scope="Cluster"), gateway_doc(GATEWAY_CRDS[1]),
             monitoring_doc()]
        )
        self.assertEqual(self.run_checker("--write"), 0)
        self.assertIn(monitoring_doc().rstrip("\n"), self.bundle_text())
        self.assertEqual(self.bundle_text().count(MONITORING_CRD), 1)

    def test_write_is_a_no_op_when_in_sync(self):
        before = self.bundle_text()
        self.assertEqual(self.run_checker("--write"), 0)
        self.assertEqual(self.bundle_text(), before)

    def test_pinned_crd_absent_from_the_bundle_fails(self):
        self.write_bundle([gateway_doc(GATEWAY_CRDS[0]), monitoring_doc()])
        self.assertEqual(self.run_checker(), 1)

    def test_write_appends_a_newly_pinned_crd_before_the_monitoring_tail(self):
        self.write_bundle([gateway_doc(GATEWAY_CRDS[0]), monitoring_doc()])
        self.assertEqual(self.run_checker("--write"), 0)
        text = self.bundle_text()
        self.assertLess(text.index(GATEWAY_CRDS[1]), text.index(MONITORING_CRD))
        self.assertEqual(self.run_checker(), 0)

    def test_unpinned_gateway_crd_in_the_bundle_fails(self):
        # A CRD applied to the cluster that the pin does not name is watched by
        # nothing, which is the exact hole this tool exists to close.
        self.write_pin(GATEWAY_CRDS[:1], not_vendored={GATEWAY_CRDS[1]: "unused"})
        with self.assertRaises(SystemExit):
            self.run_checker()

    def test_release_crd_in_neither_list_fails(self):
        self.release_text = release(
            [gateway_doc(name) for name in GATEWAY_CRDS]
            + [gateway_doc("listenersets.gateway.networking.k8s.io")]
        )
        with self.assertRaises(SystemExit):
            self.run_checker()

    def test_release_crd_recorded_as_not_vendored_passes(self):
        self.release_text = release(
            [gateway_doc(name) for name in GATEWAY_CRDS]
            + [gateway_doc("listenersets.gateway.networking.k8s.io")]
        )
        self.write_pin(
            GATEWAY_CRDS,
            not_vendored={"listenersets.gateway.networking.k8s.io": "no consumer"},
        )
        self.assertEqual(self.run_checker(), 0)

    def test_pinned_crd_absent_from_the_release_fails(self):
        self.write_pin(GATEWAY_CRDS + ["xmeshes.gateway.networking.x-k8s.io"])
        with self.assertRaises(SystemExit):
            self.run_checker()

    def test_crd_listed_as_both_vendored_and_not_vendored_fails(self):
        self.write_pin(GATEWAY_CRDS, not_vendored={GATEWAY_CRDS[0]: "contradiction"})
        with self.assertRaises(SystemExit):
            self.run_checker()

    def test_admission_policy_in_the_release_is_ignored(self):
        # The release artifact ships a ValidatingAdmissionPolicy alongside the
        # CRDs; it is staged separately, so it must not read as an unclassified
        # kind that fails the check.
        self.release_text = release(
            [admission_policy()] + [gateway_doc(name) for name in GATEWAY_CRDS]
        )
        self.assertEqual(self.run_checker(), 0)

    def test_missing_pin_key_fails(self):
        checker.PIN_FILE.write_text("channel: experimental\n", encoding="utf-8")
        with self.assertRaises(SystemExit):
            self.run_checker()

    def test_bundle_without_any_pinned_crd_fails(self):
        self.write_bundle([monitoring_doc()])
        with self.assertRaises(SystemExit):
            self.run_checker()


if __name__ == "__main__":
    unittest.main()
