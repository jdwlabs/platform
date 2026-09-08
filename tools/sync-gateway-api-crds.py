#!/usr/bin/env python3
"""Keep the Gateway API CRDs vendored in bootstrap/crds/foundation-crds.yaml
byte-identical to the upstream release artifact pinned in
tools/gateway-api-crd-pin.yaml.

Those CRDs are hand-vendored at sync-wave -1 so they exist before anything
that instantiates them. No chart, tenant entry or image digest anywhere in
this repo names a Gateway API version, so until this pin existed there was
nothing for a check to compare against and the bundle aged five upstream
minors without a single gate going red — the skew surfaced only as a
GatewayClass condition that nobody reads on a schedule.

The bundle file is shared with tools/sync-monitoring-crds.py, which owns the
3 prometheus-operator CRDs at its tail. Ownership is by CRD name, never by
position, so each tool rewrites only its own documents and both can run in
either order.

Usage:
    python3 tools/sync-gateway-api-crds.py            # check only (CI mode)
    python3 tools/sync-gateway-api-crds.py --write    # regenerate in place

Exit codes: 0 = in sync (check) / regenerated (write); 1 = drift detected
(check) or fetch/parse/pin error.
"""
import argparse
import difflib
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
PIN_FILE = REPO_ROOT / "tools/gateway-api-crd-pin.yaml"
BUNDLE_FILE = REPO_ROOT / "bootstrap/crds/foundation-crds.yaml"

RELEASE_URL = (
    "https://github.com/kubernetes-sigs/gateway-api/releases/download/"
    "{version}/{channel}-install.yaml"
)

# Both API groups the project publishes: the experimental channel adds
# x-k8s.io kinds, and one appearing in the bundle unpinned must be caught.
GROUP_SUFFIXES = (".gateway.networking.k8s.io", ".gateway.networking.x-k8s.io")

BUNDLE_VERSION_ANNOTATION = "gateway.networking.k8s.io/bundle-version"
CHANNEL_ANNOTATION = "gateway.networking.k8s.io/channel"

DIFF_LINE_LIMIT = 20


def load_pin() -> tuple[str, str, list[str], dict[str, str]]:
    pin = yaml.safe_load(PIN_FILE.read_text(encoding="utf-8"))
    try:
        version = str(pin["version"])
        channel = str(pin["channel"])
        vendored = [str(name) for name in pin["vendored"]]
    except (KeyError, TypeError) as exc:
        raise SystemExit(
            f"{PIN_FILE} is missing a required key ({exc}) — it must set "
            "'version', 'channel' and 'vendored'"
        ) from exc
    not_vendored = {str(k): str(v) for k, v in (pin.get("notVendored") or {}).items()}
    both = sorted(set(vendored) & set(not_vendored))
    if both:
        raise SystemExit(
            f"{PIN_FILE} lists {', '.join(both)} under both 'vendored' and "
            "'notVendored' — a CRD is either in the bundle or it is not"
        )
    return version, channel, vendored, not_vendored


def fetch_release(version: str, channel: str) -> str:
    url = RELEASE_URL.format(version=version, channel=channel)
    try:
        with urllib.request.urlopen(url, timeout=60) as resp:
            return resp.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        raise SystemExit(
            f"no {channel}-install.yaml asset for Gateway API {version}: {exc} ({url})\n"
            f"Check that 'version' and 'channel' in {PIN_FILE} name a real release "
            "asset — a tag with no such channel returns 404 here."
        ) from exc
    except urllib.error.URLError as exc:
        raise SystemExit(
            f"could not reach the Gateway API release artifact: {exc} ({url})\n"
            "This is a network failure, not bundle drift — re-run the job."
        ) from exc


def split_documents(text: str) -> list[str]:
    # foundation-crds.yaml documents are separated by a bare '---' line; the
    # first document has no leading separator.
    return re.split(r"(?m)^---\s*$\n?", text)


