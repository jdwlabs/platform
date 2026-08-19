#!/usr/bin/env bash
# Resolves the Alertmanager route tree with Alertmanager's own engine and
# asserts every case in routing-matrix.yaml.
#
# Rule syntax is a different question from routing. promtool proves an alert
# compiles; nothing it checks can see a route that sends that alert to the
# wrong receiver, a `continue: false` that truncates sibling matching, or a
# 'null' route ordered below the routes it is meant to pre-empt. Those
# failures are silent in the cluster — the alert simply never arrives — and
# the same wrong routing claim reached the docs twice because the only
# evidence for it was a matrix run by hand on one machine.
#
# The config is not a file in this repo: it is a string inside an
# ExternalSecret template, so it is rendered the way ESO would render it
# before amtool sees it. That rendering lives here rather than in a reviewer's
# shell history, so CI and a local run resolve the same bytes.
set -euo pipefail

if ! command -v amtool >/dev/null 2>&1; then
  echo "amtool not found. Install it from an alertmanager release tarball" >&2
  echo "(amtool ships with alertmanager, not with prometheus):" >&2
  echo "  https://github.com/prometheus/alertmanager/releases" >&2
  exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

python3 - "$REPO_ROOT" "$WORK" <<'PY'
import pathlib
import subprocess
import sys

import yaml

root, work = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])

POST_INSTALL = root / "tenants/platform/services/kube-prometheus-stack/postInstall"
CONFIG_SOURCE = POST_INSTALL / "alertmanager-config-externalsecret.yaml"
TEMPLATES_SOURCE = POST_INSTALL / "alertmanager-templates-configmap.yaml"
MATRIX = root / "tests/alertmanager-routing/routing-matrix.yaml"
CONFIG_KEY = "alertmanager.yaml"

# The mount path the Alertmanager pod sees, produced by
# alertmanagerSpec.configMaps in the service's values.yaml. Rewritten to the
# work dir below so `check-config` parses the real templates.
TEMPLATE_MOUNT = "/etc/alertmanager/configmaps/alertmanager-templates"

# Stand-ins for the two Vault-backed values ESO substitutes. Shaped like the
# real ones because Alertmanager validates them as URLs, and obviously fake so
# a rendered config in a CI log can never be mistaken for a live credential.
ESO_VALUES = {
    "discord_webhook_url": "https://discord.com/api/webhooks/000000000000000000/ci-placeholder",
    "webhook_token": "ci-placeholder",
}


def render_eso(text: str) -> str:
    """Evaluate the ESO v2 template actions this config uses, and only those.

    Two constructs appear here: `{{ .key }}`, which ESO replaces with the
    fetched secret value, and a backtick raw string, which exists purely to
    stop ESO from evaluating a Go template Alertmanager is meant to evaluate
    later. Anything else is refused rather than passed through — a construct
    this does not understand would otherwise reach amtool as literal text and
    be checked as if it were the config the cluster runs.
    """
    out: list[str] = []
    cursor = 0
    while True:
        start = text.find("{{", cursor)
        if start < 0:
            out.append(text[cursor:])
            return "".join(out)
        out.append(text[cursor:start])
        body: list[str] = []
        scan = start + 2
        while scan < len(text) and not text.startswith("}}", scan):
            # A raw string is consumed whole: the escaped Go template inside it
            # contains its own `}}`, which a naive scan would take as the end
            # of this action.
            if text[scan] == "`":
                close = text.find("`", scan + 1)
                if close < 0:
                    sys.exit(f"{CONFIG_SOURCE}: unterminated raw string in template action")
                body.append(text[scan : close + 1])
                scan = close + 1
                continue
            body.append(text[scan])
            scan += 1
        if scan >= len(text):
            sys.exit(f"{CONFIG_SOURCE}: unterminated template action starting at offset {start}")
        action = "".join(body).strip()
        if action.startswith("`") and action.endswith("`") and len(action) >= 2:
            out.append(action[1:-1])
        elif action.startswith(".") and action[1:] in ESO_VALUES:
            out.append(ESO_VALUES[action[1:]])
        else:
            sys.exit(
                f"{CONFIG_SOURCE}: template action {{{{ {action} }}}} is not one this renderer "
                "understands; add it to ESO_VALUES or teach render_eso the construct"
            )
        cursor = scan + 2


