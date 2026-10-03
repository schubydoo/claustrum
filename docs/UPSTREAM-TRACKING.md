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
now well behind the current baseline, `89cb6289`. It used a real Desktop session and
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
`battery.js`) shows a diff. The install-path entries (D10, D12 and D13) run under
`-install`, which the frame battery never drives at all.

| ID | Default | Battery-visible? | What it is |
|----|---------|------------------|------------|
| D3 | Off (`0` = unlimited) | No. Off the default path | `files.extract_tar` size cap |
| D4 | Off | Yes. `battery.js` id 70 reads `/dev/null`. No diff at the default (guard off), and it turns red once armed | `files.read` regular-file guard |
| D5 | Off (`0` = no deadline) | No. Off the default path | git-invocation deadline |
| D10 | Off (`0` = unlimited) | No. Install path | `-install` CLI size cap |
| D12 | Off (`0` = no bound) | No. Install path | `-install` download bound |
| D2 | Always-on | Maybe. A probe that reaches the path shows it (expected) | destructive-path home-dir refusal. On `-install` it is the `cli path must not be or contain the home directory` text. That refusal is this guard, not drift |
| D6 | Always-on | Maybe. A probe that reaches the path shows it (expected) | `-cli-version` single path component |
| D18 | Always-on | Maybe. A probe that reaches the path shows it (expected) | `-cli-version` must not start with `.blob-` |
| D19 | Always-on, Windows only | Maybe. A Windows probe with a junction at `.claude` or `.claude\worktrees` shows it (expected) | `git.worktree_remove` refuses that junction, where `f6010b97` answers success and deletes the branch, and `89cb6289` does the same for a branch that another ref reaches |
| D20 | Always-on, Linux and macOS | No. Off the wire. It is a signal time at a daemon start, not a frame | 50 ms settle before the group `SIGKILL` of a leader that reads as gone. In row RP05a that `SIGKILL` comes about 50 ms later than on `89cb6289`. In row PG05a claustrum ends the child in no run, where `89cb6289` ends it in some Linux runs. In rows where the child ends on `SIGTERM`, `89cb6289` also sends a group `SIGKILL` right after it in some Linux runs, and claustrum sends none. None of these is drift |
| D21 | Always-on, Windows only | No. Off the wire. It is the content of `remote-server.log`, not a frame | a second daemon on a live socket appends to `remote-server.log`. `89cb6289` truncates the file at that start. The earlier lines of its first daemon are lost. The later lines of that daemon sit behind a block of NUL bytes (rows WN04, WJ04). Keeping the lines is a maintainer decision of 2026-10-02. The longer log of claustrum is not drift |
| D22 | Always-on | Maybe. A probe that reaches the path shows it (expected) | `git.worktree_remove` refuses a worktree that is locked in the `.git` folder of `baseRepo`. `89cb6289` answers success in four states. Rows p6 and p6f have a daemon `GIT_DIR` and `GIT_COMMON_DIR` of another repository, with the folder present and gone. Row p6e has a daemon `GIT_DIR` alone. Row p6d has `baseRepo` = `<T>/missing/..`. Rows p6, p6e and p6f ran on Linux, macOS and Windows VMs, row p6d on Linux and macOS. A maintainer decision of 2026-10-03. The refusal is not drift |
| D8 | Always-on | No. It falls back to inherited stdio, not a frame | foreign/symlinked `remote-server.log` not followed (`.old` rotation matched, refuse-to-follow kept). Linux and macOS. On Windows see D21 |
| D9 | Always-on | Maybe. A type-mismatched namespace field is rejected | namespace-param binding vs. the reference's ignore |
| D13 | Always-on (unresolved in DIVERGENCES.md) | No. Install path | verify-before-decompress ordering, on `-cli-url` and on `-cli-zst` with a checksum |
| D16 | Always-on, Windows only | Yes on a Windows run. `git.status` of a linked worktree: the reference answers `exit status 128` when the user has no global excludes file, claustrum answers the status | the `core.excludesFile` of the `status`, `ls-files` and `diff-index` calls of `git.status` is `/dev/null`, not `NUL`. The reference stops at its `status` call there, so its value for the other two is not measured |
| CT-1 | Opt-in (`wantPid`) | Yes, on request. It adds `pid`/`startTime` | spawn/reattach reply extension |
| CT-2 | Opt-in (`-keep-children`) | No | children survive shutdown |
| CT-3 | Opt-in (`claustrum.conf`) | Only `version-override`, via the static check's `-version` diff | the configuration file itself |
| CT-4 | Not built. A deferred idea, recorded in DIVERGENCES.md only | No. There is no code | opt-in hardened token persistence (a `persist-token` key, or a Windows owner-only DACL) |
| CT-5 | Opt-in (`-listen-pipe`, Windows) | No | additional named-pipe transport |

