#!/usr/bin/env python3
"""Fixture tests for ``latest-desktop-sha.py`` and ``extract-desktop-pin.py``
(stdlib ``unittest``, no network).

The upstream watcher is the only signal that a new reference build exists, and a
parser that silently stops matching turns that signal off without a red run. So
the Windows and macOS feed parsers, the download integrity and HTTPS checks, the
ledger match and the zip path of the extractor are pinned here with small in-memory fixtures. Run with
``python3 -m unittest discover -s scripts -p 'test_*.py'``.
"""

from __future__ import annotations

import hashlib
import importlib.util
import io
import os
import tempfile
import unittest
import zipfile
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))


def _load(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, filename))
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


latest = _load("latest_desktop_sha", "latest-desktop-sha.py")
extract = _load("extract_desktop_pin", "extract-desktop-pin.py")

SHA_A = "a" * 40
SHA_B = "b" * 40
BUILD_INFO = (
    'JSON.parse(\'{"version":"%s","manifest":{"version":"%s","platforms":'
    '{"linux-x64":{"checksum":"00","size":1}}},'
    '"baseUrl":"https://downloads.claude.ai/claude-ssh-releases"}\')' % (SHA_A, SHA_A)
)


class FakeResponse(io.BytesIO):
    """Stands in for the urlopen result: a readable body plus geturl()."""

    def __init__(self, body: bytes, final_url: str):
        super().__init__(body)
        self._final_url = final_url

    def geturl(self) -> str:
        return self._final_url


class WindowsReleasesTest(unittest.TestCase):
    def test_keeps_full_packages_only(self):
        text = (
            "AAAA Claude-1.2.3-full.nupkg 10\n"
            "BBBB Claude-1.2.4-delta.nupkg 5\n"
            "CCCC Claude-1.10.0-full.nupkg 11\n"
            "malformed line\n"
        )
        rels = latest.parse_win_releases(text)
        self.assertEqual([r["version"] for r in rels], ["1.2.3", "1.10.0"])
        self.assertEqual(rels[1]["sha1"], "cccc")
        self.assertEqual(rels[1]["size"], 11)
        self.assertEqual(rels[1]["url"], latest.WIN_BASE + "Claude-1.10.0-full.nupkg")

    def test_latest_picks_numeric_newest(self):
        text = "AAAA Claude-1.9.0-full.nupkg 10\nCCCC Claude-1.10.0-full.nupkg 11\n"
        with mock.patch.object(latest, "fetch_text", return_value=text):
            self.assertEqual(latest.latest_package("windows")["version"], "1.10.0")

    def test_no_full_package_is_an_error(self):
        with mock.patch.object(latest, "fetch_text", return_value="BBBB C-1.0-delta.nupkg 5\n"):
            with self.assertRaises(ValueError):
                latest.latest_package("windows")


class MacFeedTest(unittest.TestCase):
    FEED = (
        '{"currentRelease":"2.0.0","releases":['
        '{"version":"1.0.0","updateTo":{"url":"https://x/old.zip"}},'
        '{"version":"2.0.0","updateTo":{"url":"https://x/new.zip"}}]}'
    )

    def test_picks_current_release(self):
        with mock.patch.object(latest, "fetch_text", return_value=self.FEED):
            pkg = latest.latest_package("macos")
        self.assertEqual(pkg, {"version": "2.0.0", "url": "https://x/new.zip"})

    def test_current_release_without_url_is_an_error(self):
        feed = '{"currentRelease":"3.0.0","releases":[{"version":"2.0.0","updateTo":{"url":"https://x"}}]}'
        with mock.patch.object(latest, "fetch_text", return_value=feed):
            with self.assertRaises(ValueError):
                latest.latest_package("macos")

    def test_unknown_platform_is_an_error(self):
        with self.assertRaises(ValueError):
            latest.latest_package("bsd")


