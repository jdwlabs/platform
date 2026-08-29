#!/usr/bin/env python3
"""Reject mutable (tag-only) container image references in a delivery repo.

This is the org's digest-pinning gate. One copy of this file is canonical
(jdwlabs/.github, tools/check-image-pins.py); every delivery repo vendors it
byte-for-byte and its CI compares the vendored copy against the canonical
one at a pinned commit, so a rule fix cannot land in one repo and silently
miss another. Everything that differs between repos — which files are
scanned, how chart values are layered, where the allowlist lives — is
declared in a per-repo config file next to the script, never in the script.

Why the gate exists: a bare tag (or no tag at all, falling back to a chart's
appVersion or an implicit `latest`) is mutable. A republished tag or a chart
bump silently changes what a node pulls next, independent of anything
committed to the repo. That already caused an outage: a reused `1.0.0`
servicediscovery tag was republished, and `imagePullPolicy: IfNotPresent`
meant nodes kept serving a build predating a new handler while every probe
stayed green.

Configuration — tools/image-pin-check.yaml, relative to the repo root:

    allowlist: tools/image-pin-allowlist.yaml
    sources:
      - kind: helm-overlays      # charts/<chart>/values-<env>.yaml layered
        charts: charts           # onto values.yaml, Helm-style
      - kind: tree               # every YAML file matching the globs
        globs:
          - tenants/**/*.yaml
          - charts/*/templates/**/*

Source kinds:

  * helm-overlays — every app chart under `charts` (library charts excluded)
    is deployed with a values.yaml base plus a values-<env>.yaml overlay per
    environment; the overlay decides the image actually pulled. Overlays are
    discovered by glob, so a new environment is checked the moment its
    overlay exists. A non-library chart with no overlay at all is a failure,
    not a silent pass: it would otherwise contribute zero references and
    read as "clean".
  * tree — every file matching the globs is walked as YAML. Files under a
    templates/ directory are Go text/template rather than YAML and are
    scanned line-by-line for literal `image:` references instead; a value
    that is a `{{ ... }}` expression resolves from values, which the values
    scan covers.

Every reference reachable in a values tree is checked, not only the
top-level `image` map: `sidecar.image.{repository,tag}` maps (with optional
`registry` and `digest` fields), `<name>Image`/`<name>_image` string keys
with an optional sibling `<name>Tag`, raw `image: repo:tag` strings inside
containers/initContainers/CronJobs, references in lists, and Kubernetes
manifests embedded as ConfigMap string values.

Every discovered reference must be digest-pinned (`...@sha256:<64 hex>`) or
listed in the allowlist with an inline reason. Nothing is exempted silently.
An entry is keyed on (path, full reference including its tag), so it stops
applying the moment the reference it excused changes — an exception cannot
outlive the thing it was written for — and an entry that no longer matches
anything is reported stale.

Malformed references are reported separately and are NOT allowlistable,
because no reason makes an unresolvable reference correct:

  * a digest-only `tag:` (`tag: "@sha256:..."`) in a structured image block.
    It contains a well-formed digest, but a chart renders
    `{{ .repository }}:{{ .tag }}` unconditionally, producing `repo:@sha256:…`
    — an empty tag followed by junk that only the kubelet rejects, at pull
    time. (A raw string `repo@sha256:…` with no tag is valid and accepted.)
  * an unquoted numeric or boolean tag (`tag: 1.10`). YAML resolves it as a
    float before Helm sees it as a version, so it deploys `1.1` — a different
    image than the author typed. The literal text is preserved in the report.
  * a tag outside the OCI grammar, or a truncated / non-sha256 digest.

IMPORTANT — this check validates *format* only (a digest anchor is present),
never that the digest matches what a registry currently serves. It never
fetches a registry. If live digest verification is ever added, it MUST
compare against the registry's manifest-list / OCI index digest, not a
per-arch child manifest digest: a `docker-content-digest` header for a
multi-arch tag is the index digest, while a running pod's imageID reports
the single-arch child manifest it pulled. Comparing those two manufactures
drift that does not exist.

Usage:
    python3 tools/check-image-pins.py [--repo-root DIR] [--config FILE]

Exit codes: 0 = no unexplained violations; 1 = a tag-only reference outside
the allowlist, a malformed reference, an unchecked chart, a stale allowlist
entry, or a malformed allowlist or config entry.
"""
import argparse
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

