#!/usr/bin/env python3
"""Reject mutable image tags that a *remote* Helm chart supplies by default
and no tenant overlay in this repo overrides.

tools/check-image-pins.py reads only files that exist in this repo. That is
the whole of the deployed configuration for raw manifests and for charts
under helm-charts/, but for a service whose `repo` in tenant.yaml points at
an upstream chart museum, most of the deployed configuration lives in the
chart's own values.yaml — a file no amount of scanning this repo will find.
A reference that never appears here is not absent, it is *invisible*, and an
invisible reference reads as "no finding" rather than as "not checked".

That is not hypothetical. Chart democratic-csi 0.15.1 defaults both
controller.driver.image.tag and node.driver.image.tag to `latest`; neither of
the two releases overrode it, so the CSI driver moving this cluster's storage
was whatever `latest` happened to resolve to at the last image pull, with no
commit here that could ever record the change (JDWLABS-369).

What this checks
----------------
For every service in tenants/*/tenant.yaml that renders a remote chart, the
chart's default values are fetched at the exact `revision` tenant.yaml pins,
the service's own values.yaml is merged over them the way Helm coalesces
them, and the *merged* result — the configuration that actually reaches the
cluster — is walked for image references.

Only references whose tag is mutable are reported. A remote chart's default
of `csi-attacher:v4.4.0` is already immutable in practice and reproducible
from the chart revision, which tenant.yaml pins; demanding a digest for every
such tag across two dozen upstream charts would produce hundreds of findings
that can only be rubber-stamped, and a check whose output is rubber-stamped
detects nothing. A tag of `latest`/`main`/`stable`, an absent tag, or a
truncated version like `v1` or `1.2` is a different thing entirely: it can
move under a pod restart with no commit anywhere, which is the bug class this
exists to catch.

This check is deliberately a sibling rather than an extension: it needs the
network and upstream chart repositories, so its failure modes (an upstream
museum being down) are not the offline checker's, and keeping them in one
process would mean an upstream outage could no longer be told apart from an
unpinned image. tools/sync-monitoring-crds.py already establishes the
precedent for a network-fetching gate in this repo.

Usage:
    python3 tools/check-remote-chart-image-pins.py

Exit codes: 0 = every remote-chart default is immutable or explained;
1 = a mutable reference with no allowlist entry, a stale allowlist entry, or
a chart that could not be fetched (a fetch failure is reported as an error,
never skipped — silently trusting what could not be read is the exact
failure this check exists to prevent).
"""
import gzip
import io
import json
import re
import sys
import tarfile
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from importlib.util import module_from_spec, spec_from_file_location

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
ALLOWLIST_FILE = REPO_ROOT / "tools/remote-chart-image-pin-allowlist.yaml"

# Reuse the sibling checker's reference extraction, YAML loading and allowlist
# schema rather than reimplementing them: the two checks must agree on what
# counts as an image reference and on what a pin looks like, and a second copy
# of that logic would drift. Loaded by path because the filename is not a legal
# module name, and deliberately not registered in sys.modules — nothing imports
# it by name, and claiming the name would fight the test module that loads its
# own copy under it.
_spec = spec_from_file_location("check_image_pins", REPO_ROOT / "tools/check-image-pins.py")
_pins = module_from_spec(_spec)
_spec.loader.exec_module(_pins)

HTTP_TIMEOUT = 60
USER_AGENT = "jdwlabs-platform-image-pin-check"

# Helm's OCI layer for the chart archive itself.
HELM_CHART_LAYER = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
OCI_ACCEPT = ",".join(
    (
        "application/vnd.oci.image.manifest.v1+json",
        "application/vnd.docker.distribution.manifest.v2+json",
        "application/vnd.oci.image.index.v1+json",
        "application/vnd.docker.distribution.manifest.list.v2+json",
    )
)

# Tags that name a moving target rather than a release. Matched against the
# first `-` separated component, so `stable-alpine` and `latest-debian` are
# caught alongside the bare forms.
MUTABLE_TAG_NAMES = frozenset(
    {
        "latest", "main", "master", "stable", "edge", "head", "tip",
        "dev", "devel", "development", "nightly", "canary", "snapshot",
        "rolling", "current", "unstable", "release", "prod", "production",
        "test", "testing", "staging", "beta", "alpha", "rc", "next",
    }
)

