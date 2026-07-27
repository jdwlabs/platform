#!/usr/bin/env python3
"""Reject mutable (tag-only) container image references across
tenants/*/services/*/values.yaml and tenants/*/services/*/postInstall/**.

Unlike deployments (one image per chart, always under image.repository /
image.tag), this repo mixes vendored Helm chart values (image.repository +
image.tag maps) with raw Kubernetes manifests under postInstall/ (plain
`image: repo:tag` strings on containers/initContainers/CronJobs). Both shapes
are walked generically rather than hard-coded per service.

A live instance of exactly this class of bug is tracked separately
(kubelet-serving-cert-approver's Talos extraManifests URL points at an
unpinned `.../main/...` branch ref) — this check only covers image
references inside this repo's own YAML, not that Talos bootstrap patch.

This check requires every discovered image reference to be digest-pinned
(`<repo>[:tag]@sha256:<64 hex>`) or listed in
tools/image-pin-allowlist.yaml with an inline reason. Nothing is exempted
silently.

IMPORTANT — this check validates *format* only (a digest anchor is
present), never that the digest matches what a registry currently serves.
It never fetches a registry. If live digest verification is ever added
here, it MUST compare against the registry's manifest-list / OCI index
digest, not a per-arch child manifest digest — a `docker-content-digest`
response header for a multi-arch tag is the index digest, while a running
pod's imageID reports the digest of the single-arch child manifest it
pulled. Conflating the two manufactures drift that does not exist.

Usage:
    python3 tools/check-image-pins.py

Exit codes: 0 = no unexplained violations; 1 = one or more tag-only image
references outside the allowlist, or a malformed allowlist entry.
"""
import re
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
ALLOWLIST_FILE = REPO_ROOT / "tools/image-pin-allowlist.yaml"

TARGET_GLOBS = (
    "tenants/*/services/*/values.yaml",
    "tenants/*/services/*/postInstall/**/*.yaml",
)

# Matches a digest-pinned reference: "<anything>@sha256:<64 lowercase hex>"
# at the end of the string. Presence-only check — see module docstring for
# why this script never resolves or compares the digest itself.
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")


def discover_files() -> list[Path]:
    files = set()
    for pattern in TARGET_GLOBS:
        files.update(REPO_ROOT.glob(pattern))
    return sorted(files)


def bare_repository(image_ref: str) -> str:
    """Strip tag/digest from an image reference, e.g.
    'ghcr.io/foo/bar:v1@sha256:abc' -> 'ghcr.io/foo/bar', without mistaking
    a registry host's port (before the first '/') for a tag separator.
    """
    ref = image_ref.split("@", 1)[0]
    last_slash = ref.rfind("/")
    tail = ref[last_slash + 1 :]
    if ":" in tail:
        return ref[: ref.rfind(":")]
    return ref


def find_line(text: str, predicate) -> int | None:
    """First 1-based line number where predicate(stripped_line) is true."""
    for i, line in enumerate(text.splitlines(), start=1):
        if predicate(line.strip()):
            return i
    return None


# Some operators (e.g. local-path-provisioner) embed a full Pod/Job manifest
# as a ConfigMap string value rather than a real nested mapping. A structural
# walk alone would miss the image reference inside that blob entirely, so
# any string that looks like an embedded Kubernetes manifest gets re-parsed
# and walked too. Scoped to blobs that actually declare a Kubernetes object
# (apiVersion + kind) to avoid false-parsing unrelated blobs (Grafana
# dashboard JSON, alert templates, SQL) that happen to contain the substring
# "image:".
EMBEDDED_MANIFEST_HINT = re.compile(r"(?m)^\s*apiVersion:")


