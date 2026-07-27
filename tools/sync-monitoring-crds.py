#!/usr/bin/env python3
"""Keep the 3 prometheus-operator CRDs vendored in bootstrap/crds/foundation-crds.yaml
in step with the kube-prometheus-stack chart revision pinned in
tenants/platform/tenant.yaml.

Those 3 CRDs (ServiceMonitor, PodMonitor, PrometheusRule) are hand-vendored
rather than installed by the kube-prometheus-stack chart because they must
exist at sync-wave -1, before wave-1/2 services create ServiceMonitor/
PodMonitor objects in their postInstall manifests. Nothing else moves this
bundle forward when the chart is bumped, so it silently drifts behind the
operator that actually reads it.

Usage:
    python3 tools/sync-monitoring-crds.py            # check only (CI mode)
    python3 tools/sync-monitoring-crds.py --write     # regenerate in place

Exit codes: 0 = in sync (check) / regenerated (write); 1 = drift detected
(check) or fetch/parse error.
"""
import argparse
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
TENANT_FILE = REPO_ROOT / "tenants/platform/tenant.yaml"
BUNDLE_FILE = REPO_ROOT / "bootstrap/crds/foundation-crds.yaml"
CHART_SERVICE_NAME = "kube-prometheus-stack"

# CRD basename -> full resource name, as vendored in foundation-crds.yaml.
CRDS = {
    "servicemonitors": "servicemonitors.monitoring.coreos.com",
    "podmonitors": "podmonitors.monitoring.coreos.com",
    "prometheusrules": "prometheusrules.monitoring.coreos.com",
}

UPSTREAM_URL = (
    "https://raw.githubusercontent.com/prometheus-community/helm-charts/"
    "kube-prometheus-stack-{revision}/charts/kube-prometheus-stack/charts/crds/crds/"
    "crd-{basename}.yaml"
)

VERSION_RE = re.compile(r"operator\.prometheus\.io/version:\s*(\S+)")


def chart_revision() -> str:
    tenant = yaml.safe_load(TENANT_FILE.read_text(encoding="utf-8"))
    for service in tenant.get("services", []):
        if service.get("name") == CHART_SERVICE_NAME:
            return str(service["revision"])
    raise SystemExit(
        f"no '{CHART_SERVICE_NAME}' service found in {TENANT_FILE} "
        "— tenant.yaml layout has changed, update tools/sync-monitoring-crds.py"
    )


def fetch(basename: str, revision: str) -> str:
    url = UPSTREAM_URL.format(revision=revision, basename=basename)
    try:
        with urllib.request.urlopen(url, timeout=30) as resp:
            text = resp.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        raise SystemExit(
            f"fetch failed for {basename} at chart revision {revision}: "
            f"{exc} ({url})\n"
            "The chart's CRD subdirectory layout may have changed upstream — "
            "check charts/kube-prometheus-stack/charts/crds/crds/ at that tag "
            "and update UPSTREAM_URL in this script."
        ) from exc
    # Upstream ships a leading '---\n# <source url>\n---\n' header before the
    # actual CRD document. Splicing that in verbatim would add a spurious
    # empty document to foundation-crds.yaml's document list, so strip down
    # to the single real document (starting at 'apiVersion:').
    match = re.search(r"(?m)^apiVersion:", text)
    if not match:
        raise SystemExit(f"unexpected upstream format for {basename}, no 'apiVersion:' line found ({url})")
    return text[match.start():]


def split_documents(text: str) -> list[str]:
    # foundation-crds.yaml documents are separated by a bare '---' line; the
    # first document has no leading separator.
    return re.split(r"(?m)^---\s*$\n?", text)


def doc_crd_name(doc: str) -> str | None:
    match = re.search(r"(?m)^  name:\s*(\S+)$", doc)
    return match.group(1) if match else None


def doc_version(doc: str) -> str | None:
    match = VERSION_RE.search(doc)
    return match.group(1) if match else None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--write",
        action="store_true",
        help="regenerate the 3 CRD documents in place instead of only checking",
    )
    args = parser.parse_args()

    revision = chart_revision()
    bundle_text = BUNDLE_FILE.read_text(encoding="utf-8")
    docs = split_documents(bundle_text)
    by_name = {doc_crd_name(d): i for i, d in enumerate(docs) if doc_crd_name(d)}

    mismatches = []
    fetched_by_basename = {}
    for basename, full_name in CRDS.items():
        idx = by_name.get(full_name)
        if idx is None:
            raise SystemExit(f"{full_name} not found in {BUNDLE_FILE} — bundle layout changed")
        current_version = doc_version(docs[idx])
        upstream_text = fetch(basename, revision)
        fetched_by_basename[basename] = upstream_text
        upstream_version = doc_version(upstream_text)
        if current_version != upstream_version:
            mismatches.append((full_name, current_version, upstream_version))

    if not args.write:
        if mismatches:
            print(
                f"foundation-crds.yaml is stale for kube-prometheus-stack "
                f"chart revision {revision}:\n"
            )
            for name, current, upstream in mismatches:
                print(f"  {name}: vendored={current} chart-pins={upstream}")
            print(
                "\nRegenerate with: python3 tools/sync-monitoring-crds.py --write"
            )
            return 1
        print(f"foundation-crds.yaml is in sync with chart revision {revision}")
        return 0

    if not mismatches:
        print(f"foundation-crds.yaml already in sync with chart revision {revision}")
        return 0

    for basename, full_name in CRDS.items():
        idx = by_name[full_name]
        docs[idx] = fetched_by_basename[basename].strip("\n") + "\n"

    # split_documents strips the bare '---' separators; recreate them for every
    # document after the first (which never had one to begin with).
    new_text = docs[0] + "".join(f"---\n{d}" for d in docs[1:])
    BUNDLE_FILE.write_text(new_text, encoding="utf-8")
    for name, current, upstream in mismatches:
        print(f"  {name}: {current} -> {upstream}")
    print(f"\nRegenerated foundation-crds.yaml for chart revision {revision}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