# `1`, `v1`, `1.2`, `v4.4` — a floating prefix that upstream re-points at each
# patch release. A full `1.2.3` is treated as immutable: re-tagging one is a
# supply-chain incident, not a routine event this check should assume.
FLOATING_VERSION_RE = re.compile(r"^v?\d+(\.\d+)?$")


def tag_of(full_ref: str) -> str:
    """The tag portion of a reference, ignoring any digest and any port in a
    registry host. Returns "" when the reference carries no tag at all.
    """
    ref = full_ref.split("@", 1)[0]
    last_segment = ref[ref.rfind("/") + 1 :]
    if ":" not in last_segment:
        return ""
    return last_segment.rsplit(":", 1)[1]


def is_mutable_tag(tag: str) -> bool:
    """Whether a tag can point somewhere new tomorrow without a commit.

    An empty tag is mutable here. Callers resolve the chart's appVersion
    first (see `effective_tag`) precisely so that the near-universal
    `tag: ""` -> `| default .Chart.AppVersion` convention is judged on the
    version it actually resolves to, and only a chart that pins neither is
    reported.
    """
    tag = tag.strip()
    if not tag:
        return True
    if FLOATING_VERSION_RE.match(tag):
        return True
    return tag.split("-", 1)[0].lower() in MUTABLE_TAG_NAMES


def effective_tag(tag: str, app_version: str) -> str:
    """The tag a chart actually renders for an image whose `tag` value is
    empty.

    Leaving `image.tag` unset is how most upstream charts say "track this
    chart's own appVersion", which the `revision` pinned in tenant.yaml fixes
    just as firmly as an explicit version would — chart argo-cd 10.3.3 always
    means one argocd build. Reporting those as unpinned would bury the four
    genuine `:latest` defaults under sixteen findings whose only possible
    resolution is an allowlist entry saying "this is fine", and a check whose
    output is rubber-stamped has stopped being a check. tools/
    image-pin-allowlist.yaml already records this same reasoning for the
    arc-systems controller image.

    A chart that declares no appVersion gets no such benefit: the reference
    then renders tagless, which a registry resolves as `latest`.
    """
    return app_version.strip() if not tag.strip() else tag


def deep_merge(base, overlay):
    """Merge `overlay` onto `base` the way Helm coalesces a -f values file.

    Maps merge key by key; every other type (scalars and, importantly,
    lists) replaces wholesale, because that is what Helm does — a chart
    default list is never appended to. An explicit `null` in the overlay
    deletes the key, matching Helm's documented behaviour for nulling out a
    default.
    """
    if not isinstance(base, dict) or not isinstance(overlay, dict):
        return overlay
    merged = dict(base)
    for key, value in overlay.items():
        if value is None:
            merged.pop(key, None)
        elif key in merged:
            merged[key] = deep_merge(merged[key], value)
        else:
            merged[key] = value
    return merged


def _get(url: str, headers: dict | None = None) -> bytes:
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT, **(headers or {})})
    with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT) as response:
        return response.read()


def _read_top_level(tar: tarfile.TarFile, chart: str, basename: str) -> str | None:
    """Text of `<chart>/<basename>` from an opened chart archive.

    Only depth-2 members are considered: a subchart's own values.yaml is
    already coalesced into the parent's under its subchart key, so reading it
    separately would double-report. The archive's top directory is usually
    the chart name but is not guaranteed to be, hence the fallback.
    """
    member = None
    for candidate in tar.getmembers():
        parts = Path(candidate.name).parts
        if len(parts) == 2 and parts[1] == basename:
            if parts[0] == chart:
                member = candidate
                break
            if member is None:
                member = candidate
    if member is None:
        return None
    extracted = tar.extractfile(member)
    return extracted.read().decode("utf-8") if extracted is not None else None


def values_from_chart_archive(archive: bytes, chart: str) -> dict:
    """The chart's default values plus its declared appVersion."""
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as tar:
        values_text = _read_top_level(tar, chart, "values.yaml")
        chart_text = _read_top_level(tar, chart, "Chart.yaml")
    if values_text is None:
        raise ValueError(f"no top-level values.yaml inside the {chart} chart archive")
    metadata = yaml.safe_load(chart_text) if chart_text else {}
    return {
        "values": yaml.load(values_text, Loader=_pins.StrScalarLoader) or {},
        "appVersion": _pins.as_str((metadata or {}).get("appVersion")).strip(),
    }


