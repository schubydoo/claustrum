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

`Battery-visible?` asks whether the standard frame battery shows a diff. "Maybe"
means that a probe which reaches the path shows it. The battery never drives
`-install`. Each symptom below is expected, not drift. The entry in
[DIVERGENCES.md](DIVERGENCES.md) has the texts and the detail.

| ID | Default | Battery-visible? | Symptom |
|----|---------|------------------|---------|
| [D2](DIVERGENCES.md#d2) | Always-on | Maybe | A delete path that is or holds the home folder is refused, also on `-install` |
| [D3](DIVERGENCES.md#d3) | Off | No | An opt-in size cap fails `files.extract_tar` |
| [D6](DIVERGENCES.md#d6) | Always-on | No, install path | A `-cli-version` of more than one path component is refused |
| [D8](DIVERGENCES.md#d8) | Always-on | No, a start failure | On Linux and macOS a FIFO, another user's file or a link to one at `remote-server.log` fails the start. On Windows a file link does. The `git-shadow` sweep writes nothing through a link |
| [D10](DIVERGENCES.md#d10) | Off | No, install path | An opt-in size cap fails `-install` |
| [D12](DIVERGENCES.md#d12) | Off | No, install path | An opt-in bound fails a slow `-install` download |
| [D15](DIVERGENCES.md#d15) | Always-on, macOS | No, off the wire | A lock holder that is not our `-serve` gets no signal |
| [D16](DIVERGENCES.md#d16) | Always-on, Windows | Yes | `git.status` of a linked worktree answers the status, not an error, when the user has no global excludes file |
| [D19](DIVERGENCES.md#d19) | Always-on, Windows | Maybe | `git.worktree_remove` refuses a junction between `baseRepo` and the leaf |
| [D20](DIVERGENCES.md#d20) | Always-on, Linux and macOS | No, off the wire | The group `SIGKILL` of a gone leader comes about 50 ms later, or not at all |
| [D22](DIVERGENCES.md#d22) | Always-on | Maybe | `git.worktree_remove` refuses a locked worktree where `89cb6289` answers success (four states), and with `worktreeRoot` the frame differs in three more |
| [D23](DIVERGENCES.md#d23) | Always-on, Windows | Maybe, in stream frames | The environment block of a child is in name order |
| [D24](DIVERGENCES.md#d24) | Always-on | No for a replaced folder: the disk differs. Yes at the index placement (not measured), and after a `.git` file swap on Windows | A create rollback keeps a folder that replaced the registration. On Windows it runs no git call after a `.git` file swap |
| [D27](DIVERGENCES.md#d27) | Always-on, Windows | Yes, in a case-sensitive folder | The stale step keeps an entry whose record path exists |
| [CT-1](DIVERGENCES.md#ct-1) | Opt-in | Yes, on request | `pid` and `startTime` on spawn and reattach |
| [CT-2](DIVERGENCES.md#ct-2) | Opt-in | No | Children survive shutdown |
| [CT-3](DIVERGENCES.md#ct-3) | Opt-in | Only `version-override`, in the static check's `-version` diff | The `claustrum.conf` file |
| [CT-4](DIVERGENCES.md#ct-4) | Not built | No | None |
| [CT-5](DIVERGENCES.md#ct-5) | Opt-in, Windows | No | An extra named-pipe transport |

D1, D4, D5, D7, D9, D11, D13, D14, D17, D18, D21 and D25 are retired: claustrum
matches the reference there. A difference on those paths is drift, unless a live
entry above covers it. Two things on those paths are claustrum's own and not
drift: the D13 chmod, clear and rename come after the checksum test, and a D17
pass takes longer. A difference against `89cb6289` on the D4 paths is
expected, and so is the stop of a slow git call (D5). See
[DIVERGENCES.md → Retired entries](DIVERGENCES.md#retired-entries), and the
`files-read-regular-only` paragraph below.

Some flags are claustrum's own and have no entry: `-token-fd`
([PROTOCOL.md → Flags and config keys](PROTOCOL.md#flags-and-config-keys)), and
`-metrics-addr` and `-wire-log`
([PROTOCOL.md → Flags of claustrum alone](PROTOCOL.md#flags-of-claustrum-alone)).
So is the skip of the kill signal for a child that already ended
([process.kill → Differences from the reference](protocol/process-kill.md#differences-from-the-reference)).
So is a rule of `process.killAndWait`. For a signal other than `KILL` it sends no
first signal during the exit drain. On Linux and macOS `KILL` goes to the group at
once, as on the reference
([A request during the exit drain](protocol/process-killandwait.md#a-request-during-the-exit-drain)).
Check those before you conclude that something is drift.

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
code, it comes from the `O_NONBLOCK` open of claustrum. That case is not
measured on a VM.

### Triage gotchas — when a probe result is misleading

These are the traps that matter for telling drift from expected. In the git and
files methods, claustrum follows `5fd08069`, the build that `scripts/UPSTREAM_SHA`
names. The exceptions are the D-entries above, and the places where an item below
or the linked page names an older build. A probe against an older build
shows the differences of each item as a build split, not drift. The rules are on
the linked pages. The history of each build is in
[REFERENCE-BUILDS.md](REFERENCE-BUILDS.md).

- A git call that runs 2 minutes is stopped, and the reply carries `git took too
  long`. A scan of ignored files that its limit of 10 minutes stopped answers
  `{"success":true}` with no copied file. Both are parity, not drift. See
  [PROTOCOL.md → Time limits of git calls](PROTOCOL.md#time-limits-of-git-calls).
- The failure texts of `git.worktree_create` hold no graft-file `hint:` lines, as
  on `5fd08069` and `90fca6e6`. The texts of `f6010b97` and `89cb6289` can hold
  them. See [Hardened git calls](PROTOCOL.md#hardened-git-calls).
- In a repository with no `.claude/`, a `timeoutMs` that expires during the copy
  step answers `timeout` "after the checkout finished" and rolls back, as on
  `90fca6e6` and `89cb6289` (measured on Linux for `89cb6289`). `f6010b97`
  answers success there. See
  [The copy of ignored files](protocol/git-worktree-create.md#the-copy-of-ignored-files).
- A folder that a `git.worktree_create` rollback keeps where `89cb6289` removes it
  is [D24](DIVERGENCES.md#d24), not drift. The frames are equal.
- The branch step of `git.worktree_remove` and of the create rollbacks follows
  `89cb6289`. `5fd08069` is not measured there. On remove, each kept case of the keep
  list adds `"branchKept":true`, where `f6010b97` adds no member and in most
  states deletes the branch. In four states a create rollback answers as
  `f6010b97`: update-ref fails, is stopped or meets a lock, or the name starts
  with `+`.
  Against `f6010b97`, `server.capabilities` also differs by the
  `git.worktree_remove.unpushedGuard` and `launcher.managed` features and by the
  `launcher.resolve` method. See [The branch step](PROTOCOL.md#the-branch-step).
- The managed launcher follows `89cb6289`: `launcher.resolve`, the
  `process.spawn` `launcher` param, the child env strip, and the gated launcher
  runs of `-install` and `-probe-cli`. The Linux file test on a cache hit before a
  launcher run follows `5fd08069`. Other launcher runs of `5fd08069` are not
  measured. `f6010b97` has none of these. See
  [launcher.resolve](protocol/launcher-resolve.md),
  [process.spawn → The managed launcher](protocol/process-spawn.md#the-managed-launcher)
  and [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli).
- The peer check follows `5fd08069`. `server.capabilities` carries the
  `peerCheck` and `peerCheckBy` members and the `server.peer_check` and
  `git.shadow_dir` features. On Linux with `CLAUDE_SSH_PEER_CHECK=1` the daemon refuses a caller in other
  namespaces with `-32006`. The frames of `89cb6289` have none of these. The
  peer check log lines differ from `5fd08069` by a level tag only (measured on
  Linux). See [The peer check](protocol/server.md#the-peer-check).
- `files.extract_tar` follows `5fd08069`, and `89cb6289` differs in each of these
  points:
  - the `files.extract_tar.execBit` feature
  - the mode `0700` of a file with the execute bit of the owner (Linux and macOS)
  - the refusal of a name that leaves `destDir` and comes back
  - the last entry of a file that the archive names two times
  - the `error` texts of the entry and `destDir` steps.

  A `destDir` that the daemon cannot search or open gets the `open destDir:`
  refusal, where `89cb6289` fails later or extracts. On Linux and macOS, a parent
  folder of mode `0300` with no `destDir` gets the `open parent:` refusal, where
  `89cb6289` extracts. The two Windows rows of
  [Differences by system](protocol/files-extract-tar.md#differences-by-system)
  are parity, and `89cb6289` differs in both (measured on Windows). A `\\?\` path with a dot or a space after its
  last name differs from both builds ([D2](DIVERGENCES.md#d2)). A `destDir` that
  is the home folder by identity gets the home refusal (D2). See
  [files.extract_tar](protocol/files-extract-tar.md).
- The `-install` prune and sweeps follow `5fd08069`, and so do the blob timing,
  the Windows `rename: ` prefix, the Windows rename retry and the Linux file test of a
  cache hit. See [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli).
- The git filters of the `git.worktree_create` checkout follow `5fd08069`. Against
  `89cb6289` the refusal `repository_only_filter` is new. So are the texts for a
  name over 200 bytes and for more than 64 filters (`89cb6289` not run for 64
  filters). An outside filter now runs
  where the repository defines the same name. Against `5fd08069` these differences are not drift:
  - With a `GIT_DIR` and a `GIT_WORK_TREE` of another repository in the daemon
    environment, claustrum answers `not_a_repo`, as `89cb6289` does, where
    `5fd08069` answers the filter refusal (measured on Linux).
  - A `~/.gitconfig` that is a directory gets the text of `89cb6289` (measured on
    Linux).
  - With a temporary directory that cannot be used, claustrum fails as
    `89cb6289` does, where `5fd08069` answers the filter refusal (measured on
    Linux and Windows).
  - A git-lfs program key of the repository with no settings copy (claustrum's
    own, `5fd08069` not measured), and some `filter.` pairs of the call log.

  See [Git filters](protocol/git-worktree-create.md#git-filters).
- The refusals of a repository config follow `5fd08069`. Against `89cb6289` the
  texts that start with `this repository's git config cannot be used: ` and
  `a file in this repository's .git directory cannot be used: ` are new, and the
  call log has one more call for each config file of a first read. A
  `git.worktree_remove` of a worktree folder that does not exist is refused too.
  With `worktreeRoot` the refused remove has the text `cannot determine the
  repository's work tree: ` before the refusal (measured on Linux and macOS).
  With `"shadowDirOff":true`, or while such a request runs (measured on Linux),
  the read does not run. Against `5fd08069` these differences are not drift. On a config that
  changes many times each second, `5fd08069` makes more config reads (measured on
  Linux). For a config with a very long key, `5fd08069` ends config reads early
  (measured on Windows). A config whose listing passes 64 MiB can get no refusal
  on claustrum (claustrum's own, not measured). See
  [Repository config refusals](PROTOCOL.md#repository-config-refusals).
- The option lists of the git calls follow `5fd08069`. Against `89cb6289` they
  differ in four states: `git.status` with replace objects, the `hint:` lines
  above, a long tree path on Windows, and the paths of a new worktree with
  `worktree.useRelativePaths`. The `-c` lists and heavy variables of the call
  log differ too. Against `5fd08069` the call logs of `git.worktree_create` differ with
  `worktreeRoot` and with `existingBranch`. See
  [Hardened git calls](PROTOCOL.md#hardened-git-calls) and
  [Where the calls differ from `5fd08069`](PROTOCOL.md#where-the-calls-differ-from-5fd08069).
- The `worktreeRoot` location rules follow `5fd08069` (measured on Linux and
  macOS). `89cb6289` is not measured there.
- A remove behind a linked `<git dir>/worktrees` follows `5fd08069` (measured on
  Linux and macOS, and for a gone worktree with a locked entry on Linux).
  `89cb6289` is not measured.
- The settings copy follows `5fd08069`. Against `89cb6289` a triager meets the
  `git.info` member `settingsControl`, the folder `git-shadow` in the run folder,
  12 more git calls in a first git request, and new refusal texts. On Windows
  each git method refuses a junction or a directory symbolic link at `.git`
  without `"shadowDirOff":true`. On Windows a create removes old
  `.claude-new-worktree-*` entries of the git directory. A config value that git
  refuses gets a normal answer where the copy leaves it out. Against `5fd08069`
  the config reads above differ. Two reply texts differ too, for a Windows
  create whose `.git` becomes a file `gitdir: <X>` while it runs (measured on
  Windows).
  See [The settings copy](PROTOCOL.md#the-settings-copy).
- A remove with a worktree folder that exists lists again after a failed
  listing, as on `5fd08069` (measured on Linux, macOS and Windows). `f6010b97`
  answers the lock-check text there. Take an empty `GIT_CONFIG_KEY_0` in the
  daemon environment. claustrum and `5fd08069` then answer the hooks refusal with
  the text of the failed listing. `89cb6289` answers the lock-check text. Take a
  `baseRepo` that is a plain folder, with nothing set. After a failed first
  listing and a failed `rev-parse --absolute-git-dir`, claustrum makes one more
  listing and `rev-parse` than `5fd08069`. The replies are equal (measured on
  Linux). See [The repository checks](protocol/git-worktree-remove.md#the-repository-checks).
- The reap of a child record with a `program` key splits the pins too, off the
  wire. claustrum follows `89cb6289`. `5fd08069` is not measured there.
  On Linux, when the process runs `program` or holds it as one whole argument,
  `89cb6289` accepts the record (Linux VM). `f6010b97` sends no signal in some
  of those states. That difference is not drift. See
  [PROTOCOL.md → Child records and the reap](PROTOCOL.md#child-records-and-the-reap-linux-and-macos).
- The configuration listing, the stray `commondir` rules and the `git.info` root
  follow `89cb6289`. `5fd08069` is not measured there. In the state `shadow` the
  answers of [The settings copy](PROTOCOL.md#the-settings-copy) come first.
  Against `f6010b97` these differences are not drift:
  - `server.capabilities` carries `git.info.discovered_root`.
  - A stray `commondir` gets the texts S1 to S3, and a dangling relative
    `commondir` symlink in a valid git directory counts as none. `f6010b97` gives
    one older text, and it refuses the remove there.
  - A listing error is in English under a German daemon locale.
  - A configuration key of 1048576 bytes passes, and one byte more gets the key
    text.
  - A listing that exits 128 with `fatal: not a git repository` answers the "no
    repository" shapes. When `git version` also fails, the text says git cannot
    run. `f6010b97` gives the hooks refusal there.
  - The `git.info` root comes from the walk.
  - [D16](DIVERGENCES.md#d16) stays. `git.status` answers by the worktree entries
    of `baseRepo`, as `89cb6289` does
    ([git.status](protocol/git-status.md#path)).

  See [Git-directory trust check](PROTOCOL.md#git-directory-trust-check),
  [Hardened git calls](PROTOCOL.md#hardened-git-calls) and
  [git.info → The root](protocol/git-info.md#the-root).
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
  checksum (`5fd08069`, Linux VM). For such a body, another count is drift.
- The `ldd` libc probe bounds ldd itself at 5 s on both binaries (parity since
  `19f30c46`). There is no libc probe off linux at all.
- claustrum made seven changes for `90fca6e6` while the JSON-RPC surface stood
  still. A triager who meets one of them and finds the drift check quiet is
  looking at parity, not drift. No method, field or error string moved. Three of
  the seven still change what a client reads. A reattach now closes the
  connection it replaces. A reaped process reads as not running for
  `process.stdin` and `process.reattach` inside the exit drain. A new worktree
  no longer inherits the nine Claude runtime-state names under `.claude/`. That
  last one is the likeliest to be misread. `19f30c46` copies eight of the nine,
  so a file missing from a seeded worktree looks like a regression against the
  previous pin. [REFERENCE-BUILDS.md](REFERENCE-BUILDS.md) holds all seven, and
  all seven are reconciled. The macOS `lsof` reads of the host cleaner follow
  [The `lsof` reads](PROTOCOL.md#the-lsof-reads-macos). The longer pass of
  claustrum there is in the `5fd08069` section of
  [REFERENCE-BUILDS.md](REFERENCE-BUILDS.md), and it is not drift. An `lsof` command that does not start, and a run that
  writes to stderr and exits 1, are parity with `89cb6289` for the busy read and
  the lock read (measured on macOS, in every run).
- The `90fca6e6` reconciliation shipped four PRs. A full re-check then found a
  seventh change that the first pass had missed. A new value inside an existing
  function shows in no symbol list, so a quiet symbol comparison does not prove
  that nothing changed. Re-check the whole build after the slices merge.

- A reference daemon that stays alive after its socket path is gone is not drift.
  On a Linux VM, the host cleaner of `f6010b97` and `89cb6289` sent no signal to
  such a daemon. It sent none to its children. The daemon ended by itself after
  11 to 12 minutes. The cleaner of claustrum sends none either. On a macOS VM
  the two references sent none either.
- On Windows a serving process that outlives its SSH session is not drift, and
  children that outlive a killed daemon are not drift. A Windows VM measured
  both against `f6010b97` and `89cb6289`, with one state on `89cb6289` only. A
  later run on that VM measured the same on claustrum. A spawned child is in no
  job on either side, and a kill ends the direct child only (kill forms:
  `89cb6289`). Descendants that outlive a kill or a `-stop` are not drift.
- A daemon that accepts no connection for some seconds after its start is not
  drift. On Linux and macOS the daemon binds its socket after the reap of its
  start. With a recorded child that ignored `SIGTERM`, the first connection to
  `89cb6289` worked 2.2 to 2.4 s after its start (Linux and macOS VMs). Before
  that a client got `ECONNREFUSED` or `ENOENT`. In that
  time `daemon.lock` holds `pid`, `role` and `node` only. claustrum is built to
  the same order.
- Child records and both markers on a socket that is not `run/<id>/rpc.sock` are
  not drift. `89cb6289` writes them on every socket shape (Linux and macOS
  VMs), and claustrum does too.
- Records that stay after a daemon start are not drift in two cases. One start
  handles 128 record names and ends 64 groups, on `89cb6289` (Linux VM).
  claustrum is built to it. The rest goes at a later start.
- A first process that lives on after a second `process.spawn` took its `id` is
  not drift. `89cb6289` sends it no signal, and a stop of the daemon leaves it
  alive (Linux, macOS and Windows VMs). The frames of that first process still
  come on its own connection, under the same `processId` (Linux VM against
  `89cb6289`). claustrum is built to it.
- A `process.spawn` reply that comes 3 to 5 s late is not drift when the spawn
  supersedes a session process. `89cb6289` answers after the old process ended
  (Linux, macOS and Windows VMs). claustrum does the same.
- An old session process that lives on after a failed session spawn is not drift
  in one case: the command of the new spawn does not exist. `89cb6289` sends the
  old process no signal there (Linux, macOS and Windows VMs). claustrum is built
  to it. A command that exists and does not start ends the old process first,
  on `89cb6289` and on claustrum (Linux VM).
- A descendant that lives on after `process.killAndWait` or a session supersede
  is not drift in these cases. One is a process that ended on the graceful
  signal within the grace. `89cb6289` then sends its group no `SIGKILL` (Linux
  and macOS VMs). Windows is not measured in that case. claustrum is built to
  it. When the grace runs out, both send the group `SIGKILL` (Linux VM). The
  holder of the pipes also lives on, with equal answers on `5fd08069` (macOS and
  Linux VMs).
- A daemon that runs no login shell before its first `process.spawn` is not drift
  (Linux and macOS VMs against `89cb6289`).
- A first spawn that answers after 4.6 s with a timed-out PATH line is not drift
  when a process of the login profile holds the output pipe. `89cb6289` does that
  (Linux VM), and claustrum is built to it.
- A `-serve` launcher that gives up after 12 s is not drift. If the daemon child
  of `89cb6289` ends before the bind, its launcher exits 1 after 12.06 s (Linux,
  macOS and Windows VMs).
- A daemon that gets `SIGTERM` or `SIGINT` during its start, goes on to its bind
  and then exits 0 is not drift. `89cb6289` does that (Linux and macOS VMs), and
  claustrum is built to it. When a `SIGTERM` comes while the daemon waits to
  evict a live daemon, or from a `-stop` during the reap of its start, its
  launcher then exits 1 after 12 s (Linux VM).
- A daemon that gets `SIGKILL` from a second daemon during its start is not
  drift. `89cb6289` does that when the two starts are 0.05 s apart (Linux VM).
  The second daemon waits on the lock, and claustrum is built to it.
- The order of two or more new caller keys in a child environment is not drift.
  `89cb6289` gave three rotations of the request order over many spawns (Linux
  VM). claustrum iterates the decoded map and builds no order of its own. On
  Windows see D23.
- A record or a marker with two blanks in its macOS start text is not accepted.
  An older claustrum build wrote that form. `89cb6289` and claustrum
  drop such a record and send no signal (macOS VM). A child of such
  an older build is therefore not ended at the next start.
- A child of a live daemon that a new daemon ends on macOS is not drift in one
  case: the `daemonStart` of its record holds two blanks. `89cb6289` and claustrum
  then read that daemon as gone (macOS VM).
- These differences on Linux and macOS are known and open. They are not drift.
    - At the reap of a start, `89cb6289` probes a group right after its `SIGTERM`.
      If the group still answers, it sends `SIGKILL`. claustrum probes after
      50 ms. That is [DIVERGENCES.md](DIVERGENCES.md) D20.
    - claustrum logs an exit line for a child that a shutdown kills:
      `Process <id> exited with code -1, terminated by SIGKILL, signalled at shutdown request`.
      `89cb6289` logs none (Linux and macOS VMs). `5fd08069` wrote it on
      Linux, not on macOS.
    - After a `-stop` during the wait of a supersede, claustrum can log the exit
      line of the old process and the `supersedes` line. `89cb6289` logged
      neither in any run. claustrum logged them in one Linux run and in every
      run on macOS.
    - `89cb6289` calls unlink on one child record up to three times at a stop or
      a kill. claustrum calls it once. `89cb6289` also calls unlink on a record
      that it never wrote (Linux VM). The files left are the same.
    - On macOS `89cb6289` runs one `ps` at the daemon start and one per spawn.
      claustrum runs none at the start and three per spawn (macOS VM).
    - With a zero-byte `-token-file` the daemon log of `89cb6289` holds
      `claude-ssh: empty token in --token-file`. claustrum logs
      `claustrum: token is empty` (Linux, macOS and Windows VMs).
    - When a frame cannot reach a closed connection, `89cb6289` logs
      `[frameSink] write failed, detaching: write unix <socket>->@: use of closed network connection`.
      claustrum logs `[frameSink] write failed, detaching: use of closed network connection`
      (Linux VM).
- One difference on Windows is known and open. It is not drift.
    - At a stop with one connection, `89cb6289` logged `closed 0 connection(s)`
      in some runs. claustrum logged `closed 1` in every run. The rate is not
      measured.
- A child that `process.spawn` starts as a shutdown begins is known, not drift.
  `5fd08069` left dozens running on Linux, about a thousand on macOS and two on
  Windows (Linux, macOS and Windows VMs). claustrum ends them all.
- A child environment block in name order on Windows is not drift. `f6010b97`
  and `89cb6289` keep the order of the daemon, then `CLAUDE_SSH_DAEMON_CHILD=1`.
  On `89cb6289` an added `PATH` entry and new caller keys come after it
  (Windows VM). The stdout frame of
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

Two refusals rest on a failed `filepath.EvalSymlinks`. One is the trust-root
test of the worktree methods (`baseRepoWalkFails`, `worktreecontain.go`). The
other is `unresolvable` (`methods_git.go`), which `git.status` applies to
`baseRepo` and `git.list_branches` to `path`. On Windows both depend on the
`winsymlink` GODEBUG setting, which Go 1.23 turned on. With it on, `os.Lstat`
reports a junction as neither a symbolic link nor a directory. With it off, a
junction is a symbolic link. With it on, the walk of `EvalSymlinks` fails at a
junction before the last component. For that reason claustrum answers
`git.worktree_create` and `git.worktree_remove` with the trust-root refusal when
a junction sits before the last component of a `baseRepo` that exists (Windows
VM). With `GODEBUG=winsymlink=0` in the daemon environment, the walk follows the
junction instead. Those answers then flip, and so do the `git.status` and
`git.list_branches` answers for `<dir>\<junction>\T`. Other code resolves
`baseRepo` through `EvalSymlinks` too, so other Windows junction answers can
move as well. A `go` line below 1.23 in `go.mod` has the same effect. The
`git.info` root for a path with a junction before its last component rests on
the same failure. That covers `<T>\lnk\..` with a junction `lnk` and a
repository below a junction (`TestInfoRootLinkThenDotDotWindows` and
`TestInfoRootBelowJunctionWindows`). With the walk following the junction, those
roots can move too. The `GIT_COMMON_DIR` pin of the trust check for such a path
rests on it as well (`walkStart`, `gitdirtrust.go`). With the walk following the
junction, the pin names the repository behind the junction. The `git.info`
answers for such a path then change: `root`, `repo` and `repoSlug` can move to
the repository behind the junction ([git.info → Differences by
system](protocol/git-info.md#differences-by-system)). The frames of `89cb6289`
are pinned in `TestInfoThroughJunctionWindows`
(`gitinfo_junction_windows_test.go`), and the pinned answers are in [PROTOCOL.md
→ Git-directory trust check](PROTOCOL.md#git-directory-trust-check). This entry
is derived from the Go source (`os/types_windows.go` and
`path/filepath/symlink.go`), not measured. Check those answers after each Go
bump that changes `winsymlink`.

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