def extract_config() -> str:
    docs = [d for d in yaml.safe_load_all(CONFIG_SOURCE.read_text()) if isinstance(d, dict)]
    for doc in docs:
        if doc.get("kind") != "ExternalSecret":
            continue
        template = doc.get("spec", {}).get("target", {}).get("template", {})
        if template.get("engineVersion") != "v2":
            sys.exit(f"{CONFIG_SOURCE}: template engineVersion is not v2; the renderer assumes v2 semantics")
        data = template.get("data", {})
        if CONFIG_KEY in data:
            return data[CONFIG_KEY]
    sys.exit(f"{CONFIG_SOURCE}: no ExternalSecret carrying {CONFIG_KEY!r}")


def materialise_templates(dest: pathlib.Path) -> int:
    docs = [d for d in yaml.safe_load_all(TEMPLATES_SOURCE.read_text()) if isinstance(d, dict)]
    data = next((d.get("data", {}) for d in docs if d.get("kind") == "ConfigMap"), {})
    if not data:
        sys.exit(f"{TEMPLATES_SOURCE}: no ConfigMap data to write")
    dest.mkdir(parents=True, exist_ok=True)
    for name, body in data.items():
        (dest / name).write_text(body)
    return len(data)


def receivers_for(config: pathlib.Path, labels: dict) -> list[str]:
    args = [f"{k}={v}" for k, v in labels.items()]
    result = subprocess.run(
        ["amtool", "config", "routes", "test", f"--config.file={config}", *args],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        sys.exit(f"amtool config routes test failed for {labels}:\n{result.stderr.strip()}")
    # A labelset resolving to several receivers is printed as one
    # comma-separated line, not one line per receiver.
    return [r.strip() for r in result.stdout.replace("\n", ",").split(",") if r.strip()]


templates_dir = work / "templates"
count = materialise_templates(templates_dir)

rendered = render_eso(extract_config())
if TEMPLATE_MOUNT not in rendered:
    # `check-config` treats a template glob that matches nothing as fine, so a
    # renamed mount path would quietly turn the template check into a no-op
    # instead of failing. The whole point of pointing the glob at real files is
    # that the templates get parsed.
    sys.exit(f"{CONFIG_SOURCE}: expected a templates entry under {TEMPLATE_MOUNT}")
rendered = rendered.replace(TEMPLATE_MOUNT, str(templates_dir))

config = work / "alertmanager.yaml"
config.write_text(rendered)
print(f"rendered {CONFIG_KEY} with {count} template file(s) materialised")

check = subprocess.run(["amtool", "check-config", str(config)], capture_output=True, text=True)
sys.stdout.write(check.stdout)
sys.stderr.write(check.stderr)
if check.returncode != 0:
    sys.exit("amtool check-config failed")

cases = yaml.safe_load(MATRIX.read_text()).get("cases") or []
if not cases:
    sys.exit(f"{MATRIX}: no routing cases; an empty matrix is a gate that asserts nothing")

failures = 0
for case in cases:
    name, labels = case["name"], case["labels"]
    expected = case["receivers"]
    actual = receivers_for(config, labels)
    # Order is part of the assertion: amtool prints receivers in route-tree
    # order, so a reordered tree that still reaches the same set is a change
    # worth seeing.
    status = "ok" if actual == expected else "FAIL"
    if status == "FAIL":
        failures += 1
    print(f"  [{status}] {name}: {', '.join(actual) or '<none>'}")
    if status == "FAIL":
        print(f"         expected: {', '.join(expected) or '<none>'}")

print(f"routing matrix: {len(cases)} case(s), {failures} failure(s)")
if failures:
    sys.exit(1)
PY