def doc_kind(doc: str) -> str | None:
    match = re.search(r"(?m)^kind:\s*(\S+)$", doc)
    return match.group(1) if match else None


def doc_crd_name(doc: str) -> str | None:
    match = re.search(r"(?m)^  name:\s*(\S+)$", doc)
    return match.group(1) if match else None


def doc_annotation(doc: str, key: str) -> str | None:
    match = re.search(rf"(?m)^\s+{re.escape(key)}:\s*(\S+)$", doc)
    return match.group(1) if match else None


def normalize(doc: str) -> str:
    # Upstream prefixes each document with a '# config/crd/<path>' source
    # comment that carries no API meaning and has never been vendored;
    # comparing without it is what makes a byte-for-byte check possible.
    match = re.search(r"(?m)^apiVersion:", doc)
    return doc[match.start():].strip("\n") if match else doc.strip("\n")


def crd_documents(text: str) -> dict[str, str]:
    # Release artifacts from v1.5.0 on also carry a ValidatingAdmissionPolicy
    # and its binding. Those are staged separately from the CRDs by design, so
    # only CustomResourceDefinition documents are in scope here.
    found = {}
    for doc in split_documents(text):
        if doc_kind(doc) != "CustomResourceDefinition":
            continue
        name = doc_crd_name(doc)
        if name:
            found[name] = normalize(doc)
    return found


def bundle_positions(docs: list[str]) -> dict[str, int]:
    positions = {}
    for index, doc in enumerate(docs):
        if doc_kind(doc) != "CustomResourceDefinition":
            continue
        name = doc_crd_name(doc)
        if name:
            positions[name] = index
    return positions


def diff_lines(name: str, vendored_doc: str, upstream_doc: str) -> list[str]:
    diff = list(
        difflib.unified_diff(
            vendored_doc.splitlines(),
            upstream_doc.splitlines(),
            fromfile=f"vendored/{name}",
            tofile=f"upstream/{name}",
            lineterm="",
            n=1,
        )
    )
    if len(diff) <= DIFF_LINE_LIMIT:
        return diff
    return diff[:DIFF_LINE_LIMIT] + [f"... ({len(diff) - DIFF_LINE_LIMIT} more diff lines)"]


def report_drift(
    version: str,
    channel: str,
    missing: list[str],
    mismatched: list[str],
    docs: list[str],
    positions: dict[str, int],
    upstream: dict[str, str],
) -> None:
    print(
        f"foundation-crds.yaml has drifted from Gateway API {version} "
        f"({channel} channel):\n"
    )
    for name in missing:
        print(f"  {name}: pinned but not present in the bundle")
    for name in mismatched:
        vendored_doc = normalize(docs[positions[name]])
        vendored_version = doc_annotation(vendored_doc, BUNDLE_VERSION_ANNOTATION)
        vendored_channel = doc_annotation(vendored_doc, CHANNEL_ANNOTATION)
        if (vendored_version, vendored_channel) != (version, channel):
            # A whole-bundle move: the line-by-line diff would be thousands of
            # lines of schema and would say nothing the annotations do not.
            print(
                f"  {name}: vendored {vendored_version}/{vendored_channel}, "
                f"pin expects {version}/{channel}"
            )
            continue
        print(f"  {name}: content differs from the pinned release artifact")
        for line in diff_lines(name, vendored_doc, upstream[name]):
            print(f"    {line}")
    print("\nRegenerate with: python3 tools/sync-gateway-api-crds.py --write")


def check_pin_covers_release(
    version: str,
    channel: str,
    vendored: list[str],
    not_vendored: dict[str, str],
    upstream: dict[str, str],
) -> None:
    absent = [name for name in vendored if name not in upstream]
    if absent:
        raise SystemExit(
            f"{PIN_FILE} vendors {', '.join(absent)}, which the {channel} channel of "
            f"Gateway API {version} does not ship.\n"
            "Dropping a CRD from the bundle prunes every object of that kind from "
            "the cluster, so resolve this deliberately rather than by deleting the "
            "entry."
        )
    unclassified = sorted(
        name for name in upstream if name not in vendored and name not in not_vendored
    )
    if unclassified:
        raise SystemExit(
            f"Gateway API {version} ({channel} channel) ships CRDs that {PIN_FILE} "
            "does not account for:\n"
            + "".join(f"  {name}\n" for name in unclassified)
            + "Add each one to 'vendored' (then run --write) or to 'notVendored' "
            "with the reason it is skipped."
        )


