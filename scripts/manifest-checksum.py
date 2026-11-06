#!/usr/bin/env python3
"""Print the checksum of one platform from a claude-ssh release manifest.

Usage: ``python3 -I scripts/manifest-checksum.py <manifest.json> <platform>``

``check-upstream.sh`` calls it. The CDN serves the manifest pretty-printed, so a
line-based grep finds no checksum there. This parses the JSON instead. It prints
the checksum only if it is 64 lowercase hex digits. Otherwise it prints nothing,
and the caller reports that the manifest lists no checksum.
"""

from __future__ import annotations

import json
import re
import sys

HEX64 = re.compile(r"[0-9a-f]{64}")


def checksum(text: str, platform: str) -> str:
    try:
        doc = json.loads(text)
    except ValueError:
        return ""
    plats = doc.get("platforms") if isinstance(doc, dict) else None
    entry = plats.get(platform) if isinstance(plats, dict) else None
    value = entry.get("checksum") if isinstance(entry, dict) else None
    if isinstance(value, str) and HEX64.fullmatch(value):
        return value
    return ""


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print("usage: manifest-checksum.py <manifest.json> <platform>", file=sys.stderr)
        return 2
    try:
        with open(argv[1], encoding="utf-8") as f:
            text = f.read()
    except (OSError, UnicodeDecodeError):
        return 0
    got = checksum(text, argv[2])
    if got:
        print(got)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