def fetch_http_chart_values(repo: str, chart: str, revision: str) -> dict:
    """Resolve a classic (index.yaml) Helm repository and pull the chart."""
    base = repo if repo.endswith("/") else repo + "/"
    index = yaml.load(
        _get(urllib.parse.urljoin(base, "index.yaml")).decode("utf-8"),
        Loader=getattr(yaml, "CSafeLoader", yaml.SafeLoader),
    )
    entries = (index or {}).get("entries", {}).get(chart)
    if not entries:
        raise ValueError(f"chart '{chart}' is not published in {repo}")
    for entry in entries:
        if str(entry.get("version")) == revision:
            urls = entry.get("urls") or []
            if not urls:
                raise ValueError(f"{chart} {revision} has no download URL in {repo}")
            return values_from_chart_archive(_get(urllib.parse.urljoin(base, urls[0])), chart)
    raise ValueError(f"{chart} {revision} is not published in {repo} (revision pinned but absent)")


def _oci_token(registry: str, repository: str) -> dict:
    """Anonymous pull token. Registries that need no auth simply 401 here and
    the caller proceeds without a header.
    """
    try:
        body = _get(
            f"https://{registry}/token?scope=repository:{repository}:pull&service={registry}"
        )
        token = json.loads(body).get("token") or json.loads(body).get("access_token")
        return {"Authorization": f"Bearer {token}"} if token else {}
    except (urllib.error.URLError, ValueError):
        return {}


def fetch_oci_chart_values(repo: str, chart: str, revision: str) -> dict:
    """Pull a chart from an OCI registry (`ghcr.io/org/charts` style)."""
    registry, _, path = repo.removeprefix("oci://").partition("/")
    repository = f"{path}/{chart}" if path else chart
    auth = _oci_token(registry, repository)
    manifest = json.loads(
        _get(
            f"https://{registry}/v2/{repository}/manifests/{revision}",
            {"Accept": OCI_ACCEPT, **auth},
        )
    )
    layers = manifest.get("layers") or []
    chart_layers = [layer for layer in layers if layer.get("mediaType") == HELM_CHART_LAYER]
    if not chart_layers:
        # Some registries publish the chart as the sole layer without the
        # canonical Helm mediaType; fall back to it rather than failing.
        chart_layers = layers[:1]
    if not chart_layers:
        raise ValueError(f"{repo}/{chart}:{revision} has no chart layer")
    digest = chart_layers[0]["digest"]
    blob = _get(f"https://{registry}/v2/{repository}/blobs/{digest}", auth)
    if not blob.startswith(b"\x1f\x8b"):
        blob = gzip.compress(blob)
    return values_from_chart_archive(blob, chart)


def fetch_chart_values(repo: str, chart: str, revision: str) -> dict:
    if repo.startswith(("http://", "https://")):
        return fetch_http_chart_values(repo, chart, revision)
    return fetch_oci_chart_values(repo, chart, revision)


def discover_releases() -> list[dict]:
    """Every tenant service rendered from a remote chart, in tenant.yaml order.

    A service with `chartPath` renders from helm-charts/ inside this repo and
    a service with `rawManifests` renders no chart at all; both are already
    fully covered by tools/check-image-pins.py.
    """
    releases = []
    for tenant_file in sorted(REPO_ROOT.glob("tenants/*/tenant.yaml")):
        tenant = tenant_file.parent.name
        document = yaml.safe_load(tenant_file.read_text(encoding="utf-8")) or {}
        for service in document.get("services") or []:
            repo = (service.get("repo") or "").strip()
            chart = (service.get("chart") or "").strip()
            name = service.get("name")
            if not repo or not chart or service.get("chartPath") or service.get("rawManifests"):
                continue
            releases.append(
                {
                    "tenant": tenant,
                    "name": name,
                    "chart": chart,
                    "repo": repo,
                    "revision": str(service.get("revision") or "").strip(),
                    "overlay": f"tenants/{tenant}/services/{name}/values.yaml",
                }
            )
    return releases