import yaml

CONFIG_RELPATH = "tools/image-pin-check.yaml"
DEFAULT_ALLOWLIST_RELPATH = "tools/image-pin-allowlist.yaml"

# Presence-only check for a digest-pinned reference — see the module
# docstring for why this script never resolves or compares the digest.
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")

# The digest component on its own, for validating what follows an '@'.
DIGEST_COMPONENT_RE = re.compile(r"^sha256:[0-9a-f]{64}$")

# OCI/Docker reference grammar for the tag component. A tag may not start
# with '.' or '-', and may not contain '@' or ':' — which is exactly why a
# digest-only tag cannot be concatenated into a valid reference.
TAG_RE = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$")

# A literal image line in a Go-templated chart file: `image: repo:tag` or a
# `<name>Image:` key. `[^#]+?` stops before a trailing comment. Nothing
# else matches because the colon must follow the key directly
# (imagePullPolicy, imagePullSecrets and imageRegistry are excluded by
# is_image_key on the captured key).
TEMPLATE_IMAGE_RE = re.compile(
    r"^\s*(?:-\s+)?(?P<key>[A-Za-z0-9_]*[Ii]mage):\s*(?P<value>[^#]+?)\s*(?:#.*)?$"
)

TEMPLATE_SUFFIXES = (".yaml", ".yml", ".tpl")

# Values syntactically incapable of being an image reference: whitespace, a
# URL (Helm *chart* repositories also use the key `repository`), a
# filesystem path, or an un-rendered Go template expression.
NON_REFERENCE_RE = re.compile(r"\s|://|\{\{|\}\}")
BOOLISH = frozenset({"true", "false", "yes", "no", "on", "off", "null", "~", ""})
VERSION_ONLY_RE = re.compile(r"^[0-9][0-9.]*$")

# Some operators (local-path-provisioner among them) embed a full Pod/Job
# manifest as a ConfigMap string value rather than a real nested mapping.
# Scoped to blobs that actually declare a Kubernetes object so unrelated
# blobs (Grafana dashboard JSON, alert templates, SQL) that happen to contain
# "image:" are not parsed.
EMBEDDED_MANIFEST_HINT = re.compile(r"(?m)^\s*apiVersion:")


class UnquotedScalar(str):
    """A plain scalar YAML would have resolved to a non-string type, kept as
    its literal source text. `yaml_type` records what YAML would have made
    of it, so a `tag: 1.10` can be reported as an unquoted number while
    still showing the author's `1.10` rather than a lossy `1.1`."""

    yaml_type: str = ""

    def __new__(cls, text: str, yaml_type: str):
        obj = super().__new__(cls, text)
        obj.yaml_type = yaml_type
        return obj


class StrScalarLoader(yaml.SafeLoader):
    """SafeLoader that keeps every plain scalar as its literal source text.

    Tags, repositories and digests are always text, so resolving them as
    anything else can only lose information. Subclassing SafeLoader narrows
    what YAML may construct (scalars only ever become str); it never widens
    it, so `!!python/object` remains unconstructable. `null` is left alone:
    an explicit null in a values overlay is how Helm clears a default.
    """


def _make_unquoted_constructor(yaml_type: str):
    def construct(loader, node):
        return UnquotedScalar(loader.construct_scalar(node), yaml_type)

    return construct


for _implicit_type in ("bool", "int", "float", "timestamp"):
    StrScalarLoader.add_constructor(
        f"tag:yaml.org,2002:{_implicit_type}", _make_unquoted_constructor(_implicit_type)
    )


