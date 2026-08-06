#!/usr/bin/env python3
"""Reject duplicate leading numeric prefixes among docs/adr/*.md filenames.

ADR numbers have collided by hand more than once (0011 used twice, then a
0012 collision across three concurrent branches) because nothing enforced
uniqueness before merge. This scans docs/adr/*.md, extracts each file's
leading numeric prefix, and fails when two or more files share one.

Usage:
    python3 tools/check-adr-numbering.py

Exit codes: 0 = every prefix unique; 1 = a collision was found.
"""
import re
import sys
from collections import defaultdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ADR_DIR = REPO_ROOT / "docs/adr"
PREFIX_RE = re.compile(r"^(\d+)-")


def main() -> int:
    if not ADR_DIR.is_dir():
        print(f"{ADR_DIR}: not found, nothing to check")
        return 0

    by_prefix = defaultdict(list)
    for path in sorted(ADR_DIR.glob("*.md")):
        match = PREFIX_RE.match(path.name)
        if not match:
            continue  # not a numbered ADR (e.g. README.md, TEMPLATE.md)
        by_prefix[match.group(1)].append(path.name)

    collisions = {prefix: names for prefix, names in by_prefix.items() if len(names) > 1}

    if not collisions:
        print(f"adr-numbering: {len(by_prefix)} numbered ADRs, no duplicate prefixes")
        return 0

    print("DUPLICATE ADR NUMBER — two or more files share the same leading prefix:")
    for prefix, names in sorted(collisions.items()):
        print(f"  {prefix}: {', '.join(sorted(names))}")
    print("\nRenumber one of the colliding files to the next free prefix.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
