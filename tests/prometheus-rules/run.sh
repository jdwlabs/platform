#!/usr/bin/env bash
# Runs promtool's rule checks and unit tests against every PrometheusRule in
# the tree. Kept out of the tenants/ directories on purpose: anything under a
# service's postInstall/ is applied to the cluster by ArgoCD, and a promtool
# test file is not a Kubernetes object.
set -euo pipefail

if ! command -v promtool >/dev/null 2>&1; then
  echo "promtool not found. Install it from a prometheus release tarball:" >&2
  echo "  https://github.com/prometheus/prometheus/releases" >&2
  exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
TESTS_DIR="$REPO_ROOT/tests/prometheus-rules"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/rules"

python3 - "$REPO_ROOT" "$WORK/rules" <<'PY'
import pathlib
import sys

import yaml

root, out = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
# Keyed by metadata.name rather than filename: two services both call their
# manifest prometheusrule.yaml, and writing by basename silently dropped one of
# them from the checks entirely.
seen: dict[str, pathlib.Path] = {}
for path in sorted((root / "tenants").rglob("*.yaml")):
    try:
        docs = list(yaml.safe_load_all(path.read_text()))
    except (yaml.YAMLError, UnicodeDecodeError):
        # Helm values and templated manifests are not all parseable YAML, and
        # none of them are PrometheusRules.
        continue
    for doc in docs:
        if not isinstance(doc, dict) or doc.get("kind") != "PrometheusRule":
            continue
        groups = doc.get("spec", {}).get("groups")
        if not groups:
            continue
        name = doc.get("metadata", {}).get("name")
        if not name:
            sys.exit(f"{path}: PrometheusRule has no metadata.name")
        if name in seen:
            sys.exit(f"duplicate PrometheusRule name {name!r} in {path} and {seen[name]}")
        seen[name] = path
        (out / f"{name}.yaml").write_text(yaml.safe_dump({"groups": groups}, sort_keys=False))
if not seen:
    sys.exit("no PrometheusRule manifests found under tenants/")
print(f"extracted {len(seen)} PrometheusRule manifest(s)")
PY

cp "$TESTS_DIR"/*_test.yaml "$WORK/"
cd "$WORK"
promtool check rules rules/*.yaml
promtool test rules ./*_test.yaml
