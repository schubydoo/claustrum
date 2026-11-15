#!/usr/bin/env python3
"""Check that each set of test shards runs every top-level test exactly once.

The shard sets are the -run and -skip flags of the matrix legs in
.github/workflows/ci.yml (one set for each job and OS) and the GIT_SHARD_*
variables of the Makefile, which run ./internal/git/ only.

The test names come from two places. `go test -list .*` names the tests that
build on this host. A scan of the *_test.go files adds the tests that build
on another OS only. Each name of the list must also be in the scan, so a
broken scan fails the check.

Run from the repository root: python3 scripts/check_git_shards.py
"""

import os
import re
import shlex
import subprocess
import sys

CI_FILE = ".github/workflows/ci.yml"
MAKEFILE = "Makefile"
GIT_DIR = "internal/git"

# Each line of go test -list that is no test name starts with one of these.
LIST_END = ("ok ", "ok\t", "?", "FAIL", "---", "exit status", "panic:")

# The scan finds the functions that go test runs with -run: tests, fuzz
# targets and examples. A benchmark runs only with -bench, so no shard runs it.
SCAN_RES = [
    re.compile(r"^func (Test\w*)\(\s*\w+\s+\*testing\.T\s*\)", re.M),
    re.compile(r"^func (Fuzz\w*)\(\s*\w+\s+\*testing\.F\s*\)", re.M),
    re.compile(r"^func (Example\w*)\(\)", re.M),
]
SKIP_DIRS = {".git", "scratch", "testdata", "vendor", "node_modules", "dist"}


def is_test_name(name):
    for prefix in ("Test", "Fuzz", "Example"):
        if name.startswith(prefix):
            rest = name[len(prefix):]
            return rest == "" or not rest[0].islower()
    return False


def module_path():
    with open("go.mod", encoding="utf-8") as f:
        for line in f:
            if line.startswith("module "):
                return line.split()[1]
    sys.exit("check_git_shards: go.mod has no module line")


def listed_names(module):
    """Return {dir: set of names} from go test -list over all packages."""
    out = subprocess.run(
        ["go", "test", "-count=1", "-list", ".*", "./..."],
        capture_output=True, text=True, check=False,
    )
    if out.returncode != 0:
        sys.exit("check_git_shards: go test -list failed:\n" + out.stdout + out.stderr)
    return parse_list(out.stdout, module)


def parse_list(text, module):
    """Return {dir: set of names} from the output of go test -list."""
    names = {}
    pending = []
    for line in text.splitlines():
        if not line.strip():
            continue
        if line.startswith(LIST_END):
            fields = line.split()
            if fields[0] in ("ok", "?") and len(fields) > 1:
                rel = fields[1][len(module):].lstrip("/") or "."
                names.setdefault(rel, set()).update(
                    n for n in pending if not n.startswith("Benchmark"))
            pending = []
            continue
        pending.append(line.strip())
    return names


def scanned_names():
    """Return {dir: set of names} from the *_test.go files of every folder."""
    names = {}
    for root, dirs, files in os.walk("."):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS and not d.startswith("."))
        rel = os.path.normpath(root)
        for fn in files:
            if not fn.endswith("_test.go"):
                continue
            with open(os.path.join(root, fn), encoding="utf-8") as f:
                text = f.read()
            for rx in SCAN_RES:
                for name in rx.findall(text):
                    if is_test_name(name) and name != "TestMain":
                        names.setdefault(rel, set()).add(name)
    return names


def parse_flags(tokens):
    """Turn ['-run=X', '-skip', 'Y'] into [('run', 'X'), ('skip', 'Y')]."""
    flags = []
    i = 0
    while i < len(tokens):
        tok = tokens[i]
        m = re.fullmatch(r"-(run|skip)(?:=(.*))?", tok)
        if not m:
            sys.exit(f"check_git_shards: unknown shard flag {tok!r}")
        if m.group(2) is not None:
            flags.append((m.group(1), m.group(2)))
            i += 1
        else:
            flags.append((m.group(1), tokens[i + 1]))
            i += 2
    return flags


def ci_groups(path=CI_FILE):
    """Return {(job, os): [(shard, flags)]} from the matrix legs of ci.yml."""
    groups = {}
    job = None
    entry = None
    entries = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
            if m:
                job = m.group(1)
                entry = None
                continue
            m = re.match(r"^\s*-\s+(os|shard):\s*(\S+)\s*$", line)
            if m:
                entry = {"job": job, m.group(1): m.group(2)}
                entries.append(entry)
                continue
            m = re.match(r"^\s+(os|shard|select):\s*(.+?)\s*$", line)
            if m and entry is not None and not line.lstrip().startswith("#"):
                entry[m.group(1)] = m.group(2)
    for e in entries:
        if "select" not in e:
            continue
        key = (e["job"], e.get("os", ""))
        groups.setdefault(key, []).append((e.get("shard", ""), parse_flags(e["select"].split())))
    return groups


def make_group(path=MAKEFILE):
    """Return [(shard, flags)] from the GIT_SHARD_* variables of the Makefile."""
    shards = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = re.match(r"^GIT_SHARD_(\w+)\s*:=\s*(.*)$", line)
            if m:
                shards.append((m.group(1).lower(), parse_flags(shlex.split(m.group(2)))))
    return shards


def selected(name, flags):
    for kind, pat in flags:
        hit = re.search(pat, name) is not None
        if kind == "run" and not hit:
            return False
        if kind == "skip" and hit:
            return False
    return True


def check(label, shards, names):
    errors = []
    for d in sorted(names):
        for name in sorted(names[d]):
            hits = [s for s, flags in shards if selected(name, flags)]
            if len(hits) != 1:
                where = ", ".join(hits) if hits else "no shard"
                errors.append(f"{label}: {d}: {name} runs in {where}")
    return errors


def main():
    module = module_path()
    listed = listed_names(module)
    scanned = scanned_names()
    errors = []
    for d, ns in sorted(listed.items()):
        missing = ns - scanned.get(d, set())
        for name in sorted(missing):
            errors.append(f"scan: {d}: {name} is in go test -list but not in the scan")
    allnames = {d: listed.get(d, set()) | scanned.get(d, set())
                for d in set(listed) | set(scanned)}
    total = sum(len(v) for v in allnames.values())
    gitnames = {GIT_DIR: allnames.get(GIT_DIR, set())}
    if total < 100 or len(gitnames[GIT_DIR]) < 100:
        errors.append(f"found only {total} tests, {len(gitnames[GIT_DIR])} in {GIT_DIR}")

    groups = ci_groups()
    if not groups:
        errors.append(f"{CI_FILE}: no shard sets found")
    for (job, osname), shards in sorted(groups.items()):
        errors += check(f"{CI_FILE} {job} {osname}".rstrip(), shards, allnames)

    make = make_group()
    if not make:
        errors.append(f"{MAKEFILE}: no GIT_SHARD_ variables found")
    errors += check(f"{MAKEFILE} {GIT_DIR}", make, gitnames)
    mac = groups.get(("test", "macos-latest"), [])
    if sorted(f for _, f in make) != sorted(f for _, f in mac):
        errors.append(f"{MAKEFILE}: the GIT_SHARD_ sets differ from the macOS legs of {CI_FILE}")

    if errors:
        print("\n".join(errors))
        sys.exit(1)
    print(f"check_git_shards: {total} tests ({len(gitnames[GIT_DIR])} in {GIT_DIR}), "
          f"{len(groups)} CI shard sets and the Makefile set each run every test once")


if __name__ == "__main__":
    main()