def load_yaml_text(text: str):
    return yaml.load(text, Loader=StrScalarLoader)


def load_all_yaml(text: str):
    return yaml.load_all(text, Loader=StrScalarLoader)


def load_yaml(path: Path) -> dict:
    if not path.exists():
        return {}
    return load_yaml_text(path.read_text(encoding="utf-8")) or {}


def as_str(value) -> str:
    """Coerce a YAML scalar to text without ever raising. StrScalarLoader
    already keeps scalars as text, but values also arrive from callers
    passing plain dicts, so everything consumed as part of a reference goes
    through here."""
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


@dataclass
class Ref:
    """One image reference, normalised so structured maps, split image/tag
    keys and raw strings compare the same way. `path` is the file an author
    would edit to fix it, which for a values overlay is the overlay even
    when the value was inherited from values.yaml. `tag` carries any digest
    suffix, matching how a values `tag:` field carries one."""

    path: str
    line: int | None
    where: str
    repository: str
    tag: str
    full_ref: str
    fallback_hint: str = ""
    problem: str | None = None

    @property
    def location(self) -> str:
        return f"{self.path}:{self.line}" if self.line else self.path

    @property
    def key(self) -> tuple[str, str]:
        return (self.path, self.full_ref)

    @property
    def rendered(self) -> str:
        if self.tag:
            return self.full_ref
        return f"{self.full_ref} ({self.fallback_hint})"


@dataclass
class Report:
    pinned: list[Ref] = field(default_factory=list)
    allowed: list[tuple[Ref, str]] = field(default_factory=list)
    violations: list[Ref] = field(default_factory=list)
    malformed: list[Ref] = field(default_factory=list)
    unchecked_charts: list[str] = field(default_factory=list)
    stale: list[tuple[str, str]] = field(default_factory=list)
    allowlist_relpath: str = DEFAULT_ALLOWLIST_RELPATH

    @property
    def ok(self) -> bool:
        return not (self.violations or self.malformed or self.unchecked_charts or self.stale)


@dataclass
class Config:
    allowlist_relpath: str
    sources: list[dict]


# --- configuration ----------------------------------------------------------


def load_config(repo_root: Path, config_path: Path | None = None) -> Config:
    path = config_path or repo_root / CONFIG_RELPATH
    if not path.exists():
        raise SystemExit(
            f"{path}: missing. This checker is shared across repos; each repo declares "
            "what it scans in that file (see the module docstring for the schema)."
        )
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    sources = data.get("sources")
    if not isinstance(sources, list) or not sources:
        raise SystemExit(f"{path}: 'sources' must be a non-empty list")
    for i, source in enumerate(sources):
        kind = (source or {}).get("kind")
        if kind == "helm-overlays":
            if not source.get("charts"):
                raise SystemExit(f"{path}: sources[{i}] (helm-overlays) needs 'charts'")
        elif kind == "tree":
            globs = source.get("globs")
            if not isinstance(globs, list) or not globs:
                raise SystemExit(f"{path}: sources[{i}] (tree) needs a non-empty 'globs' list")
        else:
            raise SystemExit(
                f"{path}: sources[{i}] has unknown kind {kind!r} (expected 'helm-overlays' or 'tree')"
            )
    return Config(
        allowlist_relpath=data.get("allowlist") or DEFAULT_ALLOWLIST_RELPATH,
        sources=sources,
    )


# --- reference grammar ------------------------------------------------------


def deep_merge(base, overlay):
    """Helm's coalesce rule for values: maps merge key-by-key, everything
    else (scalars, lists) is replaced wholesale by the overlay. An explicit
    null in the overlay clears the base value, which normalise_tag then
    treats as an absent tag — the same fallback Helm applies."""
    if isinstance(base, dict) and isinstance(overlay, dict):
        merged = dict(base)
        for key, value in overlay.items():
            merged[key] = deep_merge(base.get(key), value) if key in base else value
        return merged
    return overlay


