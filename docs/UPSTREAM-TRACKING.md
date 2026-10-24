# Staying in lock-step with the reference daemon

claustrum is behaviorally compatible with a reference daemon that ships inside
Claude Desktop's SSH-remote feature. This page tells you how to detect a new
build, and how to find out whether it changed anything claustrum must match. The
history of each build is in the [reference build ledger](REFERENCE-BUILDS.md).

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
2. The Desktop app bundle. A
   `JSON.parse('{"version":"<sha>","manifest":{…},"baseUrl":".../claude-ssh-releases"}')`
   literal inside `resources/app.asar` holds the pinned SHA and the six checksums
   and sizes. A parallel literal pins the CLI (`claude-code-releases`). Its
   `baseUrl` can end in `/rc/<sha>`, so match the bucket by path segment. Two
   scripts read the literal offline:
   - [`scripts/extract-desktop-pin.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/extract-desktop-pin.py)
     reads it from a Linux `.deb`, a Windows `.nupkg` or a macOS `.zip`.
   - [`scripts/latest-desktop-sha.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/latest-desktop-sha.py)
     runs the full loop for one platform (`--platform linux|windows|macos`,
     default `linux`): find the newest Desktop, download, extract, and compare to
     `UPSTREAM_SHA`.
3. The Desktop machine's cache. The per-platform binaries are under
   `<app-data>/claude-ssh-remote/<sha>/` (`%APPDATA%/Claude/…` on Windows),
   beside a `.verified-<goos>-<goarch>` marker.
4. Probe a guess. `manifest.json` returns 200 for a real SHA. It returns 404
   for all other values.

> Note: the *CLI* release manifest
> (`claude-code-releases/<ver>/manifest.json`) has a `commit` field. That field
> gives the CLI's commit, not the daemon's. Do not confuse the two.

### claustrum re-uploads every session (harmless)