class DownloadTest(unittest.TestCase):
    BODY = b"package bytes"

    def _download(self, pkg: dict, url: str = "https://x/p", final_url: str | None = None):
        resp = FakeResponse(self.BODY, final_url or url)
        opener = mock.Mock()
        opener.open.return_value = resp
        with tempfile.TemporaryDirectory() as td, mock.patch.object(latest, "OPENER", opener):
            latest.download(url, os.path.join(td, "p"), pkg)

    def test_matching_digests_and_size_pass(self):
        self._download({
            "sha1": hashlib.sha1(self.BODY).hexdigest().upper(),
            "sha256": hashlib.sha256(self.BODY).hexdigest(),
            "size": len(self.BODY),
        })

    def test_no_integrity_data_passes(self):
        self._download({})

    def test_sha1_mismatch_fails(self):
        with self.assertRaisesRegex(ValueError, "sha1 mismatch"):
            self._download({"sha1": "0" * 40})

    def test_sha256_mismatch_fails(self):
        with self.assertRaisesRegex(ValueError, "sha256 mismatch"):
            self._download({"sha256": "0" * 64})

    def test_size_mismatch_fails(self):
        with self.assertRaisesRegex(ValueError, "size mismatch"):
            self._download({"size": len(self.BODY) + 1})

    def test_plain_http_url_is_refused(self):
        with self.assertRaisesRegex(ValueError, "non-HTTPS URL"):
            self._download({}, url="http://x/p")

    def test_final_url_on_plain_http_is_refused(self):
        with self.assertRaisesRegex(ValueError, "redirect to non-HTTPS"):
            self._download({}, final_url="http://x/p")

    def test_every_redirect_hop_must_be_https(self):
        handler = latest.HttpsOnlyRedirect()
        req = latest.urllib.request.Request("https://x/p")
        with self.assertRaisesRegex(ValueError, "redirect to non-HTTPS"):
            handler.redirect_request(req, None, 302, "Found", {}, "http://y/p")
        nxt = handler.redirect_request(req, None, 302, "Found", {}, "https://y/p")
        self.assertEqual(nxt.full_url, "https://y/p")

    def test_opener_uses_the_https_only_handler(self):
        self.assertTrue(any(isinstance(h, latest.HttpsOnlyRedirect) for h in latest.OPENER.handlers))


class LedgerTest(unittest.TestCase):
    def test_only_build_headings_count(self):
        ledger = (
            "# Reference build ledger\n\n"
            f"### `{SHA_A}` — 2026-09-14 (built)\n\n"
            f"Seen in a note, not yet reconciled: {SHA_B}.\n"
        )
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "REFERENCE-BUILDS.md")
            with open(path, "w") as f:
                f.write(ledger)
            with mock.patch.object(latest, "LEDGER_FILE", path):
                self.assertEqual(latest.read_ledger_shas(), {SHA_A})

    def test_missing_ledger_is_empty(self):
        with mock.patch.object(latest, "LEDGER_FILE", "/nonexistent/ledger.md"):
            self.assertEqual(latest.read_ledger_shas(), set())


class ZipExtractTest(unittest.TestCase):
    def _zip(self, td: str, member: str) -> str:
        path = os.path.join(td, "pkg.zip")
        with zipfile.ZipFile(path, "w") as zf:
            zf.writestr("unrelated.txt", "x")
            zf.writestr(member, "prefix " + BUILD_INFO + " suffix")
        return path

    def test_windows_and_macos_layouts(self):
        for member in ("lib/net45/resources/app.asar", "Claude.app/Contents/Resources/app.asar"):
            with self.subTest(member=member), tempfile.TemporaryDirectory() as td:
                asar = extract.extract_asar(self._zip(td, member))
                info = extract.find_build_info(asar, extract.SSH_MARKER)
                self.assertEqual(info["version"], SHA_A)

    def test_zip_without_asar_is_an_error(self):
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaisesRegex(ValueError, "app.asar not found"):
                extract.extract_asar(self._zip(td, "resources/other.bin"))

    def test_non_zip_goes_to_the_deb_reader(self):
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "pkg.deb")
            with open(path, "wb") as f:
                f.write(b"not an archive")
            with self.assertRaisesRegex(ValueError, "not an ar archive"):
                extract.extract_asar(path)


if __name__ == "__main__":
    unittest.main()
