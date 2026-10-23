# Staying in lock-step with the reference daemon

claustrum is behaviorally compatible with a reference daemon. That daemon ships
inside Claude Desktop's SSH-remote feature. A git SHA gives its version, and a
public CDN holds it as per-platform zstd blobs. This document tells you how to
detect a new build. It also tells you how to find out whether that build changed
anything claustrum must match. For the running history of which builds changed
what, see the [reference build ledger](REFERENCE-BUILDS.md).

## How the reference is distributed

- Per-version manifest:
  `https://downloads.claude.ai/claude-ssh-releases/<sha>/manifest.json`
  → `{"version":"<sha>","platforms":{"<goos>-<goarch>":{"checksum":"<sha256 of .zst>","size":N}}}`
- Per-platform artifact:
  `https://downloads.claude.ai/claude-ssh-releases/<sha>/<goos>-<goarch>/claude-ssh.zst`
- Six targets: `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`,
  `windows-amd64`, `windows-arm64`. (GOARCH naming: `amd64`, not `x64`.)

There is no "latest" index. The SHA is the key to each release, so step 1 is
always "find the new SHA".

## Step 1 — find a candidate SHA

The SHA of the reference build is the SHA Claude Desktop deploys now. These are the
sources, easiest first:

1. A host that Desktop connected to. The cache holds one daemon per SHA:
   ```sh
   ls -1d ~/.claude/remote/srv/*/server | sed -E 's#.*/srv/([0-9a-f]+)/server#\1#'
   ```
2. The Desktop app bundle. The app contains the pinned SHA, and all six
   per-platform checksums and sizes, as a build-time constant. You can read
   that constant offline, and it needs no network. In the Linux `.deb` it is a
   `JSON.parse('{"version":"<sha>","manifest":{…},"baseUrl":".../claude-ssh-releases"}')`
   literal inside `resources/app.asar`. That file is a minified
   `.vite/build/index.chunk-*.js`, so the chunk name and the wrapper function are
   random for each build. A parallel literal pins the CLI
   (`claude-code-releases`). From version 1.24012.9 onward, its `baseUrl`
   carries a channel suffix, `claude-code-releases/rc/<sha>`. The extractor
   therefore matches the bucket by path *segment*, not by `endswith`. A new Desktop
   build is itself the "new SHA" signal. Two scripts read the constant:
   - [`scripts/extract-desktop-pin.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/extract-desktop-pin.py)
     reads it directly from a Linux `.deb`, a Windows `.nupkg` or a macOS `.zip`.
     It uses the standard library only: `ar` → `data.tar.xz` → `app.asar`, or zip
     → `app.asar`, then an enclosure brace-match. You need no `dpkg`, no `unzip`
     and no `asar`. The Windows and macOS bundles carry the same literal.
   - [`scripts/latest-desktop-sha.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/latest-desktop-sha.py)
     runs the full loop for one platform (`--platform linux|windows|macos`,
     default `linux`): find the newest Desktop → download → extract → compare to
     `UPSTREAM_SHA`.

   Observed pins: Linux 1.18286.0 (2026-07-02) pinned `7c2f88d…`. Versions
   1.20186.1 → 1.24012.9 pin `5db5e4a…`.
3. The Desktop machine's cache. The per-platform binaries are under
   `<app-data>/claude-ssh-remote/<sha>/` (`%APPDATA%/Claude/…` on Windows),
   beside a `.verified-<goos>-<goarch>` marker.
4. Probe a guess. `manifest.json` returns 200 for a real SHA. It returns 404
   for all other values.

> Note: the *CLI* release manifest
> (`claude-code-releases/<ver>/manifest.json`) has a `commit` field. That field
> gives the CLI's commit, not the daemon's. Do not confuse the two.

### claustrum re-uploads every session (harmless)

Before a session the client runs `server --version` on the cached
`~/.claude/remote/srv/<pinned-sha>/server`. It then matches `/claude-ssh\s+(\S+)/`.
It skips the re-upload only for a first token that equals the pinned SHA.
claustrum prints `claustrum <ver> (built …)`, which is its own identity. So the
token never matches, and the client sends the daemon again by SFTP every
session. That transfer is idempotent and is approximately 2.3 MB. The
`claude-ssh:`→`claustrum:` rebrand causes this. The version line is CLI stdout,
not a JSON-RPC frame. The wire contract therefore does not change, and the redeploy
is harmless. The daemon that runs at the end is still claustrum.