def walk(node, refs: list) -> None:
    if isinstance(node, dict):
        repository = node.get("repository")
        if isinstance(repository, str) and "tag" in node:
            tag = node.get("tag") or ""
            full_ref = f"{repository}:{tag}" if tag else repository
            refs.append({"kind": "helm", "repository": repository, "tag": tag, "full_ref": full_ref})
        image = node.get("image")
        if isinstance(image, str):
            refs.append({"kind": "raw", "repository": bare_repository(image), "tag": None, "full_ref": image})
        for value in node.values():
            walk(value, refs)
    elif isinstance(node, list):
        for item in node:
            walk(item, refs)
    elif isinstance(node, str) and "image:" in node and EMBEDDED_MANIFEST_HINT.search(node):
        try:
            for embedded_doc in yaml.safe_load_all(node):
                if embedded_doc is not None:
                    walk(embedded_doc, refs)
        except yaml.YAMLError:
            pass  # not actually parseable YAML — leave it to a human/other linter


def extract_refs(path: Path) -> list[dict]:
    text = path.read_text(encoding="utf-8")
    refs = []
    for doc in yaml.safe_load_all(text):
        if doc is not None:
            walk(doc, refs)
    return refs


def load_allowlist() -> dict[tuple[str, str], dict]:
    if not ALLOWLIST_FILE.exists():
        return {}
    data = yaml.safe_load(ALLOWLIST_FILE.read_text(encoding="utf-8")) or {}
    entries = data.get("exceptions", [])
    by_key = {}
    for i, entry in enumerate(entries):
        path = entry.get("path")
        repository = entry.get("repository")
        reason = entry.get("reason")
        if not path or not repository:
            raise SystemExit(
                f"{ALLOWLIST_FILE}: exceptions[{i}] missing required 'path' and/or 'repository'"
            )
        if not reason or not reason.strip():
            raise SystemExit(
                f"{ALLOWLIST_FILE}: exceptions[{i}] (path={path}, repository={repository}) "
                "missing a non-empty 'reason' — every exception must document inline why it "
                "is deliberate"
            )
        key = (path, repository)
        if key in by_key:
            raise SystemExit(f"{ALLOWLIST_FILE}: duplicate exception entry for {key}")
        by_key[key] = entry
    return by_key


def main() -> int:
    allowlist = load_allowlist()
    consumed = set()

    pinned, allowed, violations = [], [], []

    for path in discover_files():
        relpath = path.relative_to(REPO_ROOT).as_posix()
        text = path.read_text(encoding="utf-8")
        for ref in extract_refs(path):
            repository, tag, full_ref = ref["repository"], ref["tag"], ref["full_ref"]
            is_pinned = DIGEST_RE.search(full_ref) is not None
            if is_pinned:
                pinned.append((relpath, full_ref))
                continue

            if ref["kind"] == "helm":
                needle = tag if tag else repository
                key_field = "tag" if tag else "repository"
                line = find_line(text, lambda s, f=key_field, n=needle: s.startswith(f + ":") and n in s)
            else:
                line = find_line(text, lambda s, n=full_ref: s.startswith("image:") and n in s)
            location = f"{relpath}:{line}" if line else relpath

            key = (relpath, repository)
            entry = allowlist.get(key)
            if entry is not None:
                consumed.add(key)
                allowed.append((location, full_ref, entry["reason"]))
            else:
                violations.append((location, full_ref))

    stale = sorted(set(allowlist) - consumed)

    print(
        f"image-pin inventory: {len(pinned)} digest-pinned, {len(allowed)} allowlisted, "
        f"{len(violations)} unexplained\n"
    )

    if allowed:
        print("Allowlisted (see tools/image-pin-allowlist.yaml):")
        for location, full_ref, reason in allowed:
            print(f"  [{location}] {full_ref}\n    reason: {reason}")
        print()

    exit_code = 0

    if violations:
        exit_code = 1
        print("VIOLATIONS — mutable image reference with no digest pin and no allowlist entry:")
        for location, full_ref in violations:
            print(f"  [{location}] {full_ref}")
        print(
            "\nFix by pinning to '<repo>[:tag]@sha256:<index digest>' at the location above, "
            f"or add a documented exception to {ALLOWLIST_FILE.relative_to(REPO_ROOT).as_posix()}."
        )

    if stale:
        exit_code = 1
        print("\nSTALE ALLOWLIST ENTRIES — no longer match an unpinned reference (remove them):")
        for path, repository in stale:
            print(f"  {path} ({repository})")

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
