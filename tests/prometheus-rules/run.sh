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
# Keyed by rule-set name rather than source filename: two services both call
# their manifest prometheusrule.yaml, and writing by basename silently dropped
# one of them from the checks entirely.
seen: dict[str, pathlib.Path] = {}
skipped: list[str] = []


def claim(name: str, path: pathlib.Path, groups: list) -> None:
    if name in seen:
        sys.exit(f"duplicate rule set {name!r} in {path} and {seen[name]}")
    seen[name] = path
    (out / f"{name}.yaml").write_text(yaml.safe_dump({"groups": groups}, sort_keys=False))


def rule_list(value) -> bool:
    return (
        isinstance(value, list)
        and bool(value)
        and all(isinstance(r, dict) and ("alert" in r or "record" in r) for r in value)
    )


def find_chart_rules(node, path: pathlib.Path, trail: str = "") -> None:
    """Collect `prometheusRule` blocks anywhere in a values tree.

    Recursive because a subchart's rules sit under its alias
    (`<alias>.prometheusRule.rules`), which a top-level lookup never sees.
    """
    if isinstance(node, list):
        for item in node:
            find_chart_rules(item, path, trail)
        return
    if not isinstance(node, dict):
        return

    block = node.get("prometheusRule")
    if isinstance(block, dict):
        name = "-".join(filter(None, [path.parent.name, "values", trail.strip(".").replace(".", "-")]))
        # Both shapes in the wild: a flat rule list, which promtool needs
        # wrapped in a group, and pre-formed groups.
        if rule_list(block.get("rules")):
            claim(name, path, [{"name": path.parent.name, "rules": block["rules"]}])
        elif isinstance(block.get("groups"), list) and block["groups"]:
            claim(name, path, block["groups"])
        elif block.get("rules") or block.get("groups"):
            # Present but not a shape this understands. Never skip that
            # silently — an unrecognised shape is how live rules go unchecked.
            sys.exit(f"{path}: prometheusRule at {trail or '<root>'} has rules/groups in an unrecognised shape")

    for key, value in node.items():
        if key != "prometheusRule":
            find_chart_rules(value, path, f"{trail}.{key}")


# helm-charts/ is scanned too — rules embedded in a chart's values are as live
# as any manifest. Its templates/ are not: they are Go templates that only
# become YAML after rendering, which helm-lint covers instead.
roots = [root / "tenants", root / "helm-charts"]
for scan in roots:
    if not scan.is_dir():
        continue
    for path in sorted(scan.rglob("*.yaml")):
        if "templates" in path.relative_to(root).parts:
            continue
        try:
            docs = list(yaml.safe_load_all(path.read_text()))
        except (yaml.YAMLError, UnicodeDecodeError) as err:
            # Helm values and templated manifests are not all parseable YAML. A
            # silent `continue` here would drop a future rule file out of the
            # gate with no output at all, which is the same silently-incomplete
            # gate this script was rewritten to close — so an unparseable file
            # that looks like it carries rules is fatal, and every other skip
            # is named.
            try:
                looks_like_rules = "kind: PrometheusRule" in path.read_text(errors="replace")
            except OSError:
                looks_like_rules = True
            if looks_like_rules:
                sys.exit(f"{path}: contains 'kind: PrometheusRule' but does not parse: {err}")
            skipped.append(f"{path.relative_to(root)} (unparseable, no PrometheusRule)")
            continue

        for doc in docs:
            if not isinstance(doc, dict):
                continue

            if doc.get("kind") == "PrometheusRule":
                name = doc.get("metadata", {}).get("name")
                if not name:
                    sys.exit(f"{path}: PrometheusRule has no metadata.name")
                groups = doc.get("spec", {}).get("groups")
                if not groups:
                    # A PrometheusRule declaring no groups is a typo'd key, not
                    # a rule set worth zero rules. Skipping it silently is how
                    # the whole file leaves the gate unnoticed.
                    sys.exit(f"{path}: PrometheusRule {name!r} has no spec.groups")
                claim(name, path, groups)
                continue

            find_chart_rules(doc, path)

if not seen:
    sys.exit("no alert rules found under tenants/ or helm-charts/")
for note in skipped:
    print(f"skipped: {note}")
print(f"extracted {len(seen)} rule set(s)")
PY

cp "$TESTS_DIR"/*_test.yaml "$WORK/"
cd "$WORK"
promtool check rules rules/*.yaml
promtool test rules ./*_test.yaml
