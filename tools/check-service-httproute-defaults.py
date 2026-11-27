#!/usr/bin/env python3
"""Require every service HTTPRoute to spell out the fields the apiserver defaults.

Service apps are diffed client-side, which does not apply the Gateway API
CRD defaults. A route that leaves out a parentRef group/kind, a backendRef
group/kind/weight, or its rule matches is stored with those filled in, and
the gap reads as permanent drift. The fix used to be ignoring those paths in
services-appset.yaml, but under RespectIgnoreDifferences an ignored path is
also replaced with the live value before the apply: an existing route whose
matches or weights changed in git diffed clean, synced green, and kept
routing the old way. With the ignores gone, the render has to equal the
stored object, so the defaults have to be written out.

Scope is HTTPRoute documents under tenants/*/services/. Deployment-repo
routes are diffed server-side, which resolves the defaults itself, and
routes a remote chart renders are not visible without a render; one of
those left sparse shows up as OutOfSync, not as a silent no-op.

Usage:
    python3 tools/check-service-httproute-defaults.py

Exit codes: 0 = every service HTTPRoute is explicit; 1 = at least one
defaulted field is missing.
"""
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent

YAML_SUFFIXES = (".yaml", ".yml")


def _missing(obj: dict, keys: tuple[str, ...], where: str) -> list[str]:
    return [f"{where}.{key}" for key in keys if key not in obj]


def route_violations(route: dict) -> list[str]:
    spec = route.get("spec") or {}
    found = []
    for i, ref in enumerate(spec.get("parentRefs") or []):
        found += _missing(ref, ("group", "kind"), f"parentRefs[{i}]")
    for r, rule in enumerate(spec.get("rules") or []):
        where = f"rules[{r}]"
        matches = rule.get("matches")
        if not matches:
            found.append(f"{where}.matches")
        for m, match in enumerate(matches or []):
            mwhere = f"{where}.matches[{m}]"
            path = match.get("path")
            if path is None:
                found.append(f"{mwhere}.path")
            else:
                found += _missing(path, ("type", "value"), f"{mwhere}.path")
            for field in ("headers", "queryParams"):
                for h, item in enumerate(match.get(field) or []):
                    found += _missing(item, ("type",), f"{mwhere}.{field}[{h}]")
        for b, backend in enumerate(rule.get("backendRefs") or []):
            found += _missing(backend, ("group", "kind", "weight"), f"{where}.backendRefs[{b}]")
    return found


def find_violations(root: Path) -> list[str]:
    violations = []
    for path in sorted((root / "tenants").glob("*/services/**/*")):
        if path.suffix not in YAML_SUFFIXES or not path.is_file():
            continue
        with path.open(encoding="utf-8") as fh:
            documents = list(yaml.safe_load_all(fh))
        for doc in documents:
            if not isinstance(doc, dict) or doc.get("kind") != "HTTPRoute":
                continue
            name = (doc.get("metadata") or {}).get("name", "?")
            rel = path.relative_to(root)
            violations += [f"{rel} ({name}): missing spec.{field}" for field in route_violations(doc)]
    return violations


def main() -> int:
    violations = find_violations(REPO_ROOT)
    if not violations:
        print("service-httproute-defaults: every service HTTPRoute is explicit")
        return 0
    print("SPARSE SERVICE HTTPROUTE — write out the value the apiserver would default:")
    for violation in violations:
        print(f"  {violation}")
    print(
        "\nDefaults: parentRefs group gateway.networking.k8s.io, kind Gateway;"
        ' backendRefs group "", kind Service, weight 1; a rule with no match'
        " gets matches: [{path: {type: PathPrefix, value: /}}]; a header or"
        " query param match gets type Exact."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
