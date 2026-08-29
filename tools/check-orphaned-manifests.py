#!/usr/bin/env python3
"""Reject YAML that no ArgoCD Application or ApplicationSet reconciles.

A manifest can be syntactically perfect, pass kubeconform, and still change
nothing, because ArgoCD only reads the paths its Applications name. Nine
auto-remediation PRs in a row wrote plausible-looking manifests under
invented top-level directories (manifests/, cluster/, clusters/, apps/) and
every one was silently inert. Nothing in Validate caught it, because every
existing gate checks a file's content, not whether anything reads it.

The watched set is derived from the wiring itself rather than restated:

- bootstrap/**              root-app.yaml, recurse: true, except crds/**
- bootstrap/crds/*.yaml     platform-crds (00-crds.yaml): path bootstrap/crds,
                            no directory: block, so ArgoCD's default
                            recurse: false applies -- one level deep only
- helm-charts/**            chart sources rendered by the tenant envelope or a
                            service's chartPath
- tenants/<t>/tenant.yaml   governance ApplicationSet git-files generator
- tenants/<t>/services/<s>/ per services[] entry of that tenant.yaml, with the
                            same branches as tenant-envelope/services-appset:
                              rawManifests  -> postInstall/*.yaml only
                              otherwise     -> values.yaml, and postInstall/*.yaml
                                               only when postInstall is true

Everything else that ends in .yaml/.yml must live under a directory that is
known not to hold manifests (tools, tests, docs, cli, ...) or as a dotfile
at the root. Any other YAML is orphaned and fails the check, including a
services/<s>/ directory whose release is not listed in its tenant.yaml.

Known orphans that are kept on purpose (a dormant release whose manifests
are parked for re-enable) are listed with a reason in
tools/orphaned-manifest-allowlist.yaml; an entry there is a debt marker,
not a fix.

A second, independent check runs alongside the orphan scan: the ai-sre-relay
deployment carries its own GITHUB_PATH_ALLOWLIST, a set of path.Match globs
that are necessarily coarser than "actually reconciled" (a wildcard glob
like tenants/*/services/*/values.yaml also matches a dormant release's
values.yaml). find_relay_allowlist_overlap() fails the build if any glob in
that env var can reach a path this file's own allowlist has recorded as
unreconciled -- the exact JDWLABS-460 symptom one layer down: a proposal
that passes every gate and changes nothing. See GITHUB_PATH_DENYLIST in
tenants/platform/services/ai-sre-relay/postInstall/ai-sre-relay.yaml, which
is where a real conflict gets resolved.

Usage:
    python3 tools/check-orphaned-manifests.py

Exit codes: 0 = every YAML is either watched or known non-manifest, and the
relay's path allowlist cannot reach a known orphan;
1 = at least one orphan was found, or the relay allowlist overlap check
failed.
"""
import fnmatch
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
ALLOWLIST = Path(__file__).resolve().parent / "orphaned-manifest-allowlist.yaml"
RELAY_MANIFEST = Path("tenants/platform/services/ai-sre-relay/postInstall/ai-sre-relay.yaml")

# Reconciled wholesale: every file under these is read by an Application.
# bootstrap/ is deliberately absent -- its crds/ subtree is read one level
# deep, not recursively, so it needs bootstrap_verdict() below rather than a
# blanket pass.
WATCHED_TREES = ("helm-charts",)

# Hold YAML that is tooling, tests, or documentation, never a manifest.
NON_MANIFEST_TREES = ("bin", "cli", "docs", "observability", "scripts", "tests", "tools")

YAML_SUFFIXES = (".yaml", ".yml")


def load_tenant(path: Path) -> dict:
    with path.open(encoding="utf-8") as fh:
        doc = yaml.safe_load(fh) or {}
    return doc if isinstance(doc, dict) else {}


def service_rules(tenant: dict) -> dict:
    """Map release name -> (reads values.yaml, reads postInstall/) exactly as
    the services ApplicationSet template would source them."""
    rules = {}
    for svc in tenant.get("services") or []:
        if not isinstance(svc, dict) or not svc.get("name"):
            continue
        if svc.get("rawManifests"):
            rules[str(svc["name"])] = (False, True)
        else:
            rules[str(svc["name"])] = (True, bool(svc.get("postInstall")))
    return rules


