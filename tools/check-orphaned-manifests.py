#!/usr/bin/env python3
"""Reject YAML that no ArgoCD Application or ApplicationSet reconciles.

A manifest can be syntactically perfect, pass kubeconform, and still change
nothing, because ArgoCD only reads the paths its Applications name. Nine
auto-remediation PRs in a row wrote plausible-looking manifests under
invented top-level directories (manifests/, cluster/, clusters/, apps/) and
every one was silently inert. Nothing in Validate caught it, because every
existing gate checks a file's content, not whether anything reads it.

The watched set is derived from the wiring itself rather than restated:

- bootstrap/**              root-app.yaml, recurse: true
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

Usage:
    python3 tools/check-orphaned-manifests.py

Exit codes: 0 = every YAML is either watched or known non-manifest;
1 = at least one orphan was found.
"""
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
ALLOWLIST = Path(__file__).resolve().parent / "orphaned-manifest-allowlist.yaml"

# Reconciled wholesale: every file under these is read by an Application.
WATCHED_TREES = ("bootstrap", "helm-charts")

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


def find_orphans(root: Path, allowlist: Path = ALLOWLIST) -> list[tuple[str, str]]:
    prefixes = load_allowlist(allowlist)
    rules_by_tenant = {}
    for tenant_file in sorted((root / "tenants").glob("*/tenant.yaml")):
        rules_by_tenant[tenant_file.parent.name] = service_rules(load_tenant(tenant_file))

    orphans = []
    for rel in iter_yaml(root):
        top = rel.parts[0]
        if top in WATCHED_TREES or top in NON_MANIFEST_TREES:
            continue
        if top == "tenants":
            reason = tenant_verdict(rel, rules_by_tenant)
        else:
            reason = f"top-level {top}/ is not a path any Application or ApplicationSet reads"
        if reason and not allowed(rel.as_posix(), prefixes):
            orphans.append((rel.as_posix(), reason))
    return orphans


def main() -> int:
    orphans = find_orphans(REPO_ROOT)
    if not orphans:
        print("orphaned-manifests: every YAML is watched by ArgoCD or lives in a known non-manifest tree")
        return 0
    print("ORPHANED MANIFEST — YAML that no ArgoCD Application or ApplicationSet reads:")
    for path, reason in orphans:
        print(f"  {path}: {reason}")
    print(
        "\nReconciled paths are bootstrap/**, helm-charts/**, tenants/<t>/tenant.yaml,"
        " tenants/<t>/services/<release>/values.yaml and"
        " tenants/<t>/services/<release>/postInstall/*.yaml for a release listed in"
        " that tenant.yaml. Move the file there, list the release, or delete it;"
        " a dormant release can be parked in tools/orphaned-manifest-allowlist.yaml with a reason."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