A drop-in build stamp makes the client see claustrum as up-to-date and stops
the overwrite. Such a stamp emits `claude-ssh <pinned-sha>` as the first token,
and the binary goes at `srv/<pinned-sha>/server`. It is off by default. It is an
opt-in build stamp, not a wire change. Mechanism and the `-version` format:
[docs/PROTOCOL.md → `-version`](PROTOCOL.md#-version).

## Step 2 — run the drift check

```sh
scripts/check-upstream.sh <sha>
# or, with the pinned baseline SHA in scripts/UPSTREAM_SHA:
scripts/check-upstream.sh
```

The script needs network access. It does these steps:

1. Compare `<sha>` to the pinned baseline in `scripts/UPSTREAM_SHA`. A difference
   is itself the "new release" signal.
2. Fetch the manifest. Download the `linux-amd64` `claude-ssh.zst`. Check it
   against the manifest checksum. Decompress it.
3. Build claustrum. Then diff the two binaries on the items claustrum must match:
   - method names (`server.*`/`files.*`/`git.*`/`process.*` literals),
   - CLI flags (`-help` output),
   - the `-version` format,
   - the app-facing string set (errors, `[Server]`/`[process.Manager]`/
     `[frameSink]`/`[shellenv]` log lines, flag help).
4. With no drift in the checked surface, print `PASS`. With drift, print `DRIFT`
   with the specifics, and exit with a non-zero status.

This static check needs no running daemon. It is safe to run on any machine that
has network access.

## Step 3 — authoritative byte-for-byte recheck (local only)

The static check catches methods, flags, and strings that were added or removed.
Frame-level byte-identity covers result field order, stream framing, error bodies,
and `reattach` semantics. To make sure that it holds, run the private validation
battery against both the new reference and claustrum. That battery stays in `scratch/` and is not published:

```sh
# starts each binary as a PRIVATE -serve on a throwaway /tmp socket, runs the
# full method battery, and diffs normalized frames. Never touches a live daemon.
scratch/probe/validate.sh <path-to-reference> > /tmp/ref.json
scratch/probe/validate.sh ./claustrum          > /tmp/mine.json
diff /tmp/ref.json /tmp/mine.json
```

> Safety: probe only a private instance on a `/tmp` socket with its own
> `-token-file`. Never point the probe at a live daemon's socket. Clean up after
> every probe.

## Step 3b — real-session capture (highest fidelity, optional)

Steps 2–3 drive the daemon with synthetic requests we write. The best check is
the traffic of the real desktop client. This is the method:

- The bridge (`server --bridge`) is a simple stdio relay. So a tee on its stdin and
  stdout records the exact client↔daemon NDJSON of a live session. You can then
  replay that record against claustrum and diff it.
- The tools are in `scratch/capture/` (gitignored): a `capture-bridge`,
  `replay.js`, and a `REPLAY.md` runbook. `replay.js` diffs without regard to
  order. It keys responses by `id` and stream frames by `processId`+`seq`. It
  masks the version SHA, the token, and (with `--mask-data`) nondeterministic agent
  payloads.
- A throwaway SSH user records the session, through a `ForceCommand` wrapper
  scoped to that user, so the capture never touches a live daemon. Raw dumps carry
  the session token and host paths. Keep them in `scratch/` (gitignored). Never
  commit them.

We last ran this capture against a then-pinned reference, `8de85faa`. That build is
now well behind the current baseline, `5fd08069`. It used a real Desktop session and
covered the full `process.*` lifecycle. That lifecycle included a >32 KiB output
stream and a mid-stream disconnect and reconnect that drove `process.reattach`. The
result was byte-identical for the methods that build exposed. The `server.capabilities`
set, the stream-envelope field order, and per-process `reattach` replay all
matched. [docs/PROTOCOL.md](PROTOCOL.md) holds the canonical frame shapes.

Use this capture as a periodic spot-check. Use it again for a new build that changes
`process.*` behavior the synthetic battery cannot fully model (real reconnect
timing, multi-process reattach).

## Step 4 — reconcile

If the check reports drift:

1. Identify exactly what changed: a new method, a changed error string, a new
   flag, a new install fact, or a changed frame shape.
2. Implement the change in claustrum behind the existing structure. Keep every
   unchanged behavior unchanged.
3. Run the static check and the byte-for-byte battery again, until both are
   clean.
4. Update `scripts/UPSTREAM_SHA` to the new SHA. Note the change in
   `CHANGELOG.md`. If the wire surface changed, update `docs/PROTOCOL.md`.

## Don't reconcile the deliberate divergences away

Not every claustrum behavior is meant to match the reference.
[docs/DIVERGENCES.md](DIVERGENCES.md) catalogs the deliberate divergences
(the D series and CT-1..CT-5) and the four-rule standard that governs them. That file is the
canonical home for what each one is, why it exists, how to activate it, and its
reopen trigger. When the drift check flags one of them, it is expected, not drift.
Check it against the catalog before you "fix" it.

This triage index adds the one question the catalog does not answer directly. Can a
probe even see this divergence? If it can, is the symptom drift or an activated
opt-in?

### Which divergences a probe can see

`battery-visible?` asks whether the standard frame battery (`validate.sh` /
`battery.js`) shows a diff. The install-path entries (D10 and D12) run under
`-install`, which the frame battery never drives at all.

| ID | Default | Battery-visible? | What it is |
|----|---------|------------------|------------|
| D3 | Off (`0` = unlimited) | No. Off the default path | `files.extract_tar` size cap |
| D10 | Off (`0` = unlimited) | No. Install path | `-install` CLI size cap |
| D12 | Off (`0` = no bound) | No. Install path | `-install` download bound |
| D2 | Always-on | Maybe. A probe that reaches the path shows it (expected) | destructive-path home-dir refusal. On `-install` it is the `cli path must not be or contain the home directory` text. That refusal is this guard, not drift. |
| D6 | Always-on | Maybe. A probe that reaches the path shows it (expected) | `-cli-version` single path component |
| D19 | Always-on, Windows only | Maybe. A Windows probe with a junction at any directory between `baseRepo` and the leaf shows it (expected) | `git.worktree_remove` refuses that junction with `failed to remove worktree: openat <path from baseRepo>: path escapes from parent`. At `.claude` or `.claude\worktrees`, `f6010b97` answers success and deletes the branch, and `89cb6289` does the same for a branch that another ref reaches. At a junction `<P>\J` above `.claude`, `89cb6289` answers success after 2 git calls. No leaf and no branch existed there (Windows VM, cells J-b-wt-pj and J-b-wt-pJ). The maintainer widened the entry to those levels on 2026-10-07. That is this entry, not drift |
| D20 | Always-on, Linux and macOS | No. Off the wire. It is a signal time at a daemon start, not a frame | 50 ms settle before the group `SIGKILL` of a leader that reads as gone. In row RP05a that `SIGKILL` comes about 50 ms later than on `89cb6289`. In row PG05a claustrum ends the child in no run, where `89cb6289` ends it in some Linux runs. In rows where the child ends on `SIGTERM`, `89cb6289` also sends a group `SIGKILL` right after it in some Linux runs, and claustrum sends none. None of these is drift |
| D22 | Always-on | Maybe. A probe that reaches the path shows it (expected) | `git.worktree_remove` refuses a worktree that is locked in the `.git` folder of `baseRepo`. `89cb6289` answers success in four states (rows p6, p6d, p6e and p6f), and with `worktreeRoot` rows q2 to q4 differ in the frame too (see the entry). A maintainer decision of 2026-10-03. The refusal is not drift. On Linux and macOS a gone worktree whose locked entry sits behind a linked `<git dir>/worktrees` gets success, and its branch goes. That follows `5fd08069` (cell L1 of round `tier0-26`, Linux VM), by the maintainer's decision of 2026-10-14. |
| D23 | Always-on, Windows only | Maybe. A Windows probe whose child prints its environment block shows it in the stream frames (expected) | the environment block of a child is in name order. `89cb6289` keeps the order of its launching block, then `CLAUDE_SSH_DAEMON_CHILD=1`, then the added entries (Windows VM rows V1 to V10). The set of entries and the response frames are equal in those rows. Cause: the Go version. The other order is not drift |
| D24 | Always-on | No in cells B6, B6b and B6w: the frames are equal and the disk differs (Linux and macOS VMs, and cell B6w on a Windows VM). A probe that lists the registrations folder shows it. Yes in cells X1, X1b and X1c of round `task077-03`, X1 and X1b of round `task077-04` (Windows VM). At the placement the frame holds claustrum's own text `is not the folder that was tested after the add`, or `is not the folder that was moved into place` on Windows (not measured) | If git answered `rev-parse --absolute-git-dir`, the rollback of `git.worktree_create` deletes the registration only if the folder at its path is still the folder that the create accepted after the add. `89cb6289` removes the folder at that path in cells B6 and B6b (Linux and macOS VMs). The placement of the index has the same test (not measured). On Windows a rollback after `.git` became a file runs no git call (cells X1, X1b and X1c). Maintainer decisions of 2026-10-07 and 2026-10-19. The kept folder and that frame are not drift |
| D27 | Always-on, Windows only | Yes on a Windows run with a case-sensitive `.claude\worktrees` and a live worktree in another letter case. `5fd08069` deletes its entry and answers success (cell W-CS of round `task084-02`). claustrum keeps it and answers git's `worktree_add_failed` text (round `task084-03`) | the stale step keeps an entry whose record path exists. A maintainer decision of 2026-10-18. Not drift |
| D8 | Always-on | No. It is a start failure of the launcher or a file on disk, not a frame | a link, a FIFO, and on Linux and macOS another user's file, at `remote-server.log` fails the start with `permission denied`. `5fd08069` writes through a link or another user's file and blocks on a FIFO, on macOS and on Linux with the `fs.protected` sysctls off. On Windows it writes through a link (round `task015-04`). The `git-shadow` sweep makes no lock file in the target of a link |
| D16 | Always-on, Windows only | Yes on a Windows run. `git.status` of a linked worktree: the reference answers `exit status 128` when the user has no global excludes file, claustrum answers the status | the `core.excludesFile` of the `status`, `ls-files` and `diff-index` calls of `git.status` is `/dev/null`, not `NUL`. The reference stops at its `status` call there, so its value for the other two is not measured |
| CT-1 | Opt-in (`wantPid`) | Yes, on request. It adds `pid`/`startTime` | spawn/reattach reply extension |
| CT-2 | Opt-in (`-keep-children`) | No | children survive shutdown |
| CT-3 | Opt-in (`claustrum.conf`) | Only `version-override`, via the static check's `-version` diff | the configuration file itself |
| CT-4 | Not built. A deferred idea, recorded in DIVERGENCES.md only | No. There is no code | opt-in hardened token persistence (a `persist-token` key, or a Windows owner-only DACL) |
| CT-5 | Opt-in (`-listen-pipe`, Windows) | No | additional named-pipe transport |

D1, D7, D9, D11, D13, D14, D18, D21 and D25 are retired. The reference changed on the path, or a later
measurement corrected the premise, or the maintainer chose to follow the reference. claustrum now matches it. A difference on those paths is drift, not a divergence, unless
an entry in the table above covers it (for example D6, D10).
D4 is retired too. Its paths follow `5fd08069`, so a difference against
`89cb6289` there is expected. The paragraph on `files-read-regular-only` below
names the differences. D5 is retired too. The git calls follow the time limits
of `5fd08069`, so a stop of a slow git is expected. See
[DIVERGENCES.md → Retired entries](DIVERGENCES.md#retired-entries).

Check both indexes. The shipped ledger ([docs/IMPROVEMENTS.md](IMPROVEMENTS.md))
numbers several more claustrum-only behaviors, and they are just as real. They are
item 16 (`-metrics-addr`) and item 18 (`-token-fd`). Item 21 is another: claustrum skips the
kill signal for a child that already exited. Check both the divergence catalog and
the shipped ledger before you conclude that something is drift. `-wire-log` is
claustrum-only too, and it is in neither index: its detail is in
[PROTOCOL.md → Flags and config keys](PROTOCOL.md#flags-and-config-keys).

### Drift, or an activated opt-in? — the parse-behaviour table

An opt-in key that is *present* is not a deadline that is *in force*. A mistyped or
inert value leaves the divergence off, and the parity behavior that results reads
exactly like drift. When a symptom matches a D-shaped divergence on a stock
claustrum, check for the key or flag first. Then make sure that the value parses to
a positive duration. One knob takes a duration: D12 (`cli-download-timeout`),
which logs under `[Install]`. The deprecated `git-timeout`, `cli-probe-timeout`
and `libc-probe-timeout` set nothing, whatever their value. A flag value that does
not parse still exits 2, as in the table:

| Value shape | Configuration key in `claustrum.conf` | `-…` flag on the argv |
|-------------|--------------------------------|-----------------------|
| Unparseable (`= 15`) | Dropped silently, with no log. The run proceeds with no deadline | `flag.Duration` rejects it before any mode runs: `invalid value "15" for flag …: parse error` + usage, exit 2, no facts line |
| Negative (`= -1s`) | Dropped silently, with no log and no deadline | Normalized to 0 with an `[Install]` stderr warning, then runs normally (exit 0, facts line printed) |
| Any zero (`0`, `+0`, `-0`, `0s`, `-0m`, `-0.4ns`) | Accepted. It reads as opted-in but the deadline stays off | Accepted, with the deadline off |

The configuration path is silent, and that silence is the shape that looks like
drift. A missing `__INSTALL_RESULT__` does not by itself mean a malformed flag.
`-install` prints no facts line while it waits for a CLI that does not answer.
That wait ends at 30 s on a cache hit and at 120 s after an install. Through a
usable managed launcher it ends at 33 s and at 123 s. The
facts line then carries `"cliUnresponsive":true`. That is parity. Discriminate on
exit. Exit 2 with `parse error` on stderr is the bad flag. Exit 2 with
`cannot resolve home directory` on stderr is a missing home folder.

The deprecated `files-read-regular-only` (retired D4) sets nothing, whatever
its value. It is a bool, not a duration. Its flag forms parse like this:

- Flag `=value` form unrecognized (`-files-read-regular-only=maybe`): `flag.Bool`
  rejects it with usage + exit 2.
- Flag space form (`-files-read-regular-only maybe`): a bool does not consume
  the next arg. `maybe` becomes a positional and parsing stops. That silently
  drops every later flag.

A `-32602 files.read: not a regular file` on a stock claustrum is not drift. It
is the rule of `5fd08069`. Against `89cb6289` the same request differs: that
build reads the path, waits, or answers another error. An `open <path>: …` text
of `files.read` is not drift in these measured cases, where `89cb6289` has
another text:

- Linux and macOS, `stat <path>: …` on `89cb6289`: a symlink loop, a `/` or a
  further component after a regular file, a name that is too long, a NUL byte,
  and a folder that the daemon cannot search. The battery row for `files.read`
  of `~/a.txt/` shows it.
- Windows, `CreateFile <path>: …` on `89cb6289`: `CON`, `CONIN$`, `CONOUT$`, a
  `\` after a regular file, and a name that is too long. A NUL byte gets
  `Stat <path>: …` there.
- Each system, where the open fails: `89cb6289` answers `files.read: path is a
  directory` for a directory and `files.read: file exceeds maxBytes` for a file
  over the limit.

An `open <path>: resource temporarily unavailable` of `files.read` on a regular
file that another process holds under a lease is not drift either. From the
code, it comes from the `O_NONBLOCK` open of claustrum. No VM row has that
case.

### Triage gotchas — when a probe result is misleading

[docs/DIVERGENCES.md](DIVERGENCES.md) holds the full measurements. These are the
traps that matter for telling drift from expected:

- A git call that runs 2 minutes is stopped, and the reply carries `git took too
  long`. That is parity with `5fd08069`, not drift. A scan of ignored files that
  the limit of 10 minutes stopped answers `{"success":true}` with no copied file.
  See [PROTOCOL.md → Time limits of git calls](PROTOCOL.md#time-limits-of-git-calls).
- The failure frames of `git.worktree_create` differ between pins. In
  `f6010b97` the git text can start with git's graft-file deprecation `hint:` lines.
  In the add-failure frame the 512-byte cap can then cut the rest of the text.
  `90fca6e6` prints no hint. claustrum sets `GIT_GRAFT_FILE` too, and its light
  calls turn the hint off with `-c advice.graftFileDeprecated=false`. Its heavy
  calls print the hint on stderr, and no reply holds it. A hint in a frame
  of `f6010b97` and none in the frame of claustrum is therefore not drift. The
  texts of `5fd08069` have no hint either, and those of `89cb6289` have it (cell
  B2 of round `val-s3p2b1`, Linux, macOS and Windows VMs).
- In a repo with no `.claude/`, a `timeoutMs` that expires during the copy step
  splits the pins. `f6010b97` runs no copy `ls-files` and answers success.
  `90fca6e6` and claustrum answer `timeout` "after the checkout finished" and roll
  back. `89cb6289` answers like claustrum (the Linux timeout set of the whole-build
  check of issue 442). That difference is not drift.
- In two cells of `git.worktree_create` a folder stays on claustrum that
  `89cb6289` removes. In cell B6 it is an empty folder at the path of the new
  registration. In cell B6b it is the registration of a live sibling worktree
  that a wrapper moved to that path (Linux and macOS VMs). The frames are
  equal. A kept
  folder there is D24, not drift.
- The branch step of `git.worktree_remove` and of the create rollbacks splits the
  pins. claustrum follows `89cb6289`, the pin before `5fd08069`. `5fd08069` is
  not measured in these rows. Against `f6010b97`, `server.capabilities` differs by the
  `git.worktree_remove.unpushedGuard` and `launcher.managed` features and by the
  `launcher.resolve` method. On remove, every kept case of the
  branch step differs. claustrum keeps the branch and adds `"branchKept":true`, where
  `f6010b97` adds no member. In most of these cases `f6010b97` also deletes
  the branch. The kept classes, with their rows:
  - A commit that no other ref reaches: R02, R05 to R08, R10b, R17a, R19, R20, R24b
    and B2-04a. Also R21, R21b, B2-08 and B2-12, where `89cb6289` and claustrum keep
    a branch with the member, and `f6010b97` deletes both branches.
  - A lock file on the branch: R14. There `f6010b97` keeps the branch too.
  - 10001 or more heads: R15a and R15c.
  - A letter-case twin: R16 and R16b.
  - No repository at `baseRepo`: R18 and R18b. Neither side deletes anything there.
  - A `baseRepo` that does not exist: X3. Neither side deletes anything there.
  - for-each-ref stopped at the 60 s bound: R22a and B2-02.
  - rev-list stopped at that bound, or failed: B2-01 and B2-03.
  - update-ref stopped: R23a and R23b.

  In the create rollbacks, these rows differ from `f6010b97`: C02, C04, C05, C06, C06b,
  C09, C10, B2-09a, B2-09b, B2-09c, B2-09f and B2-10. There `f6010b97` deletes the
  branch and adds no branch text. Those differences are not drift. Rows C07a, C07b,
  B2-09e, X1 and X2 equal `f6010b97`. See [PROTOCOL.md](PROTOCOL.md) → The branch
  step.
- The managed launcher splits the pins the same way, not drift. claustrum follows
  `89cb6289`: `launcher.resolve`, the `process.spawn` `launcher` param, the child
  env strip, and the gated launcher runs of `-install` and `-probe-cli`. The one
  exception is the Linux file test on a cache hit before a launcher run. It
  follows `5fd08069` (see the `-install` prune and sweeps below). Other launcher
  runs of `5fd08069` are not measured. `f6010b97` has none of these. See [PROTOCOL.md](PROTOCOL.md) → launcher.*.
- The peer check splits the builds, not drift. claustrum follows `5fd08069`, the
  build that `scripts/UPSTREAM_SHA` names. `server.capabilities`
  carries the `peerCheck` and `peerCheckBy` members and the `server.peer_check`
  and `git.shadow_dir` features. On Linux the daemon refuses a caller in other
  namespaces with `-32006`. The frames of `89cb6289` have no such member and no
  such feature (Linux, macOS and Windows VMs). The daemon
  removes `CLAUDE_SSH_PEER_CHECK` from its own environment after the read, so
  its children do not inherit it. A `process.spawn` child of `5fd08069` has no
  such entry for the values `1` and `0` (Linux, macOS and Windows VMs). Its git
  calls have none for `0` (Linux VM). A child of `89cb6289` is not measured.
  Against `5fd08069` the measured peer check log lines differ by a level tag only
  (round `task011-02`, Linux VM). See [PROTOCOL.md](PROTOCOL.md) → server.*.
- `files.extract_tar` splits the builds too, not drift. claustrum follows the pin
  `5fd08069` in these places. Against
  `89cb6289` each of them differs in the measured rows:
  - `server.capabilities` lists `files.extract_tar.execBit` directly after
    `process.stdin.offset` (Linux, macOS and Windows VMs).
  - On Linux and macOS a file whose archive mode has the execute bit of the
    owner arrives with mode `0700`, not `0600`. The frame is equal.
  - A name that leaves `destDir` and comes back answers `unsafe path in
    archive: <name>`. `89cb6289` extracts `../dest/x.txt`, and it answers a
    `create` text for `sub/../../dest`.
  - A file that the archive names two times has the mode of the last entry
    (Linux and macOS VMs). On a volume that ignores letter case it has the name
    of the last entry (Windows and macOS VMs).
  - The texts after `mkdir parent <entry>:`, `mkdir <entry>:` and
    `create <entry>:` name the place below `destDir`, not the whole path.
  - A `destDir` that the daemon cannot search or open fails with
    `open destDir:`. `89cb6289` fails later, or it extracts.
  - The texts of the `destDir` steps name the last name of `destDir`, or the
    parent folder: `clean destDir: RemoveAll <name>: …` (Linux, macOS and
    Windows VMs), `open parent: open <path>: …` and `mkdir destDir: mkdirat
    <name>: …` (Linux and macOS VMs). `89cb6289` has a `clean destDir:` or
    `mkdir destDir:` text with the whole path in the rows where it fails.
  - With a parent folder of mode `0300` and no `destDir`, the answer is the
    `open parent:` text and no `destDir` is made. `89cb6289` extracts (Linux
    and macOS VMs).
  - A parent folder of mode `0400` or `0600` answers the `open parent:` text
    too, where `89cb6289` answers `clean destDir: openfdat <path>: …` (Linux
    and macOS VMs. The answer of claustrum is from a Linux VM).
  - On Windows, with no `\\?\` prefix, a `destDir` whose last name ends in a
    dot or a space is wiped and filled. `89cb6289` keeps the old file, or it
    fails (Windows VM).
  - On Windows the last names `NUL`, `a:b`, `CON`, `COM1`, `dest?` and `dest*`
    answer `clean destDir: RemoveAll <name>: …`. `89cb6289` answers another
    text, or it makes a folder (Windows VM).

  A folder above `destDir` that cannot be made answers `mkdir parent: mkdir
  <path>: …` on `89cb6289` and on `5fd08069` (Linux, macOS and Windows VMs). That is
  no difference against the pin.

  Against both builds these answers are not drift. On Windows a `destDir` with the
  `\\?\` prefix and a dot or a space after its last name answers
  `clean destDir: "<name>" is not the entry that the path names`, or the same
  text after `mkdir destDir:`, and nothing is deleted. `89cb6289` extracts into
  a folder with the exact name, and `5fd08069` into the folder with no dot or
  space (Windows VM). A `destDir` whose last name is the home folder by
  identity gets the home refusal. See [D2](DIVERGENCES.md#d2).
- The `-install` prune and sweeps split the builds too, not drift. They follow
  the pin `5fd08069`, not `89cb6289`. Folders and links stay and take no
  keep place. `x.zst`, `.fetch-d`, `p.zst.part` and empty folders stay in the
  sweeps. A `.blob-` file is not counted, a negative `-cli-keep` exits `0`, and
  the new CLI stays with `-cli-keep 0`. The blob timing, the `rename: ` prefix,
  the Windows rename retry and the Linux file test of a cache hit follow `5fd08069` too
  ([PROTOCOL.md](PROTOCOL.md) → `-install`). A probe against `89cb6289` shows these
  as a build split.
- The git filters of the `git.worktree_create` checkout split the builds too,
  not drift. claustrum follows the pin `5fd08069`. In the state `fallback-git`
  and with `"shadowDirOff":true`
  claustrum has no filter rule, as `5fd08069` (cells R-Q4f and R-Q4g of round `task084-01`, Linux VM). With
  `shadowDirOff` the rule is off, as before. Against
  `89cb6289` these answers differ:
  - A checkout that needs a filter which only the repository config defines
    answers `errorCode:"repository_only_filter"`, and nothing is left.
    `89cb6289` runs the filter (Linux and Windows VMs).
  - With a filter in the repository config and in a config outside it, the
    outside filter runs. `89cb6289` runs the one of the repository
    (Linux VM). A user `process = cat` with a repository `smudge` command
    answers `repository_only_filter`, as on `5fd08069`.
  - A filter that only the repository defines, with a name of 300 bytes,
    answers `worktree_add_failed` with the text about a name longer than 200
    bytes (Linux and Windows VMs).
  - With a settings copy, a filter that only the repository defines, with a
    name that is not valid UTF-8 and a value that does not pass, answers git's
    checkout text on Linux and success with no filter run on Windows, and
    `89cb6289` runs the filter (Windows VM). With no copy it answers the text
    about values that no option of git can replace (claustrum's own).
  - In the state `shadow`, more than 64 filter names, and a last config listing
    before the checkout that fails, or whose git still runs at 30 s and does not
    exit 0 after the SIGTERM, fail the create (claustrum's code, `89cb6289` not
    run).

  Against `5fd08069` these differences are not drift:
  - With a `GIT_DIR` and a `GIT_WORK_TREE` of another repository in the daemon
    environment, `5fd08069` answers the filter refusal. claustrum answers
    `not_a_repo`, as `89cb6289` does (Linux VM).
  - A `~/.gitconfig` that is a directory has another text on `5fd08069`.
    claustrum has the text of `89cb6289` (Linux VM).
  - With a temporary directory that cannot be used, `5fd08069` answers the
    filter refusal. claustrum fails as `89cb6289` does (Linux and Windows
    VMs).
  - With a program key of git-lfs in the config of the repository and no
    settings copy, the filter `lfs` does not run on claustrum. A create with a
    file under it answers the filter refusal for `lfs`, or claustrum's own
    `worktree_add_failed` text that names the key. `5fd08069` is not measured
    in that state.
  - In the call log, some `filter.` pairs differ.
- The refusals of a repository config split the builds too, not drift.
  Against `89cb6289` a config that the daemon does not accept gets a text that
  starts with `this repository's git config cannot be used: `. A refused file of
  the git directory gets a text that starts with `a file in this repository's
  .git directory cannot be used: `. `89cb6289` gives a normal answer, `isRepo`
  false, the hooks refusal, another refusal of that build such as `exit status
  128`, or no answer there. The call log has one more call
  for each config file of a first read. Each state but the three Windows texts
  ran on a Linux VM, and part of them on macOS and Windows VMs. Most cells ran
  `git.info` and `git.status`.
  With `"shadowDirOff":true` in the request, or while such a request runs (Linux
  VM), the read does not run. A
  `git.worktree_remove` of a worktree folder that does not exist is refused
  too. With a config of 20 MiB, `89cb6289` deletes the branch there (Linux,
  macOS and Windows VMs). With `worktreeRoot` the refused remove has the text
  `cannot determine the repository's work tree: ` before the refusal (Linux
  and macOS VMs).

  Against `5fd08069` these differences are not drift. On a config that changes many
  times each second, `5fd08069` makes more config reads than claustrum (Linux
  VM). For a config with a very long key, `5fd08069` ends config read calls
  early, and claustrum lets them end (Windows VM). A config whose listing by git
  passes 64 MiB can get no refusal on claustrum (from the code, not measured on
  `5fd08069`).

  The bounds are in
  [PROTOCOL.md → Repository config refusals](PROTOCOL.md#repository-config-refusals).
- The option lists of the git calls split the builds too, not drift.
  claustrum differs from `89cb6289` in four states: `git.status` with replace
  objects, the `hint:` lines in a failure text of `git.worktree_create`, a long
  tree path on Windows, and the paths of a new worktree with
  `worktree.useRelativePaths`. The call logs differ in the `-c` lists and in two
  variables. The cells and the systems are in
  [Hardened git calls](PROTOCOL.md#hardened-git-calls).

  Against `5fd08069` the call logs differ in `git.worktree_create`. `5fd08069`
  makes more rounds with `worktreeRoot`, and other listings with `existingBranch`. Those differences are not drift. See "Not equal
  to `5fd08069`" in [PROTOCOL.md](PROTOCOL.md#hardened-git-calls).
- The `worktreeRoot` location rules follow `5fd08069` (rounds `tier0-20` to
  `tier0-22`), not drift. `89cb6289` is not measured in these cells.
- A remove behind a linked `<git dir>/worktrees` follows `5fd08069`, not drift:
  cell C3 of rounds `tier0-24` and `tier0-25m` (Linux and macOS VMs) and L1 of
  round `tier0-26` (Linux VM). `89cb6289` is not measured.
- The settings copy of the git methods splits the builds too, not drift.
  Against `89cb6289` a triager can meet these points. Each is in
  [The settings copy](PROTOCOL.md#the-settings-copy):
  - `git.info` has a last member `settingsControl`.
  - The run folder has a folder `git-shadow`.
  - A first git request makes 12 more git calls.
  - These texts are new. They start with
    `Claude could not test git on the remote machine`,
    `git could not check this repository on the remote machine`,
    `git and Claude disagree about which repository holds this folder` and
    `Claude could not prepare its own files on the remote machine`. A create
    also answers `cannot list the repository's worktree entries`,
    `cannot prepare the repository's worktrees directory`,
    `git worktree add failed (completing the new worktree's git files)` and
    `".git" is a link or a file where Claude needs a real directory`.
  - On Windows each git method refuses a junction or a directory symbolic link
    at `.git` with the link text, without `"shadowDirOff":true`.
  - A config value that git refuses gets a normal answer where the copy leaves
    it out.
  - On Windows a create removes old `.claude-new-worktree-*` entries of the git
    directory. See [git.worktree_create](protocol/git-worktree-create.md#the-settings-copy).

  Against `5fd08069` these differences are not drift:
  - The config reads on a config that changes many times each second, and the
    config reads that `5fd08069` ends early.
  - On Windows, two reply texts of a create whose `.git` becomes a file
    `gitdir: <X>` while the create runs (Windows VM).
- A remove with a worktree folder that exists lists again after a failed listing.
  It answers the text of the failed listing or goes on (`5fd08069`, rounds
  `tier3-13`, `tier3-17` and `tier3-19`). `f6010b97` answers the lock-check text
  (row G08). In round `tier3-17` (Linux VM) claustrum makes one more listing and
  `rev-parse` in cells CaP0 and FR with equal replies, and in cell CaP3 both answer
  the colon form where `89cb6289` answered the lock-check text.
- The reap of a child record with a `program` key splits the pins too, off the
  wire. claustrum follows `89cb6289`. `5fd08069` is not measured in these rows.
  On Linux, if the process runs `program` or
  holds it as one whole argument, `89cb6289` accepts the record. Linux rows PG02a, PG02b,
  ED03a, ED03b, ED04a and ED04e show that. `f6010b97` sends no signal in rows ED03a
  to ED04e. That difference is not drift. See
  [PROTOCOL.md → Child records and the reap](PROTOCOL.md#child-records-and-the-reap-linux-and-macos).
- The configuration listing, the stray `commondir` rules and the `git.info` root
  split the pins too. claustrum follows `89cb6289` there. `5fd08069` is not
  measured in these rows. In the state `shadow` the answers of
  [The settings copy](PROTOCOL.md#the-settings-copy) come first. Against
  `f6010b97` these differences are not drift:
  - `server.capabilities` carries `git.info.discovered_root`.
  - A stray `commondir` that reads `.` or `./` is served when its git directory is
    valid (rows T01, T02 and T11). With a bad `HEAD` it gets S3 (row T10a). A
    dangling relative symlink that stays in a valid git directory counts as none
    (row T07c). With `worktreeRoot`, `git.worktree_remove` then answers `cannot list
    the repository's worktrees: exit status 128` (row DG2s-g). Without
    `worktreeRoot` it answers `{"success":true,"branchKept":true}` and keeps the
    branch. It deletes the entry. If the worktree exists, it deletes that too (rows
    DG3-g and DG3x-g). `f6010b97` refuses there and keeps everything. With a bad `HEAD` the
    symlink gets S3 too (row DG2-g). Any other content or file gets S1 or S2. In all
    these rows but DG3-g, `f6010b97` gives one older text: `%q exists where git
    itself never writes one (git keeps that file only in a linked worktree's entry
    under .git/worktrees/); remove it if you did not create it, and treat its
    appearance as tampering`. In row DG3-g it answers the lock-check text with `its
    registrations could not be examined`.
  - A listing error is in English under a German daemon locale (row L03).
  - A configuration key of 1048576 bytes passes, and one byte more gets the key
    text. `f6010b97` refuses both with `bufio.Scanner: token too long` (rows L08b
    and L08c).
  - A listing that exits 128 with `fatal: not a git repository` answers the "no
    repository" shapes, and `git.worktree_remove` with `worktreeRoot` gives `git
    finds no repository here: …`. `f6010b97` gives the hooks refusal (rows L14a,
    L14b, L14d, L14e and N01 to N04).
  - When `git version` also fails, the text says git cannot run. `f6010b97` gives
    the hooks refusal, or the "no repository" shape for a git that cannot start
    (rows L11, L12 and L15).
  - The `git.info` root comes from the walk, so a daemon `GIT_DIR` with a plain
    folder answers the non-repo body (rows I04a and I04b). The slug after `lnk/..`
    is the one of the resolved repository (row D01). On Windows the daemon does not
    take a toplevel answer that names another folder or is relative (rows W14 and
    W16). A symlink there resolves before `..` (row D03).
  - claustrum keeps D16 as it is. `git.status` now answers by the worktree
    entries of `baseRepo`, as `89cb6289` answers, so the `.git` inside `path` plays no
    part ([git.status](protocol/git-status.md#path)).

  See [PROTOCOL.md](PROTOCOL.md) → Git-directory trust check, Hardened git calls
  and [git.info → The root](protocol/git-info.md#the-root).
- D12 needs a VALID zstd body. The decoder answers an invalid one at 0 s,
  which reads like "no divergence". Also, a zero download timeout frees the body
  read only: the transport still applies `net.Dialer{Timeout: 30s}`,
  `TLSHandshakeTimeout: 10s` and the 60 s response-header limit. That last limit
  is parity.
- A connection reset in the body answers
  `download interrupted after <got>/<total> bytes: <err>` on both binaries
  (measured on Linux against `f6010b97` and `89cb6289`). A different text there
  is drift. A body that ends before its length is not measured, so do not call a
  difference there drift.
- A download that is not a zstd archive answers `decompressing: …` with
  `fetch.bytes` 4 and one progress line. That holds with a right and a wrong
  checksum (`5fd08069`, cells L15a and L15b, Linux VM). For such a body,
  another count is drift.
- The `ldd` libc probe bounds ldd itself at 5 s on both binaries (parity since
  `19f30c46`). There is no libc probe off linux at all.
- claustrum made seven changes for `90fca6e6` while the JSON-RPC surface stood still.
  A triager who meets one of them and finds the drift check quiet is looking at
  parity, not drift. No method, field or error string moved. Three of the seven
  still change what a client reads. A reattach now closes the connection it
  replaces. A reaped process reads as not running for `process.stdin` and
  `process.reattach` inside the exit drain. A new worktree no longer inherits the
  nine Claude runtime-state names under `.claude/`. That last one is the likeliest
  to be misread. `19f30c46` copies eight of the nine, so a file missing from a
  seeded worktree looks like a regression against the previous pin.
  [REFERENCE-BUILDS.md](REFERENCE-BUILDS.md) holds all seven, and all seven are
  reconciled. On the host cleaner's macOS busy
  probe, the daemon gets no SIGTERM after an `lsof` run that claustrum gave up on,
  as on `5fd08069` (round `tier4-01`, macOS VM). The retired D17 covered it. A macOS
  pass with three hung `lsof` runs and 51 s is the known difference from `5fd08069`,
  which runs one and ends in 17 s. It is not drift. For the busy read and the lock read, an `lsof`
  command that does not start is parity with `89cb6289` (macOS VM, 3 of 3 runs).
  So is a run that writes to stderr and exits 1.
- The `90fca6e6` reconciliation shipped four PRs. A full re-check then found a
  seventh change that the first pass had missed. A new value inside an existing
  function shows in no symbol list, so a quiet symbol comparison does not prove
  that nothing changed. Re-check the whole build after the slices merge.

- A reference daemon that stays alive after its socket path is gone is not drift.
  On a Linux VM, the host cleaner of `f6010b97` and `89cb6289` sent no signal to
  such a daemon. It sent none to its children (rows ST01, ST02, ST04 to ST10, HC16 to
  HC19). The daemon ended by itself after 11 to 12 minutes (rows ST09 and ST11). The
  cleaner of claustrum sends none either. On a macOS VM the two references sent none
  either (rows ST01, ST09, ST10, HC16, HC18).
- On Windows a serving process that outlives its SSH session is not drift, and
  children that outlive a killed daemon are not drift. A Windows VM measured both
  against `f6010b97` and `89cb6289` (rows WJ01 to WJ07 and WN03, and row WJ08 on
  `89cb6289`). A later run on that VM measured the same on claustrum (rows WJ01 to
  WJ08, WN03). A spawned child is in no job on either side, and a kill ends the direct
  child only (kill forms: `89cb6289`). Descendants that outlive a kill or a `-stop` are not drift.
- A daemon that accepts no connection for some seconds after its start is not
  drift. On Linux and macOS the daemon binds its socket after the reap of its
  start. With a recorded child that ignored `SIGTERM`, the first connection to
  `89cb6289` worked 2.2 to 2.4 s after its start (Linux and macOS VMs). Before
  that a client got `ECONNREFUSED` (row D1a) or `ENOENT` (row D1b). In that
  time `daemon.lock` holds `pid`, `role` and `node` only. claustrum is built to
  the same order.
- Child records and both markers on a socket that is not `run/<id>/rpc.sock` are
  not drift. `89cb6289` writes them on every socket shape (Linux and macOS rows F1
  to F4), and claustrum does too.
- Records that stay after a daemon start are not drift in two cases. One start
  handles 128 record names (Linux row C1) and ends 64 groups (Linux row C4), on
  `89cb6289`. claustrum is built to it. The rest goes at a later start.
- A first process that lives on after a second `process.spawn` took its `id` is
  not drift. `89cb6289` sends it no signal, and a stop of the daemon leaves it
  alive. Linux and macOS rows A1 to A4 and Windows rows EV and EVInh measure that.
  The frames of that first process still come on its own
  connection, under the same `processId` (Linux row P2 against `89cb6289`).
  claustrum is built to it.
- A `process.spawn` reply that comes 3 to 5 s late is not drift when the spawn
  supersedes a session process. `89cb6289` answers after the old process ended
  (Linux and macOS rows B1 to B3, Windows rows SS and SSInh). claustrum does the
  same.
- An old session process that lives on after a failed session spawn is not drift
  in one case: the command of the new spawn does not exist. `89cb6289` sends the
  old process no signal there (Linux rows P3, H1 and H9, macOS row P3, Windows
  rows SSm-path, SSm-bare and SSm-noext). claustrum is built to it. A command
  that exists and does not start ends the old process first, on `89cb6289` and
  on claustrum (Linux rows H4a to H4c).
- A descendant that lives on after `process.killAndWait` or a session supersede
  is not drift in these cases. One is a process that ended on the graceful
  signal within the grace. `89cb6289` then sends its group no `SIGKILL` (Linux rows X2a and X2b,
  also B1 and H4a to H4c). claustrum is built to it. When the grace runs out,
  both send the group `SIGKILL` (Linux rows B2, B3 and X1). macOS rows X2a to
  X2c give the same. Windows has no X2 row. The holder of the pipes also lives on,
  with equal answers on `5fd08069`, in cells K1b, K1c and K2b (rounds `tier3-03`
  and `tier3-12`, macOS and Linux VMs).
- A daemon that runs no login shell before its first `process.spawn` is not drift
  (Linux and macOS rows G1 and G2 against `89cb6289`).
- A first spawn that answers after 4.6 s with a timed-out PATH line is not drift
  when a process of the login profile holds the output pipe. `89cb6289` does that
  (Linux rows P13 and P13k), and claustrum is built to it.
- A `-serve` launcher that gives up after 12 s is not drift. If the daemon child
  of `89cb6289` ends before the bind, its launcher exits 1 after 12.06 s (Linux
  row P7, macOS row P7 and two Windows rows).
- A daemon that gets `SIGTERM` or `SIGINT` during its start, goes on to its bind
  and then exits 0 is not drift. `89cb6289` does that (Linux rows P5, H8a to H8c,
  N4 and N5, macOS row P5), and claustrum is built to it. Its launcher then exits
  1 after 12 s (rows N4 and N5).
- A daemon that gets `SIGKILL` from a second daemon during its start is not
  drift. `89cb6289` does that when the two starts are 0.05 s apart (Linux row N3).
  The second daemon waits on the lock, and claustrum is built to it (row H7).
- The order of two or more new caller keys in a child environment is not drift.
  `89cb6289` gave three rotations of the request order in 20 spawns (Linux rows
  P9 and N7). claustrum iterates the decoded map and builds no order of its own.
  On Windows see D23.
- A record or a marker with two blanks in its macOS start text is not accepted.
  An older claustrum build wrote that form. `89cb6289` and claustrum
  drop such a record and send no signal (macOS rows TBr and TBe). A child of such
  an older build is therefore not ended at the next start.
- A child of a live daemon that a new daemon ends on macOS is not drift in one
  case: the `daemonStart` of its record holds two blanks. `89cb6289` and claustrum
  then read that daemon as gone (macOS row N6).
- These differences on Linux and macOS are known and open. They are not drift.
    - At the reap of a start, `89cb6289` probes a group right after its `SIGTERM`.
      If the group still answers, it sends `SIGKILL` (rows K5, P8k, P8b and C1).
      claustrum probes after 50 ms. That is [DIVERGENCES.md](DIVERGENCES.md) D20.
    - claustrum logs an exit line for a child that a shutdown kills:
      `Process <id> exited with code -1, terminated by SIGKILL, signalled at shutdown request`.
      `89cb6289` logs none (Linux row E3, macOS row A3). `5fd08069` wrote it on
      Linux, not on macOS (round `task003-01`).
    - After a `-stop` during the wait of a supersede, claustrum can log the exit
      line of the old process and the `supersedes` line. `89cb6289` logged neither
      (row P1: Linux 1 of 6 runs against 0 of 7, macOS 2 of 2 against 0 of 2).
    - `89cb6289` calls unlink on one child record up to three times at a stop or
      a kill. claustrum calls it once. `89cb6289` also calls unlink on a record
      that it never wrote (Linux rows P1, B1, A3, P11a). The files left are the
      same.
    - On macOS `89cb6289` runs one `ps` at the daemon start and one per spawn.
      claustrum runs none at the start and three per spawn (macOS rows G1 and G2).
    - With a zero-byte `-token-file` the daemon log of `89cb6289` holds
      `claude-ssh: empty token in --token-file`. claustrum logs
      `claustrum: token is empty` (row P7 on Linux and macOS, and two Windows
      rows).
    - When a frame cannot reach a closed connection, `89cb6289` logs
      `[frameSink] write failed, detaching: write unix <socket>->@: use of closed network connection`.
      claustrum logs `[frameSink] write failed, detaching: use of closed network connection`
      (Linux row N2c).
- One difference on Windows is known and open. It is not drift.
    - At a stop with one connection, `89cb6289` logged `closed 0 connection(s)`
      in 2 of 9 runs. claustrum logged `closed 1` in 9 of 9. The rate is not
      measured.
- A child that `process.spawn` starts as a shutdown begins is known, not drift.
  `5fd08069` left some running (round `task003-01`: Linux 64, macOS 1081,
  Windows 2). claustrum ends them (round `task003-04`: 0 left).
- A child environment block in name order on Windows is not drift. `f6010b97`
  and `89cb6289` keep the order of the daemon, then `CLAUDE_SSH_DAEMON_CHILD=1`
  (rows WN01a, WN01b). On `89cb6289` an added `PATH` entry and new caller keys
  come after it (Windows VM rows V2, V5, V9 and V10). The stdout frame of
  `cmd /c set` therefore differs in bytes. That is
  [DIVERGENCES.md](DIVERGENCES.md) D23.
- The log lines of claustrum keep its level tag and the name `Claustrum` in the
  listening line. Both differ from the reference by design.

## Toolchain-induced drift — the Go 1.27 `jsonv2` hold

Reference drift is not the only way the wire can move. claustrum inherits some of
its frame bytes straight from Go's `encoding/json` (see
[docs/ARCHITECTURE.md → "Inherited wire bytes"](ARCHITECTURE.md#inherited-wire-bytes) and
`inherited_encoding_test.go`). A change in the Go toolchain claustrum is
built with can therefore move those bytes. The reference does not have to change
at all.

Go 1.27 does exactly that. It enables the `jsonv2` GOEXPERIMENT by default
(`internal/buildcfg/exp.go` sets `JSONv2: true`), which reimplements
`encoding/json`'s `Marshal` on the v2 engine. That engine renders an invalid
UTF-8 byte as the literal U+FFFD character (bytes `EF BF BD`). Go 1.26
and earlier emit the six-ASCII
`\ufffd` escape, and so did the reference daemon at `5db5e4a`. The build pinned
today, `5fd08069`, carries a go1.25.14 stamp (`go version` on the binary), as
`89cb6289` does. So it emits the same escape. `files.read`
puts raw file bytes into the `content` string. Under Go 1.27, every read of a file
holding a non-UTF-8 byte therefore diverges from the
reference. `TestInheritedInvalidUTF8BecomesReplacementChar` and
`TestInheritedLoneSurrogateBecomesThreeReplacements` fail under 1.27 for this
reason. That is the guard working as intended, not a stale expectation.

Current stance: hold the build toolchain at 1.26.x. The hold lives in the
Renovate preset (`schubydoo/renovate-config` → `claustrum.json`,
`allowedVersions: <1.27` on the `go` dep). 1.26.x patch and security bumps
still flow, while 1.27+ is blocked. That preset constrains `go.mod`. The hold
therefore follows a build path automatically only where that path resolves its
toolchain from `go.mod`. A `setup-go` step naming its own version sits outside it.
An explicit `go-version: 1.26.7` still honors the hold but stops tracking it, and
`go-version: stable` leaves it altogether. Nothing in this repo detects that.
No lint, no guard and no test reads `setup-go` inputs. The first signal is
therefore a failing run, at whatever cadence that workflow runs. `mutation.yml` used
`go-version: stable`, and it is a weekly cron. Go 1.27.0 went stable, and the
break surfaced up to a week later, on the 2026-08-24 scheduled run. It surfaced
again on 2026-08-31 (run 33387062009) before it was diagnosed. All eight `setup-go`
steps now use `go-version-file: go.mod`.
`GOEXPERIMENT=nojsonv2` restores the `\ufffd`
escape under a 1.27 toolchain (measured). It is therefore the compensating knob for
a future constraint that forces the build onto 1.27 before the trigger below fires.

Revisit trigger: if the reference daemon itself rebuilds on Go 1.27, it flips
to the literal U+FFFD too. At that point claustrum must follow, not resist.
Drop the toolchain hold, and flip the two guard-test expectations to the literal
form. Until a drift check (Steps 2–3) shows that the reference moved, holding at
1.26.x is the parity-preserving position. This is a temporary hold, not a
divergence, and it carries no D-number.

### The sort of the Windows environment block

Since Go 1.26, `createEnvBlock` in `syscall` sorts the environment block of a
child by name on Windows (`envSorted`, `src/syscall/exec_windows.go`). That sort
is the cause of divergence [D23](DIVERGENCES.md#d23). If a Go release changes or
removes the sort, the order of the block changes with it. Read the D23 entry
again after each Go bump.

### The symlink-loop text of `filepath.EvalSymlinks`

Both worktree methods refuse a symlink loop in a `worktreeRoot`
(`locationLinkRefusal`). A kernel ELOOP catches a long chain. A root that is itself a link in a
loop, or starts a chain of more than 255 links, rests on the text "EvalSymlinks: too many links" of `filepath.EvalSymlinks`
(`isSymlinkLoop`). If a Go release rewords it, that
frame changes. The S1, S1b, S1c and S1d cases in
`worktree_create_chain_unix_test.go` then fail. Check them after each Go bump.

### Windows junctions in `filepath.EvalSymlinks`

Two refusals rest on a failed `filepath.EvalSymlinks`. One is the trust-root test
of the worktree methods (`baseRepoWalkFails`, `worktreecontain.go`). The other is
`unresolvable` (`methods_git.go`), which `git.status` applies to `baseRepo` and
`git.list_branches` to `path`. On Windows both depend on the `winsymlink` GODEBUG
setting, which Go 1.23 turned on. With it on, `os.Lstat` reports a junction as
neither a symbolic link nor a directory. With it off, a junction is a symbolic link.
With it on, the walk of `EvalSymlinks` fails at a junction before the last
component. claustrum answers Windows VM rows J1 and J3 with the trust-root refusal
for that reason. With `GODEBUG=winsymlink=0` in the daemon environment, the walk
follows the junction instead. claustrum's answers to rows J1 and J3 then flip, and
so do the `git.status` and `git.list_branches` answers for `<dir>\<junction>\T`.
Other code resolves `baseRepo` through `EvalSymlinks` too, so other Windows junction
rows can move as well. A `go` line below 1.23 in `go.mod` has the same effect. The
`git.info` root for a path with a junction before its last component rests on the
same failure (rows D03-junction and W09, `TestInfoRootLinkThenDotDotWindows` and `TestInfoRootBelowJunctionWindows`). With the walk following the junction, those
roots can move too. The `GIT_COMMON_DIR` pin of the trust check for such a path
rests on it as well (`walkStart`, `gitdirtrust.go`). With the walk following the
junction, the pin names the repository behind the junction. The `git.info` answers
for such a path then change: `root`, `repo` and `repoSlug` can move to the
repository behind the junction ([git.info → Differences by system](protocol/git-info.md#differences-by-system)).
The frames of `89cb6289` are pinned in `TestInfoThroughJunctionWindows`
(`gitinfo_junction_windows_test.go`), and the pins of the cells are in
[PROTOCOL.md → Git-directory trust check](PROTOCOL.md#git-directory-trust-check). This
entry is derived from the Go source (`os/types_windows.go` and
`path/filepath/symlink.go`), not measured. Check those rows after each Go bump that
changes `winsymlink`.

### The git version under the checkout tests

With a `worktreeRoot`, both worktree methods run `git worktree list --porcelain -z`
(`worktreeListCall`, `worktreeexternal.go`). The `-z` option needs git 2.36 or
later. On an older git that call fails. Both methods then run `git worktree list --porcelain` (`worktreeListTwice`). The references on such
a git are not measured. Several tests in
`worktree_root_checkout_unix_test.go` expect a working call. They skip there
(`requireWorktreeListZ`), so a skip in CI means a git older than 2.36.
Check them after the git of a CI runner or a test VM changes.

## Automating it

- [`.github/workflows/upstream-desktop-watch.yml`](https://github.com/schubydoo/claustrum/blob/main/.github/workflows/upstream-desktop-watch.yml)
  runs twice daily (cron `17 6,18 * * *`). It runs one leg for each of Linux,
  Windows and macOS. Each leg calls `scripts/latest-desktop-sha.py --platform <p>`
  to find the SHA that the *newest* Claude Desktop for that platform pins. That is
  Step 1, automated, and it needs no out-of-band source. A new pin must
  meet two tests. It differs from `scripts/UPSTREAM_SHA`, and no build heading in
  [`REFERENCE-BUILDS.md`](REFERENCE-BUILDS.md) (a "### `<full sha>`" line)
  matches it. A SHA that only the prose mentions does not count. A lagging platform
  that still pins an older, reconciled build therefore stays quiet. A new pin on
  any platform makes that leg run `check-upstream.sh <sha>` for the static drift
  diff and open a single idempotent tracking issue. If that issue is still open,
  a later leg that finds the same SHA adds a comment to it. A leg skips all
  downloads for a Desktop version that it already analyzed. Reconciliation
  (Step 4) stays a human decision.
- The platforms ship on separate schedules, so one platform can pin a newer
  reference build than the others. On 2026-09-24, Linux Desktop 2.7032.0 pinned
  `90fca6e6…`, and Windows and macOS Desktop 2.9939.2 pinned `f6010b97…`. A
  Linux-only watcher did not see that new build. For Linux, the script reads the
  APT `Packages` index (`.deb`). For Windows, it reads the Squirrel `RELEASES`
  file for win32/x64 (`-full.nupkg`). For macOS, it reads `RELEASES.json` for
  darwin/universal (`.zip`). The script checks the `.deb` by SHA-256 and the
  `.nupkg` by SHA-1 and size. The macOS feed publishes no checksum, so TLS is
  the only integrity check on that download. The script therefore refuses a
  URL or a redirect that is not HTTPS, for every feed and package.
- You can still run `check-upstream.sh` by hand against any SHA. Examples are a
  SHA you just found, or a check that a re-published build did not shift.
