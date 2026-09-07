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
``scripts/test_lint_codecov_components.py`` pins the parser and the three failure
modes against fixtures.
"""

from __future__ import annotations

import glob
import os
import re
import sys

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


def _production_files(root: str) -> set[str]:
    """Return the root-level production `.go` files (everything but `_test.go`)."""
    return {
        os.path.basename(p)
        for p in glob.glob(os.path.join(root, "*.go"))
        if not p.endswith("_test.go")
    }


def lint(root: str) -> list[str]:
    """Compare the two sets under ``root``; return one message per problem (empty = OK)."""
    path = os.path.join(root, _CODECOV_YML)
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        return [f"could not read {_CODECOV_YML} ({exc})."]
    listed = _listed_paths(text)
    listed_set = set(listed)
    production = _production_files(root)

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
    return problems


def main(argv: list[str]) -> int:
    """Lint the repo at argv[1] (default: the current directory); print and return 0/1."""
    root = argv[1] if len(argv) > 1 else "."
    problems = lint(root)
    if problems:
        print("codecov components lint FAILED:\n")
        for problem in problems:
            print(f"  x {problem}\n")
        return 1
    print(
        f"codecov components lint: {len(_production_files(root))} production file(s) "
        f"all mapped once."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