def bootstrap_verdict(rel: Path) -> str | None:
    """Return None when rel (relative, under bootstrap/) is watched, else the
    reason it is not.

    root-app.yaml recurses bootstrap/ but excludes crds/**: that subtree is
    owned solely by the platform-crds Application (00-crds.yaml), whose
    source has no `directory:` block and so reads bootstrap/crds at ArgoCD's
    default recurse: false -- direct children only, not nested directories.
    """
    parts = rel.parts
    if len(parts) >= 2 and parts[1] == "crds":
        if len(parts) == 3:
            return None
        return "bootstrap/crds is read one level deep (recurse: false); nested directories are not"
    return None


def tenant_verdict(rel: Path, rules_by_tenant: dict) -> str | None:
    """Return None when rel (relative, under tenants/) is watched, else the
    reason it is not."""
    parts = rel.parts
    if len(parts) == 3 and parts[2] == "tenant.yaml":
        return None
    if len(parts) < 4 or parts[2] != "services":
        return "not tenant.yaml and not under services/<release>/"
    tenant, release = parts[1], parts[3]
    rules = rules_by_tenant.get(tenant)
    if rules is None:
        return f"tenants/{tenant}/tenant.yaml does not exist"
    if release not in rules:
        return f"release {release!r} is not listed in tenants/{tenant}/tenant.yaml services"
    reads_values, reads_post = rules[release]
    tail = parts[4:]
    if tail == ("values.yaml",):
        return None if reads_values else f"release {release!r} is rawManifests; values.yaml is never read"
    if len(tail) == 2 and tail[0] == "postInstall":
        return None if reads_post else f"release {release!r} has no postInstall: true; postInstall/ is never read"
    if tail and tail[0] == "postInstall":
        return "postInstall/ is read flat; nested directories are not"
    return f"only values.yaml and postInstall/*.yaml are read under services/{release}/"


def iter_yaml(root: Path):
    for path in sorted(root.rglob("*")):
        if path.suffix not in YAML_SUFFIXES or not path.is_file():
            continue
        rel = path.relative_to(root)
        # Hidden trees (.git, .github, .worktrees, .claude) and vendored
        # modules are never manifests; a root dotfile is tool config.
        if any(p.startswith(".") or p == "node_modules" for p in rel.parts[:-1]):
            continue
        if len(rel.parts) == 1 and rel.name.startswith("."):
            continue
        yield rel


def load_allowlist(path: Path) -> list[str]:
    """Path prefixes (posix, relative to the repo root) that may stay orphaned.
    Every entry must carry a reason; a bare path is refused so the list cannot
    quietly become a second, unexplained watched set."""
    if not path.is_file():
        return []
    with path.open(encoding="utf-8") as fh:
        doc = yaml.safe_load(fh) or {}
    prefixes = []
    for entry in doc.get("allow") or []:
        if not isinstance(entry, dict) or not entry.get("path") or not entry.get("reason"):
            raise SystemExit(f"{path}: every allow entry needs both 'path' and 'reason': {entry!r}")
        prefixes.append(str(entry["path"]).rstrip("/") + "/")
    return prefixes


def allowed(rel_posix: str, prefixes: list[str]) -> bool:
    return any(rel_posix == p.rstrip("/") or rel_posix.startswith(p) for p in prefixes)


def parse_relay_path_globs(root: Path, env_name: str) -> list[str]:
    """Read one comma-separated path.Match glob list straight out of the
    relay's own deployment manifest (GITHUB_PATH_ALLOWLIST or
    GITHUB_PATH_DENYLIST), so this check reads the same source of truth the
    relay reads at runtime instead of a copy that can silently drift."""
    manifest = root / RELAY_MANIFEST
    if not manifest.is_file():
        return []
    globs: list[str] = []
    for doc in yaml.safe_load_all(manifest.read_text(encoding="utf-8")):
        if not isinstance(doc, dict):
            continue
        containers = ((doc.get("spec") or {}).get("template") or {}).get("spec", {}).get("containers") or []
        for container in containers:
            for entry in container.get("env") or []:
                if isinstance(entry, dict) and entry.get("name") == env_name:
                    value = str(entry.get("value") or "")
                    # Matches main.go's splitList: values are comma-separated
                    # and the relay trims each entry, so a folded YAML block
                    # scalar's incidental leading space must be tolerated the
                    # same way here or this check reads the wrong globs.
                    globs.extend(g.strip() for g in value.split(",") if g.strip())
    return globs


