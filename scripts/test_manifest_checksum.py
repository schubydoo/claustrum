#!/usr/bin/env python3
"""Fixture tests for ``manifest-checksum.py`` (stdlib ``unittest``, no network).

``check-upstream.sh`` reads the reference checksum through it. The CDN serves the
manifest pretty-printed, and a parse that finds nothing stops the drift check on
every new build. Run with ``python3 -m unittest discover -s scripts -p 'test_*.py'``.
"""

from __future__ import annotations

import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))

_spec = importlib.util.spec_from_file_location(
    "manifest_checksum", os.path.join(HERE, "manifest-checksum.py")
)
assert _spec is not None and _spec.loader is not None
mc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(mc)

SUM = "67837ff79012926ebded7983466d03cf629b626320f09b2fd886be88d3a167c4"
DOC = {
    "version": "c" * 40,
    "platforms": {
        "linux-amd64": {"checksum": SUM, "size": 3121105},
        "linux-arm64": {"checksum": "a" * 64, "size": 2783484},
    },
}


class ManifestChecksumTest(unittest.TestCase):
    def test_pretty_printed(self) -> None:
        self.assertEqual(mc.checksum(json.dumps(DOC, indent=2), "linux-amd64"), SUM)

    def test_one_line(self) -> None:
        text = json.dumps(DOC, separators=(",", ":"))
        self.assertEqual(mc.checksum(text, "linux-amd64"), SUM)

    def test_missing_platform(self) -> None:
        self.assertEqual(mc.checksum(json.dumps(DOC, indent=2), "windows-amd64"), "")

    def test_bad_or_missing_checksum(self) -> None:
        for value in (None, SUM.upper(), SUM[:-1], SUM + "0", 7):
            plat = {} if value is None else {"checksum": value}
            text = json.dumps({"platforms": {"linux-amd64": plat}})
            self.assertEqual(mc.checksum(text, "linux-amd64"), "", value)

    def test_not_a_json_object(self) -> None:
        self.assertEqual(mc.checksum("not json", "linux-amd64"), "")
        self.assertEqual(mc.checksum("[]", "linux-amd64"), "")


if __name__ == "__main__":
    unittest.main()
