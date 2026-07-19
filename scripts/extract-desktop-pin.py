#!/usr/bin/env python3
"""Extract the pinned claude-ssh (and claude-code CLI) build info from a Claude
Desktop for Linux `.deb`.

Claude Desktop bakes the reference daemon's version + per-platform manifest into
the app as a `JSON.parse('{"version":"<sha>",…,"baseUrl":".../claude-ssh-releases"}')`
literal inside `resources/app.asar`. This is the offline "new SHA" signal that
Step 1 of docs/UPSTREAM-TRACKING.md calls for — read it without connecting
anywhere.

Self-contained: parses the `ar`(deb) container and its `data.tar.xz` with the
Python stdlib only (no dpkg-deb / ar / tar / asar needed), so it runs the same on
a dev box and a CI runner.

Usage:
  extract-desktop-pin.py <path-to.deb> [--json]
  extract-desktop-pin.py --asar <app.asar> [--json]

Output (human): the claude-ssh SHA, baseUrl, per-platform checksums, derived URLs,
and the CLI pin. With --json: a machine-readable object (used by the cron watcher).

Exit: 0 on success, 2 on any failure (no blob found, bad container, etc.).
"""
from __future__ import annotations

import io
import json
import lzma
import sys
import tarfile

ASAR_MEMBER = "./usr/lib/claude-desktop/resources/app.asar"
SSH_MARKER = "claude-ssh-releases"
CLI_MARKER = "claude-code-releases"


def read_ar_member(data: bytes, want_prefix: str) -> bytes:
    """Return the first `ar` member whose name starts with want_prefix.

    The `.deb` is a plain `ar` archive: an 8-byte magic ("!<arch>\\n") then
    60-byte headers (name[16], mtime[12], uid[6], gid[6], mode[8], size[10],
    magic[2]) each followed by `size` bytes, 2-byte aligned.
    """
    if data[:8] != b"!<arch>\n":
        raise ValueError("not an ar archive (bad magic)")
    off = 8
    while off + 60 <= len(data):
        header = data[off : off + 60]
        name = header[0:16].decode("ascii", "replace").strip()
        size = int(header[48:58].decode("ascii").strip())
        body_start = off + 60
        body = data[body_start : body_start + size]
        if name.rstrip("/").startswith(want_prefix):
            return body
        off = body_start + size + (size & 1)  # 2-byte alignment padding
    raise ValueError(f"ar member starting with {want_prefix!r} not found")


def extract_asar_from_deb(deb: bytes) -> bytes:
    """Pull app.asar out of a .deb (ar -> data.tar.xz -> tar member)."""
    data_tar_xz = read_ar_member(deb, "data.tar")
    tar_bytes = lzma.decompress(data_tar_xz)
    with tarfile.open(fileobj=io.BytesIO(tar_bytes)) as tf:
        for member in tf:
            if member.name.lstrip("./") == ASAR_MEMBER.lstrip("./"):
                f = tf.extractfile(member)
                if f is None:
                    break
                return f.read()
    raise ValueError(f"{ASAR_MEMBER} not found inside data.tar")


def find_build_info(asar: bytes, marker: str) -> dict:
    """Find the JSON build-info object whose baseUrl ends in `marker`.

    The object is embedded as text (a `JSON.parse('{...}')` string literal) inside
    the concatenated asar payload. `baseUrl` sits near the end of the object, and
    the object is nested (`manifest` carries its own `version`+`platforms`), so we
    can't anchor on a key. Instead: locate the marker, then walk candidate opening
    braces from nearest-to-marker outward and return the first object that *encloses*
    the marker and JSON-parses with a matching `baseUrl`. Inner objects (`manifest`,
    each platform) close before the marker, so they're skipped; the first enclosing,
    parseable object is the real top-level build-info. This is independent of the
    (per-build-random) chunk filename and wrapper function name.
    """
    text = asar.decode("latin-1")  # byte-faithful; JSON is ASCII
    found_marker = False
    hit = text.find(marker)
    while hit != -1:
        found_marker = True
        # Candidate opening braces before the marker, nearest first, within a
        # generous window (the whole build-info object is well under this).
        window_start = max(0, hit - 20000)
        pos = hit
        while True:
            brace = text.rfind("{", window_start, pos)
            if brace == -1:
                break
            pos = brace  # next search ends before this brace
            obj = brace_match(text, brace)
            if obj is None or (brace + len(obj)) <= hit:
                continue  # doesn't enclose the marker
            try:
                info = json.loads(obj)
            except json.JSONDecodeError:
                continue
            if isinstance(info, dict) and str(info.get("baseUrl", "")).endswith(marker):
                return info
        hit = text.find(marker, hit + len(marker))
    where = "no baseUrl object enclosed it" if found_marker else "marker absent"
    raise ValueError(f"build info for {marker!r} not recovered ({where})")


def brace_match(text: str, start: int) -> str | None:
    """Return the substring from `start` ('{') through its matching '}', honoring
    strings and escapes so braces inside string values don't miscount."""
    depth = 0
    in_str = False
    esc = False
    for i in range(start, len(text)):
        c = text[i]
        if in_str:
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
            continue
        if c == '"':
            in_str = True
        elif c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return text[start : i + 1]
    return None


def build_urls(info: dict) -> list[str]:
    base = info["baseUrl"].rstrip("/")
    ver = info["version"]
    plats = (info.get("manifest") or {}).get("platforms") or {}
    leaf = "claude-ssh.zst" if base.endswith(SSH_MARKER) else "claude.zst"
    return [f"{base}/{ver}/{p}/{leaf}" for p in sorted(plats)]


def main(argv: list[str]) -> int:
    args = argv[1:]
    as_json = "--json" in args
    args = [a for a in args if a != "--json"]
    if not args:
        print(__doc__, file=sys.stderr)
        return 2

    try:
        if args[0] == "--asar":
            asar = open(args[1], "rb").read()
        else:
            asar = extract_asar_from_deb(open(args[0], "rb").read())
    except (OSError, ValueError, IndexError) as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    try:
        ssh = find_build_info(asar, SSH_MARKER)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    cli = None
    try:
        cli = find_build_info(asar, CLI_MARKER)
    except ValueError:
        pass  # CLI pin is a bonus; absence is not fatal

    out = {
        "claude_ssh": {
            "version": ssh["version"],
            "baseUrl": ssh["baseUrl"],
            "platforms": (ssh.get("manifest") or {}).get("platforms") or {},
            "urls": build_urls(ssh),
        },
        "claude_code_cli": (
            {"version": cli["version"], "baseUrl": cli["baseUrl"]} if cli else None
        ),
    }

    if as_json:
        print(json.dumps(out, indent=2, sort_keys=True))
        return 0

    s = out["claude_ssh"]
    print(f"claude_ssh version : {s['version']}")
    print(f"claude_ssh baseUrl : {s['baseUrl']}")
    print("platforms:")
    for p, v in sorted(s["platforms"].items()):
        print(f"  {p:<14} sha256={v.get('checksum')}  size={v.get('size')}")
    print("download URLs:")
    for u in s["urls"]:
        print(f"  {u}")
    if out["claude_code_cli"]:
        c = out["claude_code_cli"]
        print(f"claude-code CLI    : {c['version']}  ({c['baseUrl']})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
