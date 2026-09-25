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
now well behind the current baseline, `90fca6e6`. It used a real Desktop session and
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
(D1–D17, CT-1..CT-5) and the four-rule standard that governs them. That file is the
canonical home for what each one is, why it exists, how to activate it, and its
reopen trigger. When the drift check flags one of them, it is expected, not drift.
Check it against the catalog before you "fix" it.

This triage index adds the one question the catalog does not answer directly. Can a
probe even see this divergence? If it can, is the symptom drift or an activated
opt-in?

### Which divergences a probe can see

`battery-visible?` asks whether the standard frame battery (`validate.sh` /
`battery.js`) shows a diff. The install-path bounds (D10–D14) run under
`-install`, which the frame battery never drives at all.

| ID | Default | Battery-visible? | What it is |
|----|---------|------------------|------------|
| D1 | Conditional. It fires for a supplied `-cli-checksum`. Caller-activated, not operator-declinable. On `-install` the caller is Desktop | No via frames. `scratch/probe/cli_probe.sh` does drive it | `-cli-zst` SHA-256 verification |
| D3 | Off (`0` = unlimited) | No. Off the default path | `files.extract_tar` size cap |
| D4 | Off | Yes. `battery.js` id 70 reads `/dev/null`. No diff at the default (guard off), and it turns red once armed | `files.read` regular-file guard |
| D5 | Off (`0` = no deadline) | No. Off the default path | git-invocation deadline |
| D10 | Off (`0` = unlimited) | No. Install path | `-install` CLI size cap |
| D11 | Off (`0` = no deadline) | No. Install path | `-install` runnability-probe deadline |
| D12 | Off (`0` = no bound) | No. Install path | `-install` download bound |
| D14 | Off (`0` = no deadline), linux only | No. Install path | `ldd --version` libc probe deadline |
| D2 | Always-on | Maybe. A probe that reaches the path shows it (expected) | destructive-path home-dir refusal |
| D6 / D7 | Always-on | Maybe. A probe that reaches the path shows it (expected) | `-cli-version` single path component / temp-sweep collision |
| D8 | Always-on | No. It falls back to inherited stdio, not a frame | foreign/symlinked `remote-server.log` not followed (`.old` rotation matched, refuse-to-follow kept) |
| D9 | Always-on | Maybe. A type-mismatched namespace field is rejected | namespace-param binding vs. the reference's ignore |
| D13 | Always-on (unresolved in DIVERGENCES.md) | No. Install path | verify-before-decompress ordering |
| CT-1 | Opt-in (`wantPid`) | Yes, on request. It adds `pid`/`startTime` | spawn/reattach reply extension |
| CT-2 | Opt-in (`-keep-children`, POSIX) | No | children survive shutdown |
| CT-3 | Opt-in (`claustrum.conf`) | Only `version-override`, via the static check's `-version` diff | the configuration file itself |
| CT-4 | Not built. A deferred idea, recorded in DIVERGENCES.md only | No. There is no code | opt-in hardened token persistence (a `persist-token` key, or a Windows owner-only DACL) |
| CT-5 | Opt-in (`-listen-pipe`, Windows) | No | additional named-pipe transport |

Check both indexes. The shipped ledger ([docs/IMPROVEMENTS.md](IMPROVEMENTS.md))
numbers several more claustrum-only behaviors, and they are just as real. They are
item 16 (`-metrics-addr`), item 17 (claustrum tears down the orphaned previous
process tree), and item 18 (`-token-fd`). Item 21 is another: claustrum skips the
kill signal for a child that already exited. Check both the divergence catalog and
the shipped ledger before you conclude that something is drift.

### Drift, or an activated opt-in? — the parse-behaviour table

An opt-in key that is *present* is not a deadline that is *in force*. A mistyped or
inert value leaves the divergence off, and the parity behavior that results reads
exactly like drift. When a symptom matches a D-shaped divergence on a stock
claustrum, check for the key or flag first. Then make sure that the value parses to
a positive duration. Four knobs take a duration: D5 (`git-timeout`), D11
(`cli-probe-timeout`), D12 (`cli-download-timeout`), and D14
(`libc-probe-timeout`). D5 logs under `[Server]`. The last three log under
`[Install]`. All four parse the same way:

| Value shape | Configuration key in `claustrum.conf` | `-…` flag on the argv |
|-------------|--------------------------------|-----------------------|
| Unparseable (`= 15`) | Dropped silently, with no log. The run proceeds with no deadline | `flag.Duration` rejects it before any mode runs: `invalid value "15" for flag …: parse error` + usage, exit 2, no facts line |
| Negative (`= -1s`) | Dropped silently, with no log and no deadline | Normalized to 0 with a `[Server]`/`[Install]` stderr warning, then runs normally (exit 0, facts line printed) |
| Any zero (`0`, `+0`, `-0`, `0s`, `-0m`, `-0.4ns`) | Accepted. It reads as opted-in but the deadline stays off | Accepted, with the deadline off |

The configuration path is silent, and that silence is the shape that looks like
drift. A missing `__INSTALL_RESULT__` does not by itself mean a malformed flag. A
stock claustrum blocked on a CLI that never answers also prints no facts line, which
is the deadline-off parity behavior. Discriminate on exit. Exit 2 with `parse error`
on stderr is the bad flag. Still running with nothing on stderr is the parity
wait.

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
  (no `timeoutMs`, D5 off) is unbounded on every path, matching the reference.
- D5 has a wire-invisible arm. When the deadline kills `git ls-files` or
  `git check-ignore`, `git.worktree_create` still answers `{"success":true}`, and the seeded files are
  absent. That covers both passes: the `.worktreeinclude` manifest copy and the
  `.claude/` copy. Nothing on the wire says so, so a clean frame diff does not
  cover it. A D5 kill of any `sourceBranch` step can change the start commit.
  The frame changes only when both lookups are killed.
- The failure frames of `git.worktree_create` differ between pins. In
  `f6010b97` the git text starts with git's graft-file deprecation `hint:` lines.
  In the add-failure frame the 512-byte cap can then cut the rest of the text.
  `90fca6e6` and claustrum do not print the hint, so that prefix is not drift.
- In a repo with no `.claude/`, a `timeoutMs` that expires during the copy step
  splits the pins. `f6010b97` runs no copy `ls-files` and answers success.
  `90fca6e6` and claustrum answer `timeout` "after the checkout finished" and roll
  back. That difference is not drift.
- D12 needs a VALID zstd body. D13's ordering answers an invalid one at 0 s,
  which reads like "no divergence". Also, a zero download timeout frees the body
  read only: `http.DefaultTransport` still applies `net.Dialer{Timeout: 30s}` and
  `TLSHandshakeTimeout: 10s`.
- D13 has two honest shapes a triager must not merge. A short or truncated
  artifact reaches the checksum (claustrum `checksum mismatch: …`). A genuine
  interrupted transfer never does, because `io.Copy`'s error returns first.
  Claustrum therefore diverges on the *prefix* (`download failed: <err>`).
- D14 fires in only one of two stall shapes (a surviving child blocks both
  binaries). There is no libc probe off linux at all, so a `libc`
  difference off linux is not D14.
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
today, `90fca6e6`, carries a go1.25.14 stamp (`go version` on the binary), so it
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