D1, D7, D11 and D14 are retired. The reference changed on the path, or a later
measurement corrected the premise, and claustrum now matches it. A difference on those paths is drift, not a divergence, unless
an entry in the table above covers it (for example D6, D10, D13, D18). See
[DIVERGENCES.md → Retired entries](DIVERGENCES.md#retired-entries).

Check both indexes. The shipped ledger ([docs/IMPROVEMENTS.md](IMPROVEMENTS.md))
numbers several more claustrum-only behaviors, and they are just as real. They are
item 16 (`-metrics-addr`) and item 18 (`-token-fd`). Item 21 is another: claustrum skips the
kill signal for a child that already exited. Check both the divergence catalog and
the shipped ledger before you conclude that something is drift.

### Drift, or an activated opt-in? — the parse-behaviour table

An opt-in key that is *present* is not a deadline that is *in force*. A mistyped or
inert value leaves the divergence off, and the parity behavior that results reads
exactly like drift. When a symptom matches a D-shaped divergence on a stock
claustrum, check for the key or flag first. Then make sure that the value parses to
a positive duration. Two knobs take a duration: D5 (`git-timeout`) and D12
(`cli-download-timeout`). D5 logs under `[Server]`, and D12 logs under
`[Install]`. Both parse the same way. The deprecated `cli-probe-timeout` and
`libc-probe-timeout` set nothing, whatever their value. A flag value that does
not parse still exits 2, as in the table:

| Value shape | Configuration key in `claustrum.conf` | `-…` flag on the argv |
|-------------|--------------------------------|-----------------------|
| Unparseable (`= 15`) | Dropped silently, with no log. The run proceeds with no deadline | `flag.Duration` rejects it before any mode runs: `invalid value "15" for flag …: parse error` + usage, exit 2, no facts line |
| Negative (`= -1s`) | Dropped silently, with no log and no deadline | Normalized to 0 with a `[Server]`/`[Install]` stderr warning, then runs normally (exit 0, facts line printed) |
| Any zero (`0`, `+0`, `-0`, `0s`, `-0m`, `-0.4ns`) | Accepted. It reads as opted-in but the deadline stays off | Accepted, with the deadline off |

The configuration path is silent, and that silence is the shape that looks like
drift. A missing `__INSTALL_RESULT__` does not by itself mean a malformed flag.
`-install` prints no facts line while it waits for a CLI that does not answer.
That wait ends at 30 s on a cache hit and at 120 s after an install. Through a
usable managed launcher it ends at 33 s and at 123 s. The
facts line then carries `"cliUnresponsive":true`. That is parity. Discriminate on
exit. Exit 2 with `parse error` on stderr is the bad flag. Exit 2 with
`cannot resolve home directory` on stderr is a missing home folder.

D4 is the one exception. It is a bool, not a duration (`files-read-regular-only`).
The forms parse like this:

- Configuration value unrecognized (`= maybe`): the parser drops it silently and
  leaves the key unset, so the flag value stands (off, unless a flag was also
  passed).
- Flag `=value` form unrecognized (`-files-read-regular-only=maybe`): `flag.Bool`
  rejects it with usage + exit 2.
- Flag space form (`-files-read-regular-only maybe`): arms the guard, because a
  bool never consumes the next arg. `maybe` becomes a positional and parsing
  stops. That silently drops every later flag. The guard is therefore armed only
  for an argv where the parser read `-serve` and the socket and token flags
  *before* the typo.
- Inert-but-accepted spelling: `false` (both forms). It contains the name a
  triager greps for, but it arms nothing.

### Triage gotchas — when a probe result is misleading

[docs/DIVERGENCES.md](DIVERGENCES.md) holds the full measurements. These are the
traps that matter for telling drift from expected:

- D4 and D5 (writerless FIFO, surviving-child git): a probe run that records "no
  reply from both binaries" does not discriminate. The off state blocks too.
  `/dev/null` is D4's discriminating input. A surviving child makes the general D5
  git sites soft (`CombinedOutput` waits on the output pipe, not on git's exit). The
  one exception is `git.worktree_create`'s read-tree checkout. It caps the
  post-exit drain at ~5 s and SIGKILLs the process group, which is parity with
  `4534d86` (`scratch/probe/wt-success-lingering-4534d86.md`). It gates success
  against `errorCode:"timeout"`+rollback on the caller `timeoutMs`. The off default
  (no `timeoutMs`, D5 off) is unbounded on every path, matching the reference. The
  one exception is the branch step, which both binaries bound since `89cb6289`
  (PROTOCOL.md → The branch step).
- D5 has a wire-invisible arm. When the deadline kills `git ls-files` or
  `git check-ignore`, `git.worktree_create` still answers `{"success":true}`, and the seeded files are
  absent. That covers both passes: the `.worktreeinclude` manifest copy and the
  `.claude/` copy. Nothing on the wire says so, so a clean frame diff does not
  cover it. A D5 kill of any `sourceBranch` step can change the start commit.
  The frame changes only when both lookups are killed.
- The failure frames of `git.worktree_create` differ between pins. In
  `f6010b97` the git text can start with git's graft-file deprecation `hint:` lines.
  In the add-failure frame the 512-byte cap can then cut the rest of the text.
  `90fca6e6` prints no hint. claustrum sets `GIT_GRAFT_FILE` as `f6010b97`
  does, so its frames carry the same hint. A hint-only difference from
  `90fca6e6` is therefore not drift.
- In a repo with no `.claude/`, a `timeoutMs` that expires during the copy step
  splits the pins. `f6010b97` runs no copy `ls-files` and answers success.
  `90fca6e6` and claustrum answer `timeout` "after the checkout finished" and roll
  back. `89cb6289` answers like claustrum (the Linux timeout set of the whole-build
  check of issue 442). That difference is not drift.
- The branch step of `git.worktree_remove` and of the create rollbacks splits the
  pins. claustrum follows `89cb6289`, the build that `scripts/UPSTREAM_SHA`
  names. Against `f6010b97`, `server.capabilities` differs by the
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
  env strip, and the gated launcher runs of `-install` and `-probe-cli`.
  `f6010b97` has none of these. See [PROTOCOL.md](PROTOCOL.md) → launcher.*.
- The reap of a child record with a `program` key splits the pins too, off the
  wire. claustrum follows `89cb6289`. On Linux, if the process runs `program` or
  holds it as one whole argument, `89cb6289` accepts the record. Linux rows PG02a, PG02b,
  ED03a, ED03b, ED04a and ED04e show that. `f6010b97` sends no signal in rows ED03a
  to ED04e. That difference is not drift. See
  [PROTOCOL.md](PROTOCOL.md) → process.spawn.
- The configuration listing, the stray `commondir` rules and the `git.info` root
  split the pins too. claustrum follows `89cb6289` there. Against `f6010b97` these
  differences are not drift:
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
    L14b, L14d, L14e and N01 to N04, and N05 on Windows).
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
    part ([PROTOCOL.md](PROTOCOL.md) → `git.status`, rule 6).

  See [PROTOCOL.md](PROTOCOL.md) → Git-directory trust check, Hardened git calls
  and git.info.
- D12 needs a VALID zstd body. D13's ordering answers an invalid one at 0 s,
  which reads like "no divergence". Also, a zero download timeout frees the body
  read only: the transport still applies `net.Dialer{Timeout: 30s}`,
  `TLSHandshakeTimeout: 10s` and the 60 s response-header limit. That last limit
  is parity.
- D13 has one honest shape: a short or truncated artifact reaches the checksum
  (claustrum `checksum mismatch: …`). A connection reset in the body is not a D13
  case. It answers
  `download interrupted after <got>/<total> bytes: <err>` on both binaries
  (measured on Linux against `f6010b97` and `89cb6289`). A different text there
  is drift. A body that ends before its length is not measured, so do not call a
  difference there drift.
- D13 also shows with equal texts. For a download that is not a zstd archive,
  with the right checksum, both binaries answer `decompressing: …`. The reference
  reported `fetch.bytes` 4 and one progress line in the measured row. claustrum reports the full body
  size (4096 in the measured row) and two progress lines. That delta is D13, not
  drift (Linux VM, `f6010b97` and `89cb6289`).
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
  reconciled. One carries a deliberate exception. On the host cleaner's macOS busy
  probe, claustrum reads an `lsof` run it gave up on as busy. The reference side is
  not probe-measured. That is [DIVERGENCES.md](DIVERGENCES.md) D17 rather than drift.
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
  claustrum does the same. The frames of that first process still come on its own
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
  is not drift in one case: the process ended on the graceful signal within the
  grace. `89cb6289` then sends its group no `SIGKILL` (Linux rows X2a and X2b,
  also B1 and H4a to H4c). claustrum is built to it. When the grace runs out,
  both send the group `SIGKILL` (Linux rows B2, B3 and X1). macOS rows X2a to
  X2c give the same. Windows has no X2 row.
- A daemon that runs no login shell before its first `process.spawn` is not drift
  (Linux and macOS rows G1 and G2 against `89cb6289`).
- A first spawn that answers after 4.6 s with a timed-out PATH line is not drift
  when a process of the login profile holds the output pipe. `89cb6289` does that
  (Linux rows P13 and P13k), and claustrum is built to it.
- A `-serve` launcher that gives up after 12 s is not drift. If the daemon child
  of `89cb6289` ends before the bind, its launcher exits 1 after 12.06 s (Linux
  row P7, macOS row P7 and two Windows rows). The bound of `5db5e4a` was about
  10 s.
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
    - At a `-stop` with a live child, `89cb6289` sent the group `SIGKILL` and then
      removed `daemon.token`. claustrum removed the token first (row P1).
    - claustrum logs an exit line for a child that a shutdown kills:
      `Process <id> exited with code -1, terminated by SIGKILL, signalled at shutdown request`.
      `89cb6289` logs none (Linux row E3, macOS row A3).
    - After a `-stop` during the wait of a supersede, claustrum can log the exit
      line of the old process and the `supersedes` line. `89cb6289` logged neither
      (row P1: Linux 1 of 6 runs against 0 of 7, macOS 2 of 2 against 0 of 2).
      In that one Linux run build `ac5cadb` also sent the group one more
      `SIGKILL`, which answered `ESRCH`. From the code: that call is gone, with
      the group `SIGKILL` after a clean exit.
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
- Two differences on Windows are known and open. They are not drift.
    - At a stop with one connection, `89cb6289` logged `closed 0 connection(s)`
      in 2 of 9 runs. claustrum logged `closed 1` in 9 of 9. The rate is not
      measured.
    - The environment block of a child comes in name order. The Go 1.26 toolchain
      sorts it. `f6010b97` and `89cb6289` keep the order of the daemon and put
      `CLAUDE_SSH_DAEMON_CHILD=1` last (rows WN01a, WN01b). The stdout frame of
      `cmd /c set` therefore differs in bytes.
- A longer `remote-server.log` after a second daemon started on a live socket on
  Windows is not drift. `89cb6289` truncates the file at that start, and claustrum
  appends to it (rows WN04, WJ04). That is [DIVERGENCES.md](DIVERGENCES.md) D21.
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
today, `89cb6289`, carries a go1.25.14 stamp (`go version` on the binary), so it
emits the same escape. `files.read`
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

### The symlink-loop text of `filepath.EvalSymlinks`

`git.worktree_create` with a `worktreeRoot` detects a symlink loop by the error
text of `filepath.EvalSymlinks`, "EvalSymlinks: too many links". Go does not
export that error. The test lives in `isSymlinkLoop` (`worktreeexternal_unix.go`).
If a Go release rewords the text, the loop frame changes from `unsafe_path` to
`mkdir_failed`. The S1, S1b, S1c and S1d cases in
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
same failure (rows D03-junction and W09). With the walk following the junction, those
roots can move too. This
entry is derived from the Go source (`os/types_windows.go` and
`path/filepath/symlink.go`), not measured. Check those rows after each Go bump that
changes `winsymlink`.

### The git version under the checkout tests

With a `worktreeRoot`, both worktree methods run `git worktree list --porcelain -z`
(`worktreeListCall`, `worktreeexternal.go`). The `-z` option needs git 2.36 or
later. On an older git that call fails. `git.worktree_create` then compares the root
with the git top level of `baseRepo` only. A root in another checkout of the
repository then passes. `git.worktree_remove` then runs `git worktree list
--porcelain` and reads its lines, so its tests run as usual. The references on such
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
