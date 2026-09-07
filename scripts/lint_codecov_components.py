#!/usr/bin/env python3
"""Lint ``codecov.yml`` so every production ``.go`` file sits in exactly one component.

claustrum is a single flat ``package main`` at the repo root, so the
``component_management`` map in ``codecov.yml`` lists files one by one. A production
file that is not listed still counts toward the project total (when the single Linux
coverage cell compiles it), but its lines show under no component in the PR comment
and the dashboard, and nothing reports the gap.
The 4534d86 reconciliation added nine production files over #314-#335 and none of
them reached the map until #336. This lint fails the PR that introduces the tenth.

Rule: the set of ``*.go`` files at the repo root that are not ``_test.go`` files must
equal the set of ``.go`` paths listed under ``component_management``, with no path
listed twice. Test files never appear in a Go coverage profile, so they are not
listed and not checked.

Exit 0 when the two sets match, 1 (with the offending file names) otherwise. Stdlib
only: the map is read with a line regex, not a YAML parser, because the paths are
plain ``- name.go`` list items and CI runs this on a bare Python.
"""

from __future__ import annotations

import glob
import os
import re

_CODECOV_YML = "codecov.yml"

# A component path entry: `        - server.go` (any indent, one file, nothing else).
_PATH_ENTRY = re.compile(r"^\s*-\s+([A-Za-z0-9_]+\.go)\s*$")


def _listed_paths(text: str) -> list[str]:
    """Return every `.go` path listed under ``component_management``, in file order."""
    paths: list[str] = []
    in_components = False
    for line in text.splitlines():
        if line.startswith("component_management:"):
            in_components = True
            continue
        # A new top-level key ends the section.
        if in_components and line and not line[0].isspace() and not line.startswith("#"):
            break
        if in_components:
            m = _PATH_ENTRY.match(line)
            if m:
                paths.append(m.group(1))
    return paths


def _production_files() -> set[str]:
    """Return the root-level production `.go` files (everything but `_test.go`)."""
    return {
        os.path.basename(p) for p in glob.glob("*.go") if not p.endswith("_test.go")
    }


def main() -> int:
    """Compare the two sets; print the differences and return 1 if any, else 0."""
    try:
        with open(_CODECOV_YML, encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        print(f"codecov components lint FAILED: could not read {_CODECOV_YML} ({exc}).")
        return 1
    listed = _listed_paths(text)
    listed_set = set(listed)
    production = _production_files()

    problems = []
    for name in sorted(production - listed_set):
        problems.append(
            f"{name}: production file missing from {_CODECOV_YML} component_management; "
            f"add it under the component that owns its behavior."
        )
    for name in sorted(listed_set - production):
        problems.append(
            f"{name}: listed in {_CODECOV_YML} but no such production file exists; "
            f"remove the stale entry."
        )
    seen: set[str] = set()
    for name in listed:
        if name in seen:
            problems.append(f"{name}: listed more than once in {_CODECOV_YML}.")
        seen.add(name)

    if problems:
        print("codecov components lint FAILED:\n")
        for problem in problems:
            print(f"  x {problem}\n")
        return 1
    print(f"codecov components lint: {len(production)} production file(s) all mapped once.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