The client runs `--version` on `~/.claude/remote/srv/<pinned-sha>/server`. For a
`--version` output that matches `/claude-ssh\s+(\S+)/` with the pinned SHA as that
token, it skips the upload. The stock claustrum line holds no such match, so the
client sends the daemon again by SFTP every session. That is not drift: the version line is CLI stdout, not a frame. The
daemon that runs at the end is still claustrum. The
opt-in `version-override` ([CT-3](DIVERGENCES.md#ct-3)) stops the re-upload. See
[PROTOCOL.md → `-version`](PROTOCOL.md#-version).

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
and `reattach` semantics. To check it, run one frame battery against the new
reference and against claustrum, and diff the results:

1. Start each binary as a private `-serve` on a throwaway socket, with its own
   `-token-file`.
2. Send every method, with the same requests, to each daemon.
3. Mask the values that differ between runs or between the two binaries, such
   as the version id and the token, and diff the two sets of frames.

The in-repo goldens (`testdata/socket_*.golden.json`) hold claustrum's side of
that contract, so `make test` catches a change of claustrum's own frames.

> Safety: probe only a private instance on a throwaway socket. Never point the
> probe at a live daemon's socket. Clean up after every probe.

## Step 3b — real-session capture (highest fidelity, optional)

Steps 2–3 drive the daemon with synthetic requests. The best check is the traffic
of the real desktop client:

- The bridge (`server --bridge`) is a stdio relay. A tee on its stdin and stdout
  records the exact client↔daemon NDJSON of a live session. Replay that record
  against claustrum, and diff without regard to order: key responses by `id` and
  stream frames by `processId` and `seq`.
- Record with a throwaway SSH user, through a `ForceCommand` wrapper for that
  user, so the capture never touches a live daemon. A raw dump holds the session
  token and host paths. Never commit one.

The last capture ran against `8de85faa`, a build well behind the current
baseline, `5fd08069`. Its result is in
[REFERENCE-BUILDS.md](REFERENCE-BUILDS.md#8de85faaa11694321e937499a18c7ab88f37c76c-2026-05-21).
Use a capture as a periodic spot-check. Use it again for a build that changes
`process.*` behavior that the synthetic battery cannot fully model (real
reconnect timing, multi-process reattach).

## Step 4 — reconcile

If the check reports drift:

1. Identify exactly what changed: a new method, a changed error string, a new
   flag, a new install fact, or a changed frame shape.
2. Implement the change in claustrum behind the existing structure. Keep every
   unchanged behavior unchanged.
3. Run the static check and the byte-for-byte battery again, until both are
   clean.
4. Bump the pin to the new SHA in `scripts/UPSTREAM_SHA`, and take the other
   steps of [REFERENCE-BUILDS.md → When you bump the pin](REFERENCE-BUILDS.md#when-you-bump-the-pin).
   For a user-facing change, add a `.changeset/` fragment with
   `knope document-change`. knope writes `CHANGELOG.md`.

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

An opt-in key that is *present* is not a deadline that is *in force*. An unparseable,
negative or inert value leaves the divergence off, and the resulting parity behavior reads
like drift. So check for the key or flag first, and check that its value parses to
a positive duration. One knob takes a duration: D12 (`cli-download-timeout`),
which logs under `[Install]`. The deprecated `git-timeout`, `cli-probe-timeout`,
`libc-probe-timeout` and `files-read-regular-only` set nothing, whatever their
value. A flag value that does not parse still exits 2:

| Value shape | Configuration key in `claustrum.conf` | `-…` flag on the argv |
|-------------|--------------------------------|-----------------------|
| Unparseable (`= 15`) | Dropped silently, with no log. The run proceeds with no deadline | `flag.Duration` rejects it before any mode runs: `invalid value "15" for flag …: parse error` + usage, exit 2, no facts line |
| Negative (`= -1s`) | Dropped silently, with no log and no deadline | Normalized to 0 with an `[Install]` stderr warning, then runs normally (exit 0, facts line printed) |
| Any zero (`0`, `+0`, `-0`, `0s`, `-0m`, `-0.4ns`) | Accepted. It reads as opted-in but the deadline stays off | Accepted, with the deadline off |

The configuration path is silent, and that silence looks like drift. A missing
`__INSTALL_RESULT__` line does not by itself mean a bad flag. `-install` prints no
facts line while it waits for a CLI that does not answer. That is parity, and the
facts line then carries `"cliUnresponsive":true`
([-install](INSTALL.md#-install-ensure-the-agent-cli) has the bounds). Tell the
cases apart on stderr. `parse error` with exit 2 is a bad flag. `cannot resolve
home directory` is a missing home folder.

`files-read-regular-only` is a bool. `-files-read-regular-only=maybe` exits 2 with
the usage. In the space form, `-files-read-regular-only maybe`, the bool does not
take `maybe`. It becomes a positional, parsing stops, and every later flag is
dropped silently.

`files.read` follows `5fd08069`: the refusal `-32602 files.read: not a regular
file`, and an `open <path>: …` text where the open fails. Against `89cb6289` these
are not drift. That build reads such a path, waits, or answers another text. In
the measured cases its texts are these:

- On Linux and macOS, `stat <path>: …` for a symlink loop, a `/` or a further
  component after a regular file, a name that is too long, a NUL byte, and a folder that the
  daemon cannot search.
- On Windows, `CreateFile <path>: …` for `CON`, `CONIN$`, `CONOUT$`, a `\` after a
  regular file, and a name that is too long. A NUL byte gets `Stat <path>: …`.
- On each system, where the open fails, `files.read: path is a directory` for a
  directory and `files.read: file exceeds maxBytes` for a file over the limit.

An `open <path>: resource temporarily unavailable` for a regular file that another
process holds under a lease is not drift either. It comes from the `O_NONBLOCK`
open of claustrum (from the code, not measured). See
[files.read](protocol/files-read.md).

### Triage gotchas: when a probe result is misleading

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
  and [INSTALL.md → -install](INSTALL.md#-install-ensure-the-agent-cli).
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
  cache hit. See [INSTALL.md → -install](INSTALL.md#-install-ensure-the-agent-cli).
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
  log differ too. Against `5fd08069` the call logs differ in these states, with
  equal replies:
  - With `worktreeRoot`, `5fd08069` runs `rev-parse --show-toplevel`, `rev-parse
    --absolute-git-dir` and `worktree list --porcelain -z` before the add, after
    the add and after the checkout. claustrum runs them before the add only
    (measured on Linux and macOS). A second remove of a gone worktree makes fewer
    calls too (measured on Linux).
  - With `existingBranch`, claustrum runs one config listing before the attach
    add, where `5fd08069` runs two. After a failed attach add, claustrum runs one
    listing before the `-b` add, and `5fd08069` none.
  - With a git filter or git-lfs, the read-tree of claustrum can carry filter
    items before its list.

  With `"shadowDirOff":true` the other calls are equal in list, arguments,
  working directory and variables. Without the member these are equal: the
  folder variables and the six heavy values of the light calls of a create. So
  are the `lfs.storage` item of the checkout and the names of the short-lived
  entries. That is measured
  on Linux and macOS in the state `shadow`. On Windows the variables and the
  names are measured in the state `fallback-git`. `githarden.go` holds the
  lists. See [Hardened git calls](PROTOCOL.md#hardened-git-calls).
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
  Windows). After the start test on a `.git` file that names a missing path,
  claustrum runs a config listing and makes no `.probe`, where `5fd08069` runs a
  `rev-parse` against `.probe` (measured on macOS and Windows), with equal frames.
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
  [DAEMON.md → Child records and the reap](DAEMON.md#child-records-and-the-reap-linux-and-macos).
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
- D12 needs a valid zstd body. The decoder answers an invalid body at once, and
  that reads like "no divergence". A zero download timeout frees the body read
  only. The [transport clocks](DIVERGENCES.md#conventions-for-opt-in-divergences)
  and the 60 s wait for the response headers still apply, and that wait is
  parity. See [-install](INSTALL.md#-install-ensure-the-agent-cli).
- A connection reset in the body answers
  `download interrupted after <got>/<total> bytes: <err>`, as on `f6010b97` and
  `89cb6289` (measured on Linux). Another text there is drift. A body that ends
  before its length is not measured, so a difference there is no proof of drift.
- A download that is not a zstd archive answers `decompressing: …` with
  `fetch.bytes` 4 and one progress line, with a right and a wrong checksum, as on
  `5fd08069` (measured on Linux). Another count for such a body is drift.
- The `ldd` libc probe has its 5 s bound, as on the reference since `19f30c46`.
  There is no libc probe off Linux.
- The seven changes of claustrum for `90fca6e6` are not drift, and the drift check
  is quiet on them. [REFERENCE-BUILDS.md](REFERENCE-BUILDS.md) lists them. Three
  change what a client reads. One is that a new worktree does not get the nine
  Claude runtime state names under `.claude/`. `19f30c46` copies eight of them, so
  a file missing from a seeded worktree looks like a regression against that pin. A quiet symbol comparison does not prove
  that nothing changed, so re-check the whole build after the slices merge.
- On macOS the `lsof` reads of the host cleaner, and the longer pass of claustrum
  after a run that it gave up on, are not drift. An `lsof` that does not start,
  and a run that writes to stderr and exits 1, are parity with `89cb6289`
  (measured on macOS). See [The `lsof` reads](DAEMON.md#the-lsof-reads-macos).

Unless an item names another build, the daemon items below are measured against
`89cb6289`.

- On Linux and macOS a stranded daemon and its children get no signal from the host cleaner, as on `f6010b97` and
  `89cb6289`. The
  daemon ends by itself. See
  [Signals of the cleaner](DAEMON.md#signals-of-the-cleaner).
- On Windows a daemon that outlives its SSH session and children that outlive a
  killed daemon are not drift, against `f6010b97` and `89cb6289`, one state on
  `89cb6289` only. Descendants that outlive a kill or a `-stop` are not drift
  either. A child is in no job, and a kill ends the direct child only (kill forms
  measured on `89cb6289`). See [The job of the launcher](DAEMON.md#the-job-of-the-launcher-windows)
  and [Windows child trees](protocol/process-kill.md#windows-child-trees).
- On Linux and macOS a daemon accepts no connection while the reap of its start
  waits. A connect then gets `ECONNREFUSED` or `ENOENT`, and `daemon.lock` holds
  `pid`, `role` and `node` only. That is not drift. See
  [The bind after the reap](DAEMON.md#the-bind-after-the-reap-linux-and-macos).
- On Linux and macOS records and both identity markers on a socket that is not
  `run/<id>/rpc.sock` are not drift. Records that stay after a start past 128
  record names or 64 groups are not drift either (measured on Linux). An older claustrum
  build wrote two blanks before a one-digit day in its macOS start texts. On macOS
  a `daemonStart` in that form does not match, so a new daemon reads that live
  daemon as gone, and a `start` in that form does not match either. Neither is
  drift. See
  [Child records and the reap](DAEMON.md#child-records-and-the-reap-linux-and-macos).
- A first process that lives on after a second `process.spawn` took its `id` is
  not drift. It gets no signal, and a stop leaves it alive (measured on Linux, macOS and
  Windows). Its frames still come
  on its own connection (measured on Linux). See
  [A spawn with an `id` that is in use](protocol/process-spawn.md#a-spawn-with-an-id-that-is-in-use).
- A session spawn answers after the old process ended. If the
  command of the new spawn does not exist, the old process gets no signal. A command
  that exists and does not start ends the old process first (measured on Linux).
  None of these is drift. See
  [Session supersede](protocol/process-spawn.md#session-supersede).
- On Linux and macOS, if a child of `process.killAndWait` or a supersede ends on
  the graceful signal within the grace, its group gets no `SIGKILL`. A descendant
  that lives on then is not drift. A process that holds the output pipes
  lives on too, as on `5fd08069`. See
  [process.killAndWait](protocol/process-killandwait.md).
- On Linux and macOS the daemon runs no login shell before its first
  `process.spawn`. When a process of the login profile holds the output pipe of the
  shell, the first spawn waits for the 4 s cap and logs the timed-out PATH
  line (measured on Linux). Neither is drift. See
  [The login-shell PATH](protocol/process-spawn.md#the-login-shell-path-linux-and-macos).
- A `-serve` launcher that gives up after 12 s is not drift, also for a daemon
  that ends before its bind (measured on Linux, macOS and Windows). See [The launcher wait](DAEMON.md#the-launcher-wait).
  A daemon that gets `SIGTERM` or `SIGINT` during its start binds and then exits
  0, and that is not drift (measured on Linux, and on macOS for a `SIGTERM` from a
  second daemon). A daemon that a second daemon ends with `SIGKILL` during
  its start is not drift (measured on Linux). See
  [Eviction of a live holder](DAEMON.md#eviction-of-a-live-holder).
- The order of two or more new caller keys in a child environment is not fixed,
  as on `89cb6289` (measured on Linux). That is not drift. See
  [The environment of the child](protocol/process-spawn.md#the-environment-of-the-child).
- On Windows the environment block of a child is in name order. `f6010b97` and
  `89cb6289` keep the order of the daemon, then `CLAUDE_SSH_DAEMON_CHILD=1`. On
  `89cb6289` an added `PATH` entry and the new caller keys come after it. So the
  stdout frame of `cmd /c set` differs
  in bytes. That is [D23](DIVERGENCES.md#d23).
- A child that `process.spawn` starts as a shutdown begins is ended by claustrum.
  `5fd08069` leaves it running on Linux, macOS and Windows. That is known, not
  drift. See
  [A spawn during a shutdown](protocol/process-spawn.md#a-spawn-during-a-shutdown).
- These log and call differences are known and open. They are not drift.
    - On Linux and macOS, the later `SIGKILL` of a gone leader at the reap of a
      start is [D20](DIVERGENCES.md#d20).
    - On Linux and macOS claustrum logs the exit line `Process <id> exited with
      code -1, terminated by SIGKILL, signalled at shutdown request` for a child
      that a shutdown kills. `89cb6289` logs none, and `5fd08069` logs it on Linux
      only.
    - On Linux and macOS, after a `-stop` during the wait of a supersede,
      claustrum can log the exit line of the old process and the `supersedes`
      line. `89cb6289` logs neither.
    - On Linux `89cb6289` unlinks a child record up to three times at a stop or
      a kill, and claustrum unlinks it once. `89cb6289` also unlinks a record
      that it never wrote. The files left are the same.
    - On macOS `89cb6289` runs one `ps` at the daemon start and one for each
      spawn. claustrum runs none at the start and three for each spawn.
    - With a zero-byte `-token-file`, `89cb6289` logs `claude-ssh: empty token in
      --token-file`, and claustrum logs `claustrum: token is empty`.
    - On Linux, for a frame that cannot reach a closed connection, `89cb6289` logs
      `[frameSink] write failed, detaching: write unix <socket>->@: use of closed network connection`.
      claustrum logs
      `[frameSink] write failed, detaching: use of closed network connection`.
    - On Windows, at a stop with one connection, `89cb6289` can log `closed 0
      connection(s)`, and claustrum logs `closed 1`.
- The level tag and the name `Claustrum` in the listening line differ from the
  reference by design. See [Daemon log](DAEMON.md#daemon-log-remote-serverlog).

## Toolchain-induced drift — the Go 1.27 `jsonv2` hold

Reference drift is not the only way the wire can move. claustrum inherits some of
its frame bytes from Go's `encoding/json`
([ARCHITECTURE.md → Inherited wire bytes](ARCHITECTURE.md#inherited-wire-bytes),
`inherited_encoding_test.go`). So a new Go toolchain can move those bytes while
the reference stays the same.

Go 1.27 does that. It turns on the `jsonv2` GOEXPERIMENT by default, and the v2
`Marshal` renders an invalid UTF-8 byte as the literal U+FFFD character (bytes
`EF BF BD`). Go 1.26 and earlier emit the six-ASCII `\ufffd` escape. The build
pinned today, `5fd08069`, carries a go1.25.14 stamp (`go version` on the binary), as
`89cb6289` does. So it emits the same escape. `files.read` puts raw file bytes
into `content`, so under Go 1.27 every read of a file with a non-UTF-8 byte
diverges from the reference. `TestInheritedInvalidUTF8BecomesReplacementChar` and
`TestInheritedLoneSurrogateBecomesThreeReplacements` fail under 1.27 for this
reason. That is the guard working, not a stale expectation.

Current stance: hold the build toolchain at 1.26.x. The hold lives in the
Renovate preset (`schubydoo/renovate-config` → `claustrum.json`,
`allowedVersions: <1.27` on the `go` dep). 1.26.x patch and security bumps
still flow, while 1.27+ is blocked. That preset constrains `go.mod`, so the hold
reaches only a build path that resolves its toolchain from `go.mod`. Every
`setup-go` step uses `go-version-file: go.mod`. Keep it so. A step that names its
own version leaves the hold (`go-version: stable`) or stops tracking it (a fixed
`1.26.x`). No lint, guard or test reads `setup-go` inputs, so the first signal is
a failing run of that workflow.
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

Both worktree methods refuse a symlink loop in a `worktreeRoot`. A kernel ELOOP
catches a long chain. A root that is itself a link in a loop, or starts a chain of
more than 255 links, rests on the text "EvalSymlinks: too many links" of
`filepath.EvalSymlinks`. If a Go release rewords it, that frame changes, and the
S1, S1b, S1c and S1d cases in `worktree_create_chain_unix_test.go` fail. Check
them after each Go bump.

### Windows junctions in `filepath.EvalSymlinks`

On Windows, the walk of `filepath.EvalSymlinks` fails at a junction before the
last component, since Go 1.23 turned on the `winsymlink` GODEBUG setting (from
the Go source, not measured). These answers rest on that failure:

- The trust-root refusal, claustrum's own, of `git.worktree_create` and
  `git.worktree_remove` for a junction before the last component of a `baseRepo`
  that exists (measured on Windows).
- The `"isRepo": false` answer of `git.status` (`baseRepo`) and
  `git.list_branches` (`path`) for `<dir>\<junction>\T` (from the code).
- The `git.info` answers for a path with a junction before its last component
  (from the code). These are `root`, `repo`, `repoSlug` and the `GIT_COMMON_DIR`
  pin of the trust check ([git.info → Differences by system](protocol/git-info.md#differences-by-system),
  [PROTOCOL.md → Git-directory trust check](PROTOCOL.md#git-directory-trust-check)).

With `GODEBUG=winsymlink=0` in the daemon environment, or a `go` line below 1.23
in `go.mod`, the walk follows the junction. Those answers then flip or move to the
repository behind the junction, and other Windows junction answers can move too.
That is derived from the Go source (`os/types_windows.go` and
`path/filepath/symlink.go`), not measured. After each Go bump that changes
`winsymlink`, check `TestInfoRootLinkThenDotDotWindows`,
`TestInfoRootBelowJunctionWindows` and `TestInfoThroughJunctionWindows`, which pins
the frames of `89cb6289`.

### The git version under the checkout tests

With a `worktreeRoot`, both worktree methods run `git worktree list --porcelain -z`.
The `-z` option needs git 2.36 or later. On an older git that call fails, and both
methods run `git worktree list --porcelain` instead. The references on such a git
are not measured. The tests in `worktree_root_checkout_unix_test.go` that need
`-z` skip there, so a skip in CI means a git older than 2.36. Check them after the
git of a CI runner or a test VM changes.

## Automating it

- [`.github/workflows/upstream-desktop-watch.yml`](https://github.com/schubydoo/claustrum/blob/main/.github/workflows/upstream-desktop-watch.yml)
  runs twice daily (cron `17 6,18 * * *`), with one leg for each of Linux,
  Windows and macOS. Each leg runs Step 1: `scripts/latest-desktop-sha.py
  --platform <p>` finds the SHA that the newest Claude Desktop for that platform
  pins. A new pin differs from `scripts/UPSTREAM_SHA` and matches no build
  heading (a "### `<full sha>`" line) of
  [`REFERENCE-BUILDS.md`](REFERENCE-BUILDS.md). A SHA in the prose does not count,
  so a platform that still pins a reconciled build stays quiet. For a new pin, the
  leg runs `check-upstream.sh <sha>` and opens one tracking issue. While that issue
  is open, a later leg with the same SHA adds a comment to it. A leg skips the
  downloads for a Desktop version that it already analyzed. Reconciliation
  (Step 4) stays a human decision.
- Each platform has its own leg because the platforms ship on separate
  schedules, so one can pin a newer reference build than the others. Linux uses
  the APT `Packages` index (`.deb`). Windows uses the Squirrel `RELEASES` file
  for win32/x64 (`-full.nupkg`). macOS uses `RELEASES.json` for darwin/universal
  (`.zip`). The script checks the `.deb` by SHA-256 and the `.nupkg` by SHA-1 and
  size. The macOS feed publishes no checksum, so TLS is the only integrity check
  there. The script therefore refuses a URL or a redirect that is not HTTPS, for
  every feed and package.
- You can still run `check-upstream.sh` by hand against any SHA. Examples are a
  SHA you just found, or a check that a re-published build did not shift.
