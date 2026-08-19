#!/usr/bin/env bash
# Runs promtool's rule checks and unit tests against every alert rule in the
# tree. Kept out of the tenants/ directories on purpose: anything under a
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
# Keyed by rule-file name rather than source filename: two services both call
# their manifest prometheusrule.yaml, and writing by basename silently dropped
# one of them from the checks entirely.
seen: dict[str, pathlib.Path] = {}
skipped: list[str] = []


def claim(name: str, path: pathlib.Path, groups: list) -> None:
    if name in seen:
        sys.exit(f"duplicate rule set {name!r} in {path} and {seen[name]}")
    seen[name] = path
    (out / f"{name}.yaml").write_text(yaml.safe_dump({"groups": groups}, sort_keys=False))


for path in sorted((root / "tenants").rglob("*.yaml")):
    try:
        raw = path.read_text()
        docs = list(yaml.safe_load_all(raw))
    except (yaml.YAMLError, UnicodeDecodeError) as err:
        # Helm values and templated manifests are not all parseable YAML. A
        # silent `continue` here would drop a future rule file out of the gate
        # with no output at all, which is the same silently-incomplete gate
        # this script was rewritten to close — so an unparseable file that
        # looks like it carries rules is fatal, and every other skip is named.
        try:
            looks_like_rules = "kind: PrometheusRule" in path.read_text(errors="replace")
        except OSError:
            looks_like_rules = True
        if looks_like_rules:
            sys.exit(f"{path}: contains 'kind: PrometheusRule' but does not parse: {err}")
        skipped.append(str(path.relative_to(root)))
        continue

    for doc in docs:
        if not isinstance(doc, dict):
            continue

        if doc.get("kind") == "PrometheusRule":
            groups = doc.get("spec", {}).get("groups")
            if not groups:
                continue
            name = doc.get("metadata", {}).get("name")
            if not name:
                sys.exit(f"{path}: PrometheusRule has no metadata.name")
            claim(name, path, groups)
            continue

        # Rules embedded in Helm values are just as live as a PrometheusRule
        # manifest — the subchart renders them into one — and go unchecked
        # unless they are extracted here too. The shape is a flat rule list
        # under `prometheusRule.rules`, which promtool needs wrapped in a
        # group. Matched structurally rather than by key name alone so an
        # unrelated `prometheusRule` block cannot be mistaken for this one.
        block = doc.get("prometheusRule")
        if not isinstance(block, dict):
            continue
        rules = block.get("rules")
        if not isinstance(rules, list) or not rules:
            continue
        if not all(isinstance(r, dict) and ("alert" in r or "record" in r) for r in rules):
            continue
        # Checked whether or not the chart has them enabled: a disabled rule
        # set that no longer compiles is a trap laid for whoever enables it.
        claim(f"{path.parent.name}-values", path, [{"name": path.parent.name, "rules": rules}])

if not seen:
    sys.exit("no alert rules found under tenants/")
for path in skipped:
    print(f"skipped (unparseable, no PrometheusRule): {path}")
print(f"extracted {len(seen)} rule set(s)")
PY

cp "$TESTS_DIR"/*_test.yaml "$WORK/"
cd "$WORK"
promtool check rules rules/*.yaml
promtool test rules ./*_test.yaml