def bare_repository(image_ref: str) -> str:
    """Strip tag/digest from an image reference, e.g.
    'ghcr.io/foo/bar:v1@sha256:abc' -> 'ghcr.io/foo/bar', without mistaking
    a registry host's port (before the first '/') for a tag separator."""
    ref = image_ref.split("@", 1)[0]
    tail = ref[ref.rfind("/") + 1 :]
    if ":" in tail:
        return ref[: ref.rfind(":")]
    return ref


def has_tag_or_digest(image_ref: str) -> bool:
    """Whether a reference already carries its own tag or digest. Only the
    last path segment is inspected, so a registry host's port
    (registry.local:5000/team/app) is not mistaken for a tag separator."""
    if "@" in image_ref:
        return True
    return ":" in image_ref[image_ref.rfind("/") + 1 :]


def split_raw_reference(image_ref: str) -> tuple[str, str]:
    """Split a raw 'repo[:tag][@digest]' string into (repository, tag) where
    the tag carries any digest suffix."""
    repository = bare_repository(image_ref)
    remainder = image_ref[len(repository) :]
    return repository, remainder.lstrip(":")


def compose_reference(registry: str, repository: str, tag: str, digest: str) -> str:
    """Rebuild the reference a chart template renders from a structured
    {registry, repository, tag, digest} block (the Bitnami/common shape)."""
    ref = repository
    if registry and not repository.startswith(registry + "/"):
        ref = f"{registry}/{repository}"
    if tag:
        ref = f"{ref}:{tag}"
    if digest:
        ref = f"{ref}@{digest}"
    return ref


def normalise_tag(raw) -> tuple[str, str | None]:
    """Return (tag, problem). An unquoted numeric tag is kept as its literal
    text by StrScalarLoader, but the fact that YAML would have parsed it as
    a number is itself the defect: Helm's own parser does exactly that, so
    `tag: 1.10` deploys `1.1`."""
    if raw is None:
        return "", None
    if isinstance(raw, UnquotedScalar) and raw.yaml_type in ("int", "float"):
        return (
            str(raw),
            f"tag is an unquoted YAML number ({raw}), which Helm parses as a number before "
            "it is ever a version string (1.10 becomes 1.1) — quote it",
        )
    if isinstance(raw, UnquotedScalar) and raw.yaml_type == "bool":
        return str(raw), f"tag is an unquoted YAML boolean ({raw}) — quote it"
    if isinstance(raw, str):
        return str(raw), None
    if isinstance(raw, bool):
        return as_str(raw), f"tag is an unquoted YAML boolean ({raw!r}) — quote it"
    if isinstance(raw, (int, float)):
        return str(raw), f"tag is an unquoted YAML number ({raw!r}) — quote it"
    return as_str(raw), f"tag is a {type(raw).__name__}, not a string — quote it"


def reference_problem(repository: str, tag: str, structured: bool) -> str | None:
    """Reject references a chart would render into something no registry
    can resolve. Checked before the digest test, because a digest-only tag
    contains a well-formed digest and would otherwise read as pinned."""
    if not tag:
        return None
    if tag.startswith("@"):
        if structured:
            return (
                "digest-only tag. A chart renders "
                '"{{ .repository }}:{{ .tag }}" unconditionally, so this becomes '
                f"'{repository}:{tag}' — an empty tag followed by a digest, which no registry "
                "can resolve. helm template and helm lint both accept it. "
                "Use '<tag>@sha256:<index digest>'"
            )
        name, digest = "", tag[1:]
    else:
        name, sep, digest = tag.partition("@")
        if not sep:
            digest = None
    if digest is not None and not DIGEST_COMPONENT_RE.match(digest):
        return f"digest component '@{digest}' is not a well-formed '@sha256:<64 lowercase hex>'"
    if name and not TAG_RE.match(name):
        return f"tag '{name}' is not a valid OCI tag ([A-Za-z0-9_][A-Za-z0-9._-]{{0,127}})"
    return None


# --- values walking ---------------------------------------------------------


