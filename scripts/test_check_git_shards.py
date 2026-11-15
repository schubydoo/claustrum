#!/usr/bin/env python3
"""Fixture tests for ``check_git_shards.py`` (stdlib ``unittest``).

The check is a CI gate, so its three parsers and its count are pinned here.
Each case feeds a small fixture and asserts the exact result. Run with
``python3 -m unittest discover -s scripts -p 'test_*.py'``.
"""

from __future__ import annotations

import os
import tempfile
import unittest

import check_git_shards as shards

MODULE = "example.com/mod"

LIST_OUTPUT = """\
TestRootA
BenchmarkRoot
ok  \texample.com/mod\t0.010s
?   \texample.com/mod/internal/empty\t[no test files]
TestGitOne
ExampleGit
FuzzGit
ok  \texample.com/mod/internal/git\t0.020s
"""

CI_YAML = """\
jobs:
  test:
    strategy:
      matrix:
        include:
          - os: windows-latest
            shard: worktree
            select: -run=^(TestWorktree|TestCreate) -skip=^TestWorktreeCreate
            # select: -run=^Commented
          - os: windows-latest
            shard: create
            select: -run=^TestWorktreeCreate
          - os: macos-latest
            shard: rest
            select: -skip=^TestWorktree
  coverage-shard:
    strategy:
      matrix:
        include:
          - shard: worktree
            select: -run=^TestWorktree
          - shard: rest
            select: -skip=^TestWorktree
"""

MAKEFILE = """\
GIT_SHARD_WORKTREE := -run '^TestWorktree'
GIT_SHARD_REST     := -skip '^(TestWorktree|TestProbe)'
OTHER := -run '^Nothing'
"""


def write_tmp(test, text):
    fd, path = tempfile.mkstemp()
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(text)
    test.addCleanup(os.remove, path)
    return path


class ParseListTest(unittest.TestCase):
    def test_names_go_to_their_package_folder(self):
        got = shards.parse_list(LIST_OUTPUT, MODULE)
        self.assertEqual(got, {
            ".": {"TestRootA"},
            "internal/empty": set(),
            "internal/git": {"TestGitOne", "ExampleGit", "FuzzGit"},
        })


class ParseFlagsTest(unittest.TestCase):
    def test_equals_and_separate_forms(self):
        self.assertEqual(
            shards.parse_flags(["-run=^A", "-skip", "^B"]),
            [("run", "^A"), ("skip", "^B")])

    def test_unknown_flag_stops(self):
        with self.assertRaises(SystemExit):
            shards.parse_flags(["-bench=."])


class CIGroupsTest(unittest.TestCase):
    def test_both_entry_forms(self):
        got = shards.ci_groups(write_tmp(self, CI_YAML))
        self.assertEqual(got, {
            ("test", "windows-latest"): [
                ("worktree", [("run", "^(TestWorktree|TestCreate)"),
                              ("skip", "^TestWorktreeCreate")]),
                ("create", [("run", "^TestWorktreeCreate")]),
            ],
            ("test", "macos-latest"): [("rest", [("skip", "^TestWorktree")])],
            ("coverage-shard", ""): [
                ("worktree", [("run", "^TestWorktree")]),
                ("rest", [("skip", "^TestWorktree")]),
            ],
        })


class MakeGroupTest(unittest.TestCase):
    def test_shard_variables_only(self):
        got = shards.make_group(write_tmp(self, MAKEFILE))
        self.assertEqual(got, [
            ("worktree", [("run", "^TestWorktree")]),
            ("rest", [("skip", "^(TestWorktree|TestProbe)")]),
        ])


class CheckTest(unittest.TestCase):
    SETS = [
        ("worktree", [("run", "^(TestWorktree|TestCreate)"), ("skip", "^TestWorktreeCreate")]),
        ("create", [("run", "^TestWorktreeCreate")]),
        ("rest", [("skip", "^(TestWorktree|TestCreate)")]),
    ]

    def test_each_name_once(self):
        names = {"d": {"TestWorktreeRemove", "TestWorktreeCreateA", "TestCreateB", "TestOther"}}
        self.assertEqual(shards.check("L", self.SETS, names), [])

    def test_combined_run_and_skip(self):
        flags = self.SETS[0][1]
        self.assertTrue(shards.selected("TestWorktreeRemove", flags))
        self.assertTrue(shards.selected("TestCreateB", flags))
        self.assertFalse(shards.selected("TestWorktreeCreateA", flags))
        self.assertFalse(shards.selected("TestOther", flags))

    def test_missing_selection(self):
        sets = self.SETS[:2]
        self.assertEqual(shards.check("L", sets, {"d": {"TestOther"}}),
                         ["L: d: TestOther runs in no shard"])

    def test_duplicate_selection(self):
        sets = [self.SETS[0], ("create", [("run", "^TestWorktree")]), self.SETS[2]]
        self.assertEqual(shards.check("L", sets, {"d": {"TestWorktreeRemove"}}),
                         ["L: d: TestWorktreeRemove runs in worktree, create"])


if __name__ == "__main__":
    unittest.main()