def overlay_values(relpath: str) -> dict:
    path = REPO_ROOT / relpath
    if not path.exists():
        return {}
    return yaml.load(path.read_text(encoding="utf-8"), Loader=_pins.StrScalarLoader) or {}


def mutable_refs(values: dict, app_version: str = "") -> list[str]:
    """Every image reference in a merged values tree whose tag can still move.

    Returned references are the *resolved* ones, so a finding shows the tag
    that would actually be deployed rather than the empty value written in
    the chart.
    """
    refs: list[dict] = []
    _pins.walk(values, refs)
    found = []
    for ref in refs:
        full_ref = ref["full_ref"]
        if _pins.DIGEST_RE.search(full_ref) is not None:
            continue
        tag = tag_of(full_ref)
        resolved = effective_tag(tag, app_version)
        if not is_mutable_tag(resolved):
            continue
        if not tag and resolved:
            full_ref = f"{full_ref.rstrip(':')}:{resolved}"
        found.append(full_ref)
    return sorted(set(found))


def collect(releases, allowlist, fetch=None):
    """Classify every remote chart. Returns (checked, allowed, violations,
    errors, consumed_allowlist_keys).

    `fetch` is resolved at call time rather than bound as a default so that
    tests can substitute chart defaults without reaching the network.
    """
    fetch = fetch or fetch_chart_values
    allowed, violations, errors = [], [], []
    consumed = set()
    checked = 0
    cache: dict[tuple[str, str, str], dict] = {}

    for release in releases:
        key = (release["repo"], release["chart"], release["revision"])
        try:
            if key not in cache:
                cache[key] = fetch(*key)
            defaults = cache[key]
        except Exception as exc:  # noqa: BLE001 — reported, never swallowed
            errors.append((f"{release['tenant']}/{release['name']}", f"{type(exc).__name__}: {exc}"))
            continue

        checked += 1
        merged = deep_merge(defaults["values"], overlay_values(release["overlay"]))
        for full_ref in mutable_refs(merged, defaults["appVersion"]):
            entry = allowlist.get((release["overlay"], full_ref))
            if entry is not None:
                consumed.add((release["overlay"], full_ref))
                allowed.append((release["overlay"], full_ref, entry["reason"]))
            else:
                violations.append((release, full_ref))

    return checked, allowed, violations, errors, consumed


def main() -> int:
    _pins.ALLOWLIST_FILE = ALLOWLIST_FILE
    allowlist = _pins.load_allowlist()
    releases = discover_releases()
    checked, allowed, violations, errors, consumed = collect(releases, allowlist)
    stale = sorted(set(allowlist) - consumed)

    print(
        f"remote-chart image-pin inventory: {checked}/{len(releases)} charts checked, "
        f"{len(allowed)} allowlisted, {len(violations)} unexplained, {len(errors)} unreadable\n"
    )

    if allowed:
        print(f"Allowlisted (see {ALLOWLIST_FILE.relative_to(REPO_ROOT).as_posix()}):")
        for overlay, full_ref, reason in allowed:
            print(f"  [{overlay}] {full_ref}\n    reason: {reason}")
        print()

    exit_code = 0

    if violations:
        exit_code = 1
        print("VIOLATIONS — remote chart default resolves to a mutable tag and nothing pins it:")
        for release, full_ref in violations:
            print(
                f"  [{release['chart']} {release['revision']} -> {release['tenant']}/"
                f"{release['name']}] {full_ref}"
            )
            print(f"    pin it in {release['overlay']}")
        print(
            "\nFix by overriding the tag in that overlay as '<tag>@sha256:<index digest>', "
            f"or add a documented exception to {ALLOWLIST_FILE.relative_to(REPO_ROOT).as_posix()}."
        )

    if errors:
        exit_code = 1
        print("\nUNREADABLE CHARTS — could not fetch defaults, so nothing was verified:")
        for release, message in errors:
            print(f"  {release}: {message}")

    if stale:
        exit_code = 1
        print("\nSTALE ALLOWLIST ENTRIES — no longer match a mutable reference (remove them):")
        for overlay, full_ref in stale:
            print(f"  {overlay} ({full_ref})")

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