def _glob_reaches_prefix(prefix_parts: tuple[str, ...], glob: str) -> bool:
    """True if some file directly or transitively under directory
    prefix_parts could satisfy glob, under path.Match / Go path.Match
    semantics: '*' matches within a single '/'-delimited segment, and a
    glob's segment count is fixed. The glob must therefore have strictly
    more segments than the prefix (the prefix is a directory, not a file),
    and its leading segments must each match the prefix's corresponding
    segment."""
    glob_parts = glob.split("/")
    if len(glob_parts) <= len(prefix_parts):
        return False
    return all(fnmatch.fnmatchcase(p, g) for p, g in zip(prefix_parts, glob_parts))


def find_relay_allowlist_overlap(root: Path, allowlist: Path = ALLOWLIST) -> list[tuple[str, str]]:
    """Cross-check the relay's GITHUB_PATH_ALLOWLIST against this checker's
    own record of what is not actually reconciled. Returns (orphan_path,
    glob) pairs for every allowlist entry a GITHUB_PATH_ALLOWLIST glob can
    still reach after GITHUB_PATH_DENYLIST exceptions are subtracted -- an
    empty result means every debt-marked orphan is either unreachable by the
    relay's globs or has a matching denylist entry."""
    prefixes = [p.rstrip("/") for p in load_allowlist(allowlist)]
    allow_globs = parse_relay_path_globs(root, "GITHUB_PATH_ALLOWLIST")
    deny_globs = parse_relay_path_globs(root, "GITHUB_PATH_DENYLIST")

    hits = []
    for prefix in prefixes:
        parts = tuple(prefix.split("/"))
        reaching = [g for g in allow_globs if _glob_reaches_prefix(parts, g)]
        if not reaching:
            continue
        denied = any(_glob_reaches_prefix(parts, g) for g in deny_globs)
        if not denied:
            hits.append((prefix, reaching[0]))
    return hits


def find_orphans(root: Path, allowlist: Path = ALLOWLIST) -> list[tuple[str, str]]:
    prefixes = load_allowlist(allowlist)
    rules_by_tenant = {}
    for tenant_file in sorted((root / "tenants").glob("*/tenant.yaml")):
        rules_by_tenant[tenant_file.parent.name] = service_rules(load_tenant(tenant_file))

    orphans = []
    for rel in iter_yaml(root):
        top = rel.parts[0]
        if top in NON_MANIFEST_TREES:
            continue
        if top == "bootstrap":
            reason = bootstrap_verdict(rel)
        elif top in WATCHED_TREES:
            continue
        elif top == "tenants":
            reason = tenant_verdict(rel, rules_by_tenant)
        else:
            reason = f"top-level {top}/ is not a path any Application or ApplicationSet reads"
        if reason and not allowed(rel.as_posix(), prefixes):
            orphans.append((rel.as_posix(), reason))
    return orphans


def main() -> int:
    orphans = find_orphans(REPO_ROOT)
    overlap = find_relay_allowlist_overlap(REPO_ROOT)
    ok = True

    if orphans:
        ok = False
        print("ORPHANED MANIFEST — YAML that no ArgoCD Application or ApplicationSet reads:")
        for path, reason in orphans:
            print(f"  {path}: {reason}")
        print(
            "\nReconciled paths are bootstrap/** (except crds/, read one level deep),"
            " helm-charts/**, tenants/<t>/tenant.yaml,"
            " tenants/<t>/services/<release>/values.yaml and"
            " tenants/<t>/services/<release>/postInstall/*.yaml for a release listed in"
            " that tenant.yaml. Move the file there, list the release, or delete it;"
            " a dormant release can be parked in tools/orphaned-manifest-allowlist.yaml with a reason."
        )

    if overlap:
        ok = False
        if orphans:
            print()
        print(
            "RELAY PATH ALLOWLIST OVERLAP — GITHUB_PATH_ALLOWLIST in"
            f" {RELAY_MANIFEST} can still reach a path"
            " tools/orphaned-manifest-allowlist.yaml records as unreconciled:"
        )
        for path, glob in overlap:
            print(f"  {path}: matches glob {glob!r}")
        print(
            "\nA patch to one of these would pass the relay's repo, path, and"
            " existing-file gates and open a plausible PR that changes nothing —"
            " the JDWLABS-460 symptom, one layer down. Add a matching"
            " GITHUB_PATH_DENYLIST glob in that manifest, or narrow"
            " GITHUB_PATH_ALLOWLIST so it cannot reach the path."
        )

    if ok:
        print("orphaned-manifests: every YAML is watched by ArgoCD or lives in a known non-manifest tree,"
              " and the relay's path allowlist cannot reach a known orphan")
        return 0
    return 1


if __name__ == "__main__":
    sys.exit(main())
