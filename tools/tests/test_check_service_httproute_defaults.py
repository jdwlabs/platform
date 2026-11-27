#!/usr/bin/env python3
"""Regression tests for tools/check-service-httproute-defaults.py.

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
    "check_service_httproute_defaults", TOOLS_DIR / "check-service-httproute-defaults.py"
)
checker = importlib.util.module_from_spec(_spec)
sys.modules["check_service_httproute_defaults"] = checker
_spec.loader.exec_module(checker)

EXPLICIT = """
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: app
  namespace: app
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: platform-gateway
      namespace: nginx-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
          headers:
            - type: Exact
              name: x-env
              value: prd
      backendRefs:
        - group: ""
          kind: Service
          name: app
          port: 80
          weight: 1
"""


class CheckerHarness(unittest.TestCase):
    """Runs the checker against a synthetic tenants/ tree instead of this repo."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)

    def write(self, relpath: str, text: str) -> None:
        path = self.root / relpath
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(text), encoding="utf-8")

    def violations(self) -> list[str]:
        return checker.find_violations(self.root)

    def route_without(self, needle: str) -> str:
        lines = EXPLICIT.splitlines()
        return "\n".join(line for line in lines if needle not in line) + "\n"

    def test_fully_explicit_route_passes(self):
        self.write("tenants/platform/services/app/postInstall/httproute.yaml", EXPLICIT)
        self.assertEqual(self.violations(), [])

    def test_missing_parent_ref_group_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without("group: gateway.networking.k8s.io"),
        )
        self.assertTrue(any("parentRefs[0].group" in v for v in self.violations()))

    def test_missing_parent_ref_kind_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without("kind: Gateway"),
        )
        self.assertTrue(any("parentRefs[0].kind" in v for v in self.violations()))

    def test_rule_without_matches_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            """
            apiVersion: gateway.networking.k8s.io/v1
            kind: HTTPRoute
            metadata: {name: app, namespace: app}
            spec:
              parentRefs:
                - {group: gateway.networking.k8s.io, kind: Gateway, name: gw}
              rules:
                - backendRefs:
                    - {group: "", kind: Service, name: app, port: 80, weight: 1}
            """,
        )
        self.assertTrue(any("rules[0].matches" in v for v in self.violations()))

    def test_match_without_path_fails(self):
        # The apiserver defaults a missing match path to PathPrefix "/", so a
        # method-only match is just as sparse as no match at all.
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            """
            apiVersion: gateway.networking.k8s.io/v1
            kind: HTTPRoute
            metadata: {name: app, namespace: app}
            spec:
              parentRefs:
                - {group: gateway.networking.k8s.io, kind: Gateway, name: gw}
              rules:
                - matches:
                    - method: POST
                  backendRefs:
                    - {group: "", kind: Service, name: app, port: 80, weight: 1}
            """,
        )
        self.assertTrue(any("matches[0].path" in v for v in self.violations()))

    def test_path_without_type_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without("type: PathPrefix"),
        )
        self.assertTrue(any("path.type" in v for v in self.violations()))

    def test_header_match_without_type_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without("type: Exact"),
        )
        self.assertTrue(any("headers[0].type" in v for v in self.violations()))

    def test_backend_ref_without_weight_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without("weight: 1"),
        )
        self.assertTrue(any("backendRefs[0].weight" in v for v in self.violations()))

    def test_backend_ref_without_core_group_fails(self):
        # An empty-string group is the defaulted value and must be written out;
        # an absent key is what the apiserver fills in.
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without('group: ""'),
        )
        self.assertTrue(any("backendRefs[0].group" in v for v in self.violations()))

    def test_backend_ref_without_kind_fails(self):
        self.write(
            "tenants/platform/services/app/postInstall/httproute.yaml",
            self.route_without("kind: Service"),
        )
        self.assertTrue(any("backendRefs[0].kind" in v for v in self.violations()))

    def test_route_in_multi_document_file_is_checked(self):
        self.write(
            "tenants/acme/services/app/postInstall/all.yaml",
            "apiVersion: v1\nkind: Service\nmetadata: {name: app}\n---\n"
            + self.route_without("weight: 1"),
        )
        self.assertTrue(any("backendRefs[0].weight" in v for v in self.violations()))

    def test_other_kinds_are_ignored(self):
        self.write(
            "tenants/platform/services/app/postInstall/gateway.yaml",
            """
            apiVersion: gateway.networking.k8s.io/v1
            kind: Gateway
            metadata: {name: gw}
            spec:
              listeners: []
            """,
        )
        self.assertEqual(self.violations(), [])

    def test_route_outside_services_is_ignored(self):
        # Deployment-repo routes are diffed server-side, so they may stay sparse.
        self.write("helm-charts/example/templates/httproute.yaml", self.route_without("weight: 1"))
        self.assertEqual(self.violations(), [])

    def test_this_repo_passes(self):
        self.assertEqual(checker.find_violations(checker.REPO_ROOT), [])


if __name__ == "__main__":
    unittest.main()