def check_bundle_is_pinned(vendored: list[str], positions: dict[str, int]) -> None:
    unpinned = sorted(
        name
        for name in positions
        if name.endswith(GROUP_SUFFIXES) and name not in vendored
    )
    if unpinned:
        raise SystemExit(
            f"{BUNDLE_FILE} vendors Gateway API CRDs that {PIN_FILE} does not list:\n"
            + "".join(f"  {name}\n" for name in unpinned)
            + "Every Gateway API CRD in the bundle is applied to the cluster, so "
            "every one of them has to be pinned or nothing checks it."
        )
    if not any(name in positions for name in vendored):
        raise SystemExit(
            f"no pinned Gateway API CRD found in {BUNDLE_FILE} — bundle layout "
            "changed, update tools/sync-gateway-api-crds.py"
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--write",
        action="store_true",
        help="regenerate the pinned CRD documents in place instead of only checking",
    )
    args = parser.parse_args()

    version, channel, vendored, not_vendored = load_pin()
    upstream = crd_documents(fetch_release(version, channel))
    check_pin_covers_release(version, channel, vendored, not_vendored, upstream)

    bundle_text = BUNDLE_FILE.read_text(encoding="utf-8")
    docs = split_documents(bundle_text)
    positions = bundle_positions(docs)
    check_bundle_is_pinned(vendored, positions)

    present = [name for name in vendored if name in positions]
    missing = [name for name in vendored if name not in positions]
    mismatched = [
        name for name in present if normalize(docs[positions[name]]) != upstream[name]
    ]

    if not args.write:
        if missing or mismatched:
            report_drift(version, channel, missing, mismatched, docs, positions, upstream)
            return 1
        print(
            f"foundation-crds.yaml matches Gateway API {version} ({channel} channel) "
            f"for all {len(vendored)} pinned CRDs"
        )
        return 0

    if not missing and not mismatched:
        print(
            f"foundation-crds.yaml already matches Gateway API {version} "
            f"({channel} channel)"
        )
        return 0

    for name in mismatched:
        docs[positions[name]] = upstream[name] + "\n"
    # Newly pinned CRDs are appended after the last Gateway API document so the
    # prometheus-operator tail keeps its order; sync-monitoring-crds.py finds
    # its own documents by name, so the index shift is nothing to it.
    insert_at = max(positions[name] for name in present) + 1
    for offset, name in enumerate(missing):
        docs.insert(insert_at + offset, upstream[name] + "\n")

    # split_documents strips the bare '---' separators; recreate them for every
    # document after the first (which never had one to begin with).
    new_text = docs[0] + "".join(f"---\n{doc}" for doc in docs[1:])

    # The bundle is applied by an Application that prunes, and pruning a CRD
    # deletes every object of that kind. A regeneration that loses a name is a
    # cluster-wide outage delivered by a green sync, so refuse to write one.
    before = set(bundle_positions(split_documents(bundle_text)))
    after = set(bundle_positions(split_documents(new_text)))
    if before - after:
        raise SystemExit(
            "refusing to write: regeneration would drop "
            f"{', '.join(sorted(before - after))} from {BUNDLE_FILE}, which would "
            "prune every object of that kind from the cluster"
        )

    BUNDLE_FILE.write_text(new_text, encoding="utf-8")
    for name in missing:
        print(f"  {name}: added")
    for name in mismatched:
        print(f"  {name}: updated")
    print(
        f"\nRegenerated foundation-crds.yaml for Gateway API {version} "
        f"({channel} channel)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
