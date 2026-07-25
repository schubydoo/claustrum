#!/usr/bin/env python3
"""Bake the release version + timestamp into ``buildstamp.go``.

A ``go install github.com/schubydoo/claustrum@vX.Y.Z`` build compiles the tagged
source out of the module cache. That path has no VCS context, so the toolchain
records no ``vcs.time`` build setting, and ``debug.BuildInfo`` has no timestamp
field of its own -- there is nothing for the daemon to read at runtime, and the
installer passes no ``-ldflags``. The only channel that reaches such a build is
the source itself, so the release pipeline writes the stamp into it.

Runs as a Command step right after ``PrepareRelease`` (see ``knope.toml``), which
has already bumped ``VERSION``; knope's following ``git commit -am`` picks the
rewritten file up (it is tracked, so ``-a`` stages it). The commit lands on the
release PR, gets tagged, and ``main.go``'s ``applyReleaseStamp`` consumes it --
only when the resolved module version matches ``releaseVersion`` exactly, so a
pseudo-version install (``@main``, ``@<sha>``) never inherits a release's time.

Usage: ``write_build_stamp.py <version> [timestamp] [path]``
       (knope passes ``$version``; ``timestamp`` defaults to now, UTC, and exists
       so the output can be pinned in tests).

Fails loud on a malformed version or timestamp rather than shipping a wrong
stamp -- a bad value here is baked into a tag permanently. Stdlib only.
"""

import re
import sys
from datetime import datetime, timezone

PATH = "buildstamp.go"
VERSION_RE = re.compile(r"^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$")
STAMP_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
CONST_RE = re.compile(
    r'(?P<head>\nconst \(\n\treleaseVersion = ")[^"]*(?P<mid>"\n\treleaseTime    = ")[^"]*(?P<tail>"\n\)\n)'
)


def utc_now() -> str:
    """Return the current time as an RFC 3339 UTC stamp (second precision)."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def rewrite(text: str, version: str, stamp: str) -> str:
    """Return ``text`` with the two generated consts set to ``version``/``stamp``.

    Raises ``ValueError`` if the const block isn't in the shape this script
    writes -- meaning buildstamp.go was hand-edited or gofmt reflowed it, either
    of which must fail the release rather than silently produce no stamp.
    """
    if not VERSION_RE.match(version):
        raise ValueError(f"version {version!r} is not a bare X.Y.Z (no leading 'v')")
    if not STAMP_RE.match(stamp):
        raise ValueError(f"timestamp {stamp!r} is not RFC 3339 UTC, e.g. 2026-07-25T21:04:16Z")

    updated, n = CONST_RE.subn(rf'\g<head>{version}\g<mid>{stamp}\g<tail>', text)
    if n != 1:
        raise ValueError(
            f"{PATH}: expected exactly one generated const block, found {n} -- "
            "has the file been edited by hand?"
        )
    return updated


def main(argv: list[str]) -> int:
    """CLI entry: ``write_build_stamp.py <version> [timestamp] [path]``."""
    if not 2 <= len(argv) <= 4:
        print(__doc__)
        return 2
    version = argv[1]
    stamp = argv[2] if len(argv) > 2 else utc_now()
    path = argv[3] if len(argv) > 3 else PATH
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    updated = rewrite(text, version, stamp)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(updated)
    print(f"stamped {path}: releaseVersion={version} releaseTime={stamp}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
