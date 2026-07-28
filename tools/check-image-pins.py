#!/usr/bin/env python3
"""Reject mutable (tag-only) container image references across every YAML
file under tenants/ and helm-charts/.

Unlike deployments (one image per chart, always under image.repository /
image.tag), this repo mixes vendored Helm chart values (image.repository +
image.tag maps, sometimes with a separate registry/digest field) with raw
Kubernetes manifests under postInstall/ (plain `image: repo:tag` strings on
containers/initContainers/CronJobs), plus in-house and vendored charts under
helm-charts/ whose own values.yaml is the deployed configuration. All shapes
are walked generically rather than hard-coded per service.

Coverage notes:
  - helm-charts/ is in scope because several platform services deploy from
    it via `chartPath` in tenants/platform/tenant.yaml, and their tenant
    overlays override little or nothing — the chart's own values.yaml is
    what reaches the cluster.
  - Files under a chart's templates/ directory are Go text/template, not
    YAML, so they are scanned with a literal-reference regex instead of a
    YAML parse. Only a hard-coded (non-templated) reference can be caught
    there; a `{{ .Values.image.repository }}` reference is covered by the
    values.yaml it reads from.

A live instance of exactly this class of bug is tracked separately
(kubelet-serving-cert-approver's Talos extraManifests URL points at an
unpinned `.../main/...` branch ref) — this check only covers image
references inside this repo's own YAML, not that Talos bootstrap patch.

This check requires every discovered image reference to be digest-pinned
(`<repo>[:tag]@sha256:<64 hex>`) or listed in
tools/image-pin-allowlist.yaml with an inline reason. Nothing is exempted
silently. An allowlist entry is keyed on the *full* reference including its
tag, so bumping an exempted image to a materially different version loses
the exemption instead of inheriting it.

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
    "tenants/**/*.yaml",
    "tenants/**/*.yml",
    "helm-charts/**/*.yaml",
    "helm-charts/**/*.yml",
)

# Matches a digest-pinned reference: "<anything>@sha256:<64 lowercase hex>"
# at the end of the string. Presence-only check — see module docstring for
# why this script never resolves or compares the digest itself.
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")


class StrScalarLoader(yaml.SafeLoader):
    """SafeLoader that keeps every plain scalar as its literal source text.

    An unquoted `tag: 1.0` otherwise resolves to a Python float, which both
    breaks every string operation downstream and silently rewrites a tag
    like `1.10` into `1.1`. Tags, repositories and digests are always text,
    so resolving them as anything else can only lose information.

    Subclassing SafeLoader keeps the safe constructor set: this narrows what
    YAML may construct (scalars only ever become str), it never widens it,
    so `!!python/object` remains unconstructable.
    """


for _implicit_type in ("bool", "int", "float", "timestamp"):
    StrScalarLoader.add_constructor(
        f"tag:yaml.org,2002:{_implicit_type}",
        lambda loader, node: loader.construct_scalar(node),
    )


def load_all_yaml(text: str):
    return yaml.load_all(text, Loader=StrScalarLoader)


def as_str(value) -> str:
    """Coerce a YAML scalar to text without ever raising.

    StrScalarLoader already keeps scalars as text, but references also
    arrive from embedded manifests and from callers passing plain dicts, so
    every value consumed as part of a reference goes through here.
    """
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def is_image_key(key) -> bool:
    """A key holds an image reference if it is `image` or a camelCase /
    snake_case suffix of it (initImage, sidecarImage, dbReadyImage,
    init_image). Deliberately does not match imagePullPolicy,
    imagePullSecrets or imageRegistry, whose values are not references.
    """
    if not isinstance(key, str):
        return False
    return key == "image" or key.endswith("Image") or key.endswith("_image")


# Values syntactically incapable of being an image reference: whitespace, a
# URL (Helm *chart* repositories also use the key `repository`), a
# filesystem path, or an un-rendered Go template expression.
NON_REFERENCE_RE = re.compile(r"\s|://|\{\{|\}\}")
BOOLISH = frozenset({"true", "false", "yes", "no", "on", "off", "null", "~", ""})
VERSION_ONLY_RE = re.compile(r"^[0-9][0-9.]*$")


def looks_like_image_reference(value) -> bool:
    ref = as_str(value).strip()
    if not ref or ref.lower() in BOOLISH:
        return False
    if NON_REFERENCE_RE.search(ref):
        return False
    if ref.startswith("/") or ref.startswith("."):
        return False
    if VERSION_ONLY_RE.match(ref):
        return False
    return True


def sibling_tag_key(image_key: str) -> str | None:
    """The key holding the tag for a split repository/tag pair, e.g.
    `dbReadyImage` -> `dbReadyTag`. Some charts (litellm-helm's db-ready init
    container among them) keep the repository and the tag in two sibling
    scalars that the template joins with a colon, so neither key alone is the
    reference that gets deployed.
    """
    if image_key == "image":
        return "tag"
    if image_key.endswith("Image"):
        return image_key[: -len("Image")] + "Tag"
    if image_key.endswith("_image"):
        return image_key[: -len("_image")] + "_tag"
    return None


def has_tag_or_digest(image_ref: str) -> bool:
    """Whether a reference already carries its own tag or digest. Only the
    last path segment is inspected, so a registry host's port
    (registry.local:5000/team/app) is not mistaken for a tag separator.
    """
    if "@" in image_ref:
        return True
    return ":" in image_ref[image_ref.rfind("/") + 1 :]


def compose_reference(registry: str, repository: str, tag: str, digest: str) -> str:
    """Rebuild the reference a chart template renders from a structured
    {registry, repository, tag, digest} block (the Bitnami/common shape).
    """
    ref = repository
    if registry and not repository.startswith(registry + "/"):
        ref = f"{registry}/{repository}"
    if tag:
        ref = f"{ref}:{tag}"
    if digest:
        ref = f"{ref}@{digest}"
    return ref


def find_line(text: str, key: str, needle: str) -> int | None:
    """First 1-based line number declaring `key:` and mentioning `needle`."""
    prefix = f"{key}:"
    for i, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("- "):
            stripped = stripped[2:].strip()
        if stripped.startswith(prefix) and needle in stripped:
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

# A literal image reference in a Go-templated chart file: `image: repo:tag`
# with no template expression in the value. Used only for templates/ files,
# which cannot be YAML-parsed.
TEMPLATE_IMAGE_RE = re.compile(
    r"^\s*(?:-\s+)?(?P<key>[A-Za-z0-9_]*[Ii]mage):\s*[\"']?(?P<ref>[^\"'\s]+)[\"']?\s*$"
)


def walk(node, refs: list) -> None:
    if isinstance(node, dict):
        repository = node.get("repository")
        if looks_like_image_reference(repository):
            # A structured image block is a reference whether or not it
            # carries a `tag` key: a missing tag means the chart template
            # falls back to appVersion or to an implicit `latest`, which is
            # the most mutable case of all, not an exempt one.
            repository = as_str(repository)
            full_ref = compose_reference(
                as_str(node.get("registry")).strip(),
                repository,
                as_str(node.get("tag")).strip(),
                as_str(node.get("digest")).strip(),
            )
            refs.append(
                {
                    "kind": "helm",
                    "key": "repository",
                    "needle": repository,
                    "full_ref": full_ref,
                }
            )
        for key, value in node.items():
            if is_image_key(key) and looks_like_image_reference(value):
                image = as_str(value)
                tag_key = sibling_tag_key(key)
                sibling = as_str(node.get(tag_key)).strip() if tag_key else ""
                full_ref = image
                if sibling and not has_tag_or_digest(image):
                    full_ref = f"{image}:{sibling}"
                refs.append(
                    {"kind": "raw", "key": key, "needle": image, "full_ref": full_ref}
                )
            walk(value, refs)
    elif isinstance(node, list):
        for item in node:
            walk(item, refs)
    elif isinstance(node, str) and "image:" in node and EMBEDDED_MANIFEST_HINT.search(node):
        try:
            for embedded_doc in load_all_yaml(node):
                if embedded_doc is not None:
                    walk(embedded_doc, refs)
        except yaml.YAMLError:
            pass  # not actually parseable YAML — leave it to a human/other linter


def is_chart_template(path: Path) -> bool:
    return "templates" in path.parts


def extract_refs_from_template(text: str) -> list[dict]:
    refs = []
    for line in text.splitlines():
        match = TEMPLATE_IMAGE_RE.match(line)
        if not match:
            continue
        key, ref = match.group("key"), match.group("ref")
        if not is_image_key(key) or not looks_like_image_reference(ref):
            continue
        refs.append({"kind": "raw", "key": key, "needle": ref, "full_ref": ref})
    return refs


def extract_refs(path: Path) -> list[dict]:
    text = path.read_text(encoding="utf-8")
    if is_chart_template(path):
        return extract_refs_from_template(text)
    refs = []
    for doc in load_all_yaml(text):
        if doc is not None:
            walk(doc, refs)
    return refs


def discover_files() -> list[Path]:
    files = set()
    for pattern in TARGET_GLOBS:
        files.update(REPO_ROOT.glob(pattern))
    return sorted(files)


def load_allowlist() -> dict[tuple[str, str], dict]:
    if not ALLOWLIST_FILE.exists():
        return {}
    data = yaml.load(ALLOWLIST_FILE.read_text(encoding="utf-8"), Loader=StrScalarLoader) or {}
    entries = data.get("exceptions", [])
    by_key = {}
    for i, entry in enumerate(entries):
        path = entry.get("path")
        ref = entry.get("ref")
        reason = entry.get("reason")
        if not path or not ref:
            raise SystemExit(
                f"{ALLOWLIST_FILE}: exceptions[{i}] missing required 'path' and/or 'ref'. "
                "'ref' is the full reference including its tag (e.g. 'redis:7.4-alpine'), "
                "so that bumping the tag re-opens the question instead of inheriting the "
                "exemption."
            )
        if not reason or not reason.strip():
            raise SystemExit(
                f"{ALLOWLIST_FILE}: exceptions[{i}] (path={path}, ref={ref}) "
                "missing a non-empty 'reason' — every exception must document inline why it "
                "is deliberate"
            )
        key = (path, ref)
        if key in by_key:
            raise SystemExit(f"{ALLOWLIST_FILE}: duplicate exception entry for {key}")
        by_key[key] = entry
    return by_key


def collect(files: list[Path], allowlist: dict):
    """Classify every discovered reference. Returns
    (pinned, allowed, violations, consumed_allowlist_keys).
    """
    pinned, allowed, violations = [], [], []
    consumed = set()
    seen = set()

    for path in files:
        relpath = path.relative_to(REPO_ROOT).as_posix()
        text = path.read_text(encoding="utf-8")
        for ref in extract_refs(path):
            full_ref = ref["full_ref"]
            key = (relpath, full_ref)
            if key in seen:
                continue  # same reference repeated in one file — one finding
            seen.add(key)

            if DIGEST_RE.search(full_ref) is not None:
                pinned.append((relpath, full_ref))
                continue

            line = find_line(text, ref["key"], ref["needle"])
            location = f"{relpath}:{line}" if line else relpath

            entry = allowlist.get(key)
            if entry is not None:
                consumed.add(key)
                allowed.append((location, full_ref, entry["reason"]))
            else:
                violations.append((location, full_ref))

    return pinned, allowed, violations, consumed


def main() -> int:
    allowlist = load_allowlist()
    pinned, allowed, violations, consumed = collect(discover_files(), allowlist)
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
        for path, ref in stale:
            print(f"  {path} ({ref})")

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
