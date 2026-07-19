#!/usr/bin/env python3
"""Discover the claude-ssh SHA pinned by the *latest* Claude Desktop for Linux and
compare it to the baseline in scripts/UPSTREAM_SHA.

This automates Step 1 of docs/UPSTREAM-TRACKING.md ("find a candidate SHA"): it
reads the APT `Packages` index, picks the newest `claude-desktop` build, downloads
that `.deb`, and runs `extract-desktop-pin.py` to read the pinned reference SHA out
of the app bundle — no CDN guessing, no running daemon. The scheduled
`upstream-desktop-watch.yml` workflow calls this and opens an issue when the pin
moves off the baseline.

Usage:
  latest-desktop-sha.py                 # fetch latest .deb, print summary
  latest-desktop-sha.py --deb PATH      # use a local .deb (skip the download)
  latest-desktop-sha.py --github-output # also append vars to $GITHUB_OUTPUT

Stdlib only. Exit 0 on success (drift is reported via output vars, not exit code),
2 on failure.
"""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import tempfile
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
EXTRACTOR = os.path.join(HERE, "extract-desktop-pin.py")
UPSTREAM_SHA_FILE = os.path.join(HERE, "UPSTREAM_SHA")

APT_BASE = "https://downloads.claude.ai/claude-desktop/apt/stable/"
PACKAGES = APT_BASE + "dists/stable/main/binary-amd64/Packages"


def parse_packages(text: str) -> list[dict]:
    out = []
    for stanza in text.split("\n\n"):
        o = {}
        for line in stanza.splitlines():
            if ": " in line:
                k, v = line.split(": ", 1)
                o[k] = v
        if o.get("Version") and o.get("Filename"):
            out.append(o)
    return out


def version_key(v: str) -> tuple:
    return tuple(int(p) if p.isdigit() else 0 for p in v.split("."))


def latest_package() -> dict:
    text = urllib.request.urlopen(PACKAGES, timeout=60).read().decode()
    pkgs = parse_packages(text)
    if not pkgs:
        raise ValueError("no packages parsed from APT index")
    return max(pkgs, key=lambda p: version_key(p["Version"]))


def download(url: str, dest: str, expect_sha256: str | None) -> None:
    h = hashlib.sha256()
    with urllib.request.urlopen(url, timeout=300) as r, open(dest, "wb") as f:
        while True:
            chunk = r.read(1 << 20)
            if not chunk:
                break
            f.write(chunk)
            h.update(chunk)
    if expect_sha256 and h.hexdigest() != expect_sha256:
        raise ValueError(f"deb checksum mismatch: want={expect_sha256} got={h.hexdigest()}")


def extract_pin(deb_path: str) -> dict:
    res = subprocess.run(
        [sys.executable, EXTRACTOR, deb_path, "--json"],
        capture_output=True, text=True,
    )
    if res.returncode != 0:
        raise ValueError(f"extractor failed: {res.stderr.strip()}")
    return json.loads(res.stdout)


def read_baseline() -> str:
    try:
        with open(UPSTREAM_SHA_FILE) as f:
            return f.read().strip()
    except OSError:
        return ""


def main(argv: list[str]) -> int:
    args = argv[1:]
    github_output = "--github-output" in args
    args = [a for a in args if a != "--github-output"]
    deb_path = None
    if "--deb" in args:
        deb_path = args[args.index("--deb") + 1]

    try:
        if deb_path:
            desktop_version = os.path.basename(deb_path)
            for tok in os.path.basename(deb_path).split("_"):
                if tok and tok[0].isdigit():
                    desktop_version = tok
                    break
            info = extract_pin(deb_path)
        else:
            pkg = latest_package()
            desktop_version = pkg["Version"]
            url = APT_BASE + pkg["Filename"]
            with tempfile.TemporaryDirectory() as td:
                dest = os.path.join(td, "claude-desktop.deb")
                print(f"downloading Desktop {desktop_version} ...", file=sys.stderr)
                download(url, dest, pkg.get("SHA256"))
                info = extract_pin(dest)
    except (OSError, ValueError, urllib.error.URLError) as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    ssh = info["claude_ssh"]
    sha = ssh["version"]
    baseline = read_baseline()
    is_new = bool(baseline) and sha != baseline
    cli = (info.get("claude_code_cli") or {}).get("version", "-")

    print(f"desktop_version : {desktop_version}")
    print(f"claude_ssh_sha  : {sha}")
    print(f"baseline_sha    : {baseline or '(none)'}")
    print(f"is_new          : {str(is_new).lower()}")
    print(f"claude_code_cli : {cli}")
    print("download URLs:")
    for u in ssh["urls"]:
        print(f"  {u}")

    if github_output and os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as f:
            f.write(f"desktop_version={desktop_version}\n")
            f.write(f"claude_ssh_sha={sha}\n")
            f.write(f"baseline_sha={baseline}\n")
            f.write(f"is_new={'true' if is_new else 'false'}\n")
            f.write(f"claude_code_cli={cli}\n")
            man_url = f"{ssh['baseUrl'].rstrip('/')}/{sha}/manifest.json"
            f.write(f"manifest_url={man_url}\n")

    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
