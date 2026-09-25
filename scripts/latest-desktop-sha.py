#!/usr/bin/env python3
"""Discover the claude-ssh SHA pinned by the *latest* Claude Desktop for one
platform and compare it to the baseline in scripts/UPSTREAM_SHA.

This automates Step 1 of docs/UPSTREAM-TRACKING.md ("find a candidate SHA"): it
reads the platform's update feed, picks the newest Desktop build, downloads that
package, and runs `extract-desktop-pin.py` to read the pinned reference SHA out
of the app bundle — no CDN guessing, no running daemon. The scheduled
`upstream-desktop-watch.yml` workflow calls this once per platform and opens an
issue when a pin is new: not the baseline, and not any full SHA recorded in
docs/REFERENCE-BUILDS.md.

The platforms ship on separate schedules, so one can pin a newer reference build
than another. Feeds:
  linux   — the APT `Packages` index (`.deb`, SHA-256 checked)
  windows — the Squirrel `RELEASES` file for win32/x64 (`-full.nupkg`, SHA-1 and
            size checked)
  macos   — the Squirrel.Mac `RELEASES.json` for darwin/universal (`.zip`; the
            feed carries no checksum, so the download is trusted over TLS only)

Usage:
  latest-desktop-sha.py                     # linux: fetch latest, print summary
  latest-desktop-sha.py --platform windows  # linux | windows | macos
  latest-desktop-sha.py --peek              # ONLY read the latest version (no download)
  latest-desktop-sha.py --deb PATH          # use a local package (skip the download)
  latest-desktop-sha.py --github-output     # also append vars to $GITHUB_OUTPUT

`--peek` is the cheap no-op gate for the watcher: it reads just the update feed
to learn the newest Desktop version, so an already-analyzed version costs no
download (166 MB for Linux, 250-380 MB for Windows and macOS). `--deb` accepts a
`.deb`, `.nupkg` or `.zip`; the extractor picks the container by magic bytes.

Stdlib only. Exit 0 on success (a *new* pin is reported via the `is_new` output
var, not the exit code), 2 on failure — including a missing/unreadable
`scripts/UPSTREAM_SHA`, which would otherwise silently disable detection
(Greptile P1).
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
EXTRACTOR = os.path.join(HERE, "extract-desktop-pin.py")
UPSTREAM_SHA_FILE = os.path.join(HERE, "UPSTREAM_SHA")
LEDGER_FILE = os.path.join(HERE, os.pardir, "docs", "REFERENCE-BUILDS.md")

APT_BASE = "https://downloads.claude.ai/claude-desktop/apt/stable/"
PACKAGES = APT_BASE + "dists/stable/main/binary-amd64/Packages"
WIN_BASE = "https://downloads.claude.ai/releases/win32/x64/"
MAC_FEED = "https://downloads.claude.ai/releases/darwin/universal/RELEASES.json"
PLATFORMS = ("linux", "windows", "macos")


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
    """Order Debian versions newest-last. Honors an optional `epoch:` prefix
    (a higher epoch always wins, e.g. `2:1.0.0` > `1.999.0`) and compares the
    remaining version by its numeric runs. Claude Desktop currently ships plain
    dotted versions (`1.22209.0`), but parsing the epoch keeps the "latest"
    selection correct if one is ever introduced (Greptile P2)."""
    epoch = 0
    rest = v
    if ":" in v:
        e, rest = v.split(":", 1)
        epoch = int(e) if e.isdigit() else 0
    nums = tuple(int(n) for n in re.findall(r"\d+", rest))
    return (epoch, nums)


def fetch_text(url: str) -> str:
    # utf-8-sig: the Windows RELEASES file starts with a byte-order mark.
    return urllib.request.urlopen(url, timeout=60).read().decode("utf-8-sig")


def parse_win_releases(text: str) -> list[dict]:
    """Parse a Squirrel.Windows `RELEASES` file: one `<SHA1> <file> <size>` per
    line. Only `-full.nupkg` entries count; a delta package cannot be extracted
    on its own."""
    out = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) != 3:
            continue
        sha1, name, size = parts
        m = re.search(r"-(\d[0-9A-Za-z.+~-]*)-full\.nupkg$", name)
        if m and size.isdigit():
            out.append({"version": m.group(1), "url": WIN_BASE + name,
                        "sha1": sha1.lower(), "size": int(size)})
    return out


def latest_package(platform: str = "linux") -> dict:
    """Return the newest build for `platform` as {version, url} plus whatever
    integrity data the feed publishes (sha256, or sha1 + size)."""
    if platform == "linux":
        pkgs = parse_packages(fetch_text(PACKAGES))
        if not pkgs:
            raise ValueError("no packages parsed from APT index")
        p = max(pkgs, key=lambda p: version_key(p["Version"]))
        return {"version": p["Version"], "url": APT_BASE + p["Filename"],
                "sha256": p.get("SHA256")}
    if platform == "windows":
        rels = parse_win_releases(fetch_text(WIN_BASE + "RELEASES"))
        if not rels:
            raise ValueError("no -full.nupkg parsed from Windows RELEASES")
        return max(rels, key=lambda r: version_key(r["version"]))
    if platform == "macos":
        feed = json.loads(fetch_text(MAC_FEED))
        cur = feed.get("currentRelease")
        for rel in feed.get("releases") or []:
            upd = rel.get("updateTo") or {}
            if rel.get("version") == cur and upd.get("url"):
                return {"version": cur, "url": upd["url"]}
        raise ValueError(f"currentRelease {cur!r} has no url in macOS RELEASES.json")
    raise ValueError(f"unknown platform {platform!r} (want one of {', '.join(PLATFORMS)})")


def download(url: str, dest: str, pkg: dict) -> None:
    """Stream `url` to `dest`, then check every digest and size the feed gave."""
    digests = {k: hashlib.new(k) for k in ("sha256", "sha1") if pkg.get(k)}
    size = 0
    with urllib.request.urlopen(url, timeout=300) as r, open(dest, "wb") as f:
        while True:
            chunk = r.read(1 << 20)
            if not chunk:
                break
            f.write(chunk)
            size += len(chunk)
            for h in digests.values():
                h.update(chunk)
    for k, h in digests.items():
        if h.hexdigest() != pkg[k].lower():
            raise ValueError(f"package {k} mismatch: want={pkg[k]} got={h.hexdigest()}")
    if pkg.get("size") and size != pkg["size"]:
        raise ValueError(f"package size mismatch: want={pkg['size']} got={size}")


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


def read_ledger_shas() -> set[str]:
    """Full 40-hex SHAs recorded in docs/REFERENCE-BUILDS.md. A platform whose
    Desktop lags behind still pins an older, already-reconciled build; that pin
    is known, not new. A missing ledger yields an empty set, which errs toward a
    noisy issue rather than a silent miss."""
    try:
        with open(LEDGER_FILE) as f:
            return set(re.findall(r"\b[0-9a-f]{40}\b", f.read()))
    except OSError:
        return set()


def main(argv: list[str]) -> int:
    args = argv[1:]
    github_output = "--github-output" in args
    peek = "--peek" in args
    args = [a for a in args if a not in ("--github-output", "--peek")]
    platform = "linux"
    if "--platform" in args:
        i = args.index("--platform")
        platform = args[i + 1] if i + 1 < len(args) else ""
        if platform not in PLATFORMS:
            print(f"error: --platform must be one of {', '.join(PLATFORMS)}", file=sys.stderr)
            return 2
    deb_path = None
    if "--deb" in args:
        deb_path = args[args.index("--deb") + 1]

    # --peek: cheap no-op gate — read only the newest version from the feed.
    if peek:
        try:
            pkg = latest_package(platform)
        except (ValueError, KeyError, urllib.error.URLError, OSError) as e:
            print(f"error: {e}", file=sys.stderr)
            return 2
        ver = pkg["version"]
        print(f"desktop_version : {ver}")
        if github_output and os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a") as f:
                f.write(f"desktop_version={ver}\n")
        return 0

    try:
        if deb_path:
            desktop_version = os.path.basename(deb_path)
            for tok in os.path.basename(deb_path).split("_"):
                if tok and tok[0].isdigit():
                    desktop_version = tok
                    break
            info = extract_pin(deb_path)
        else:
            pkg = latest_package(platform)
            desktop_version = pkg["version"]
            with tempfile.TemporaryDirectory() as td:
                dest = os.path.join(td, "claude-desktop.pkg")
                print(f"downloading Desktop {desktop_version} ({platform}) ...", file=sys.stderr)
                download(pkg["url"], dest, pkg)
                info = extract_pin(dest)
    except (OSError, ValueError, KeyError, urllib.error.URLError) as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    ssh = info["claude_ssh"]
    sha = ssh["version"]
    baseline = read_baseline()
    if not baseline:
        # A missing/unreadable baseline would make every SHA look unchanged and
        # keep the workflow silently green. Fail loudly instead (Greptile P1).
        print(
            f"error: baseline {UPSTREAM_SHA_FILE} is missing or empty — "
            "cannot determine whether the pin changed",
            file=sys.stderr,
        )
        return 2
    # New = not the baseline and not any older build in the ledger, so a
    # lagging platform does not re-report a build that was already reconciled.
    is_new = sha != baseline and sha not in read_ledger_shas()
    cli = (info.get("claude_code_cli") or {}).get("version", "-")

    if not deb_path:
        print(f"platform        : {platform}")
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
