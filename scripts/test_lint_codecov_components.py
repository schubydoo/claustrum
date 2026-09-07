#!/usr/bin/env python3
"""Fixture tests for ``lint_codecov_components.py`` (stdlib ``unittest``).

The lint is a required CI gate, so its parser and its three failure modes are pinned
here rather than trusted to "it passes on the current tree". Each case builds a
throwaway repo root with a handful of ``.go`` files and a ``codecov.yml``, then asserts
the exact problem list. Run with ``python3 -m unittest discover -s scripts -p 'test_*.py'``.
"""

from __future__ import annotations

import os
import tempfile
import unittest

import lint_codecov_components as lint

_HEADER = """\
# comment before the map
codecov:
  require_ci_to_pass: false
flags:
  paths:
    - not_a_component.go
component_management:
  individual_components:
    - component_id: a
      name: a
      paths:
"""

_TRAILER = """\
other_top_level_key:
  paths:
    - after_the_section.go
"""


def _entries(*names: str) -> str:
    """Render ``names`` as indented ``- name`` list items, with a comment line between."""
    return "".join(f"        # about {n}\n        - {n}\n" for n in names)


class LintCodecovComponentsTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = self._tmp.name
        self.addCleanup(self._tmp.cleanup)

    def _write_repo(self, go_files: list[str], yml: str) -> None:
        for name in go_files:
            with open(os.path.join(self.root, name), "w", encoding="utf-8") as fh:
                fh.write("package main\n")
        with open(os.path.join(self.root, "codecov.yml"), "w", encoding="utf-8") as fh:
            fh.write(yml)

    def test_all_mapped_once_passes(self) -> None:
        self._write_repo(
            ["a.go", "b_unix.go", "a_test.go", "b_unix_test.go"],
            _HEADER + _entries("a.go", "b_unix.go") + _TRAILER,
        )
        self.assertEqual(lint.lint(self.root), [])

    def test_missing_production_file_is_reported(self) -> None:
        self._write_repo(["a.go", "b.go"], _HEADER + _entries("a.go") + _TRAILER)
        problems = lint.lint(self.root)
        self.assertEqual(len(problems), 1)
        self.assertTrue(problems[0].startswith("b.go: production file missing"), problems)

    def test_stale_entry_is_reported(self) -> None:
        self._write_repo(["a.go"], _HEADER + _entries("a.go", "ghost.go") + _TRAILER)
        problems = lint.lint(self.root)
        self.assertEqual(len(problems), 1)
        self.assertTrue(problems[0].startswith("ghost.go: listed in codecov.yml but no such"), problems)

    def test_duplicate_entry_is_reported(self) -> None:
        self._write_repo(["a.go"], _HEADER + _entries("a.go", "a.go") + _TRAILER)
        problems = lint.lint(self.root)
        self.assertEqual(problems, ["a.go: listed more than once in codecov.yml."])

    def test_section_boundaries(self) -> None:
        # `.go` list items before component_management (the flags block) and after the
        # next top-level key are not component entries: neither may count as listed, so
        # both show up as production files that are MISSING from the map.
        self._write_repo(
            ["a.go", "not_a_component.go", "after_the_section.go"],
            _HEADER + _entries("a.go") + _TRAILER,
        )
        problems = lint.lint(self.root)
        self.assertEqual(
            [p.split(":")[0] for p in problems],
            ["after_the_section.go", "not_a_component.go"],
            problems,
        )

    def test_entry_regex_is_strict(self) -> None:
        # A glob, a nested path or a trailing comment is not an entry the lint accepts,
        # so the real file behind it reads as missing rather than silently matching.
        yml = _HEADER + "        - '*.go'\n        - sub/a.go\n        - a.go # note\n" + _TRAILER
        self._write_repo(["a.go"], yml)
        problems = lint.lint(self.root)
        self.assertEqual([p.split(":")[0] for p in problems], ["a.go"], problems)

    def test_unreadable_yml_is_a_problem(self) -> None:
        self._write_repo(["a.go"], "")
        os.remove(os.path.join(self.root, "codecov.yml"))
        problems = lint.lint(self.root)
        self.assertEqual(len(problems), 1)
        self.assertIn("could not read codecov.yml", problems[0])

    def test_main_exit_codes(self) -> None:
        self._write_repo(["a.go"], _HEADER + _entries("a.go") + _TRAILER)
        self.assertEqual(lint.main(["lint", self.root]), 0)
        self._write_repo(["a.go", "b.go"], _HEADER + _entries("a.go") + _TRAILER)
        self.assertEqual(lint.main(["lint", self.root]), 1)


if __name__ == "__main__":
    unittest.main()