def is_image_key(key) -> bool:
    """A key holds an image reference if it is `image` or a camelCase /
    snake_case suffix of it (initImage, sidecarImage, dbReadyImage,
    init_image). Deliberately does not match imagePullPolicy,
    imagePullSecrets or imageRegistry, whose values are not references."""
    if not isinstance(key, str):
        return False
    return key == "image" or key.endswith("Image") or key.endswith("_image")


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
    `dbReadyImage` -> `dbReadyTag`. Some charts keep the repository and the
    tag in two sibling scalars that the template joins with a colon, so
    neither key alone is the reference that gets deployed."""
    if image_key == "image":
        return "tag"
    if image_key.endswith("Image"):
        return image_key[: -len("Image")] + "Tag"
    if image_key.endswith("_image"):
        return image_key[: -len("_image")] + "_tag"
    return None


def structured_repository(node: dict) -> tuple[str, str] | None:
    """The (key, value) holding the repository path of a structured image
    block, or None if this mapping is not one.

    Two shapes occur. The common one splits the reference across
    `repository` (path) and an optional `registry` (host). The other, used
    by the democratic-csi chart among others, has no `repository` at all
    and puts the *whole* path in `registry`, which the template renders as
    `{{ .registry }}:{{ .tag }}`. Keying only on `repository` makes that
    second shape invisible — the exact hole through which an unpinned
    `latest` once reached a cluster.

    A `registry` holding a bare host (`docker.io`, `registry.k8s.io`) is
    not a reference on its own, so the fallback requires a path separator.
    That also keeps a normal {registry, repository} pair from being counted
    twice, since those only reach the fallback when `repository` is absent.
    """
    repository = node.get("repository")
    if looks_like_image_reference(repository):
        return "repository", as_str(repository)
    if repository is None:
        registry = node.get("registry")
        if looks_like_image_reference(registry) and "/" in as_str(registry):
            return "registry", as_str(registry)
    return None


def walk(node, refs: list, trail: str = "") -> None:
    """Collect every image reference in a values tree, at any depth. Each
    entry is a dict with `where` (dotted trail), `key` and `needle` (for
    locating the source line), `repository`, `raw_tag` (the tag scalar as
    loaded, or None), `digest`, `full_ref` and `structured`."""
    if isinstance(node, dict):
        structured = structured_repository(node)
        if structured is not None:
            # A structured image block is a reference whether or not it
            # carries a `tag` key: a missing tag means the chart template
            # falls back to appVersion or to an implicit `latest`, which is
            # the most mutable case of all, not an exempt one.
            repo_key, repository = structured
            registry = "" if repo_key == "registry" else as_str(node.get("registry")).strip()
            if registry and not repository.startswith(registry + "/"):
                repository = f"{registry}/{repository}"
            refs.append(
                {
                    "where": trail or "image",
                    "key": repo_key,
                    "needle": structured[1],
                    "repository": repository,
                    "raw_tag": node.get("tag"),
                    "digest": as_str(node.get("digest")).strip(),
                    "structured": True,
                }
            )
        for key, value in node.items():
            child_trail = f"{trail}.{key}" if trail else str(key)
            if is_image_key(key) and looks_like_image_reference(value):
                image = as_str(value).strip()
                tag_key = sibling_tag_key(key)
                sibling = node.get(tag_key) if tag_key else None
                repository, tag = split_raw_reference(image)
                raw_tag: object = tag
                # A tag that arrived as a separate scalar the chart template
                # concatenates behaves like the structured {repository, tag}
                # map for reference_problem's purposes: a digest-only value
                # ("@sha256:...") renders into "repo:@sha256:..." either way,
                # which is exactly as unpullable as the map form. Only a tag
                # already embedded in one raw string (split_raw_reference's
                # own "repo@sha256:..." parse) is exempt, since that shape is
                # a genuinely valid digest reference with no tag at all.
                composed_tag = False
                if not has_tag_or_digest(image) and as_str(sibling).strip():
                    raw_tag = sibling
                    composed_tag = True
                refs.append(
                    {
                        "where": child_trail,
                        "key": key,
                        "needle": image,
                        "repository": repository,
                        "raw_tag": raw_tag,
                        "digest": "",
                        "structured": composed_tag,
                    }
                )
            walk(value, refs, child_trail)
    elif isinstance(node, list):
        for i, item in enumerate(node):
            walk(item, refs, f"{trail}[{i}]")
    elif isinstance(node, str) and "image:" in node and EMBEDDED_MANIFEST_HINT.search(node):
        try:
            for embedded_doc in load_all_yaml(node):
                if embedded_doc is not None:
                    walk(embedded_doc, refs, trail)
        except yaml.YAMLError:
            pass  # not actually parseable YAML — leave it to a human/other linter


def find_line(text: str, key: str, needle: str) -> int | None:
    """First 1-based line number declaring `key:` and mentioning `needle`,
    falling back to any line mentioning `needle`. Best-effort: a reference
    inherited from values.yaml has no line in the overlay."""
    prefix = f"{key}:"
    lines = text.splitlines()
    for i, line in enumerate(lines, start=1):
        stripped = line.strip()
        if stripped.startswith("- "):
            stripped = stripped[2:].strip()
        if stripped.startswith(prefix) and needle in stripped:
            return i
    for i, line in enumerate(lines, start=1):
        if needle in line:
            return i
    return None


def build_ref(found: dict, relpath: str, text: str, fallback_hint: str) -> Ref:
    tag, problem = normalise_tag(found["raw_tag"])
    tag = tag.strip()
    problem = problem or reference_problem(found["repository"], tag, found["structured"])
    digest = found["digest"]
    if digest and not DIGEST_COMPONENT_RE.match(digest):
        problem = problem or f"digest '{digest}' is not a well-formed 'sha256:<64 lowercase hex>'"
    if tag.startswith("@") and not found["structured"]:
        full_ref = f"{found['repository']}{tag}"  # raw `repo@sha256:…`, no tag at all
    else:
        full_ref = compose_reference("", found["repository"], tag, digest)
    if digest:
        tag = f"{tag}@{digest}" if tag else f"@{digest}"
    line = find_line(text, found["key"], found["needle"])
    if line is None and tag:
        line = find_line(text, "tag", tag.split("@", 1)[0]) if found["structured"] else None
    return Ref(
        path=relpath,
        line=line,
        where=found["where"],
        repository=found["repository"],
        tag=tag,
        full_ref=full_ref,
        fallback_hint=fallback_hint,
        problem=problem,
    )


# --- sources ----------------------------------------------------------------


def refs_in_tree(tree, relpath: str = "", text: str = "", fallback_hint: str = "") -> list[Ref]:
    """Every image reference in an already-loaded values tree, normalised.
    The entry point for other tools that assemble a values tree themselves
    (a remote chart's defaults merged with an overlay, say) and need the
    same answer to "what is a reference, and is it pinned" as this gate."""
    found: list[dict] = []
    walk(tree, found)
    return [build_ref(f, relpath, text, fallback_hint) for f in found]


def refs_from_values_text(text: str, relpath: str, fallback_hint: str, merged=None) -> list[Ref]:
    if merged is not None:
        return refs_in_tree(merged, relpath, text, fallback_hint)
    refs: list[Ref] = []
    for doc in load_all_yaml(text):
        if doc is not None:
            refs.extend(refs_in_tree(doc, relpath, text, fallback_hint))
    return refs


def refs_from_template_text(text: str, relpath: str) -> list[Ref]:
    refs = []
    for i, line in enumerate(text.splitlines(), start=1):
        match = TEMPLATE_IMAGE_RE.match(line)
        if not match:
            continue
        key = match.group("key")
        value = match.group("value").strip().strip("\"'").strip()
        # A templated value resolves from values.yaml, which the values scan
        # already covers with the real per-environment overrides.
        if not is_image_key(key) or not looks_like_image_reference(value):
            continue
        repository, tag = split_raw_reference(value)
        refs.append(
            Ref(
                path=relpath,
                line=i,
                where=key,
                repository=repository,
                tag=tag,
                full_ref=value,
                fallback_hint="no tag — implicit :latest",
                problem=reference_problem(repository, tag, structured=False),
            )
        )
    return refs


def is_chart_template(path: Path) -> bool:
    return "templates" in path.parts


def extract_refs(path: Path, repo_root: Path) -> list[Ref]:
    relpath = path.relative_to(repo_root).as_posix()
    text = path.read_text(encoding="utf-8")
    if is_chart_template(path):
        return refs_from_template_text(text, relpath)
    return refs_from_values_text(text, relpath, "no tag — chart appVersion or implicit :latest")


def discover_app_charts(charts_dir: Path) -> list[Path]:
    if not charts_dir.is_dir():
        return []
    charts = []
    for chart_dir in sorted(p for p in charts_dir.iterdir() if p.is_dir()):
        if load_yaml(chart_dir / "Chart.yaml").get("type") == "library":
            continue
        charts.append(chart_dir)
    return charts


def refs_from_helm_overlays(source: dict, repo_root: Path, report: Report) -> list[Ref]:
    refs: list[Ref] = []
    charts_dir = repo_root / source["charts"]
    for chart_dir in discover_app_charts(charts_dir):
        overlays = sorted(chart_dir.glob("values-*.yaml"))
        if not overlays:
            report.unchecked_charts.append(chart_dir.relative_to(repo_root).as_posix())
            continue
        base = load_yaml(chart_dir / "values.yaml")
        for overlay in overlays:
            text = overlay.read_text(encoding="utf-8")
            merged = deep_merge(base, load_yaml_text(text) or {})
            refs.extend(
                refs_from_values_text(
                    text,
                    overlay.relative_to(repo_root).as_posix(),
                    "no tag — falls back to Chart.appVersion",
                    merged=merged,
                )
            )
    return refs


def refs_from_tree(source: dict, repo_root: Path) -> list[Ref]:
    files = set()
    for pattern in source["globs"]:
        for path in repo_root.glob(pattern):
            if not path.is_file():
                continue
            if is_chart_template(path) and path.suffix not in TEMPLATE_SUFFIXES:
                continue
            if not is_chart_template(path) and path.suffix not in (".yaml", ".yml"):
                continue
            files.add(path)
    refs: list[Ref] = []
    for path in sorted(files):
        refs.extend(extract_refs(path, repo_root))
    return refs


# --- allowlist --------------------------------------------------------------


def load_allowlist(allowlist_file: Path) -> dict[tuple[str, str], dict]:
    if not allowlist_file.exists():
        return {}
    data = load_yaml_text(allowlist_file.read_text(encoding="utf-8")) or {}
    by_key = {}
    for i, entry in enumerate(data.get("exceptions", []) or []):
        path = entry.get("path")
        ref = entry.get("ref")
        reason = entry.get("reason")
        if not path or not ref:
            raise SystemExit(
                f"{allowlist_file}: exceptions[{i}] missing required 'path' and/or 'ref'. "
                "'ref' is the full reference including its tag (e.g. 'redis:7.4-alpine', or "
                "the bare repository when no tag is set), so that bumping the tag re-opens "
                "the question instead of inheriting the exemption."
            )
        if isinstance(ref, UnquotedScalar):
            raise SystemExit(
                f"{allowlist_file}: exceptions[{i}] (path={path}) has an unquoted non-string "
                f"'ref' ({ref}) — quote it so it keys against the reference verbatim"
            )
        if not isinstance(ref, str):
            raise SystemExit(f"{allowlist_file}: exceptions[{i}] (path={path}) 'ref' must be a string")
        if not reason or not as_str(reason).strip():
            raise SystemExit(
                f"{allowlist_file}: exceptions[{i}] (path={path}, ref={ref}) "
                "missing a non-empty 'reason' — every exception must document inline why it "
                "is deliberate"
            )
        key = (str(path), str(ref))
        if key in by_key:
            raise SystemExit(f"{allowlist_file}: duplicate exception entry for {key}")
        by_key[key] = entry
    return by_key


# --- driver -----------------------------------------------------------------


def check(repo_root: Path, config: Config | None = None) -> Report:
    config = config or load_config(repo_root)
    allowlist = load_allowlist(repo_root / config.allowlist_relpath)
    report = Report(allowlist_relpath=config.allowlist_relpath)
    consumed = set()
    seen = set()

    refs: list[Ref] = []
    for source in config.sources:
        if source["kind"] == "helm-overlays":
            refs.extend(refs_from_helm_overlays(source, repo_root, report))
        else:
            refs.extend(refs_from_tree(source, repo_root))

    for ref in refs:
        if ref.key in seen:
            continue  # same reference repeated in one file — one finding
        seen.add(ref.key)
        if ref.problem:
            report.malformed.append(ref)
            continue
        if DIGEST_RE.search(ref.full_ref):
            report.pinned.append(ref)
            continue
        entry = allowlist.get(ref.key)
        if entry is not None:
            consumed.add(ref.key)
            report.allowed.append((ref, as_str(entry["reason"])))
        else:
            report.violations.append(ref)

    report.stale = sorted(set(allowlist) - consumed)
    return report


def render(report: Report) -> str:
    out = [
        f"image-pin inventory: {len(report.pinned)} digest-pinned, {len(report.allowed)} allowlisted, "
        f"{len(report.violations)} unexplained, {len(report.malformed)} malformed\n"
    ]

    if report.pinned:
        out.append("Digest-pinned:")
        for ref in report.pinned:
            out.append(f"  [{ref.location}] {ref.where}: {ref.rendered}")
        out.append("")

    if report.allowed:
        out.append(f"Allowlisted (see {report.allowlist_relpath}):")
        for ref, reason in report.allowed:
            out.append(f"  [{ref.location}] {ref.where}: {ref.rendered}\n    reason: {reason}")
        out.append("")

    if report.malformed:
        out.append("MALFORMED — reference cannot resolve; not allowlistable:")
        for ref in report.malformed:
            out.append(f"  [{ref.location}] {ref.where}: {ref.rendered}\n    {ref.problem}")
        out.append("")

    if report.violations:
        out.append("VIOLATIONS — mutable image reference with no digest pin and no allowlist entry:")
        for ref in report.violations:
            out.append(f"  [{ref.location}] {ref.where}: {ref.rendered}")
        out.append(
            "\nFix by pinning to '<tag>@sha256:<index digest>' at the location above, or add a "
            f"documented exception (path + ref + reason) to {report.allowlist_relpath}.\n"
        )

    if report.unchecked_charts:
        out.append("UNCHECKED CHARTS — no values-<env>.yaml overlay, so nothing was verified:")
        for chart in report.unchecked_charts:
            out.append(f"  {chart}")
        out.append("")

    if report.stale:
        out.append("STALE ALLOWLIST ENTRIES — no longer match an unpinned reference (remove them):")
        for path, ref in report.stale:
            out.append(f"  {path} ({ref})")
        out.append("")

    if report.ok:
        out.append("0 issues — every image reference is digest-pinned or documented.")

    return "\n".join(out)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=Path(__file__).resolve().parent.parent,
        help="repository root (default: the parent of this script's directory)",
    )
    parser.add_argument(
        "--config",
        type=Path,
        default=None,
        help=f"per-repo config file (default: <repo-root>/{CONFIG_RELPATH})",
    )
    args = parser.parse_args(argv)
    repo_root = args.repo_root.resolve()
    report = check(repo_root, load_config(repo_root, args.config))
    print(render(report))
    return 0 if report.ok else 1


if __name__ == "__main__":
    sys.exit(main())
