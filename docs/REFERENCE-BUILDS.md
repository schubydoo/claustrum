# Reference build ledger

Every claustrum release tracks one specific reference `claude-ssh` daemon build.
A git SHA identifies the build. [Upstream tracking](UPSTREAM-TRACKING.md)
describes how to find and diff a new build. This page is the running history of
those builds. It records what each build changed on the wire. The reconciliation
trail therefore stays in one place, and not in many commit messages.

[`scripts/UPSTREAM_SHA`](https://github.com/schubydoo/claustrum/blob/main/scripts/UPSTREAM_SHA)
holds the SHA of the build that is pinned now. This ledger gives the reasons
behind each bump.

## Builds

Newest first. A "wire change" is a change to the JSON-RPC surface that claustrum
must match byte-for-byte. A pure rebuild with no wire change also gets a row.
This lets a reader tell a re-published SHA from a real release.

| Reference SHA | Built (UTC) | Wire changes | Reconciled in |
|---|---|---|---|
| `90fca6e6…` | 2026-09-14 (built) | no surface change. 7 behavior changes, 3 of them client-visible and 1 of those on a frame. See below | [PRs 387–392](https://github.com/schubydoo/claustrum/pull/392) |
| `19f30c46…` | 2026-09-11 (observed) | 2 changes + 4 off-wire subsystems + a new CLI mode. See below | [PRs 356–371](https://github.com/schubydoo/claustrum/pull/371) |
| `3ef9370e…` | 2026-09-03 (built) | none (off-wire: linux libc probe reordered ldd-first) | [PR 345](https://github.com/schubydoo/claustrum/pull/345) |
| `4534d86…` | 2026-09-04 (observed) | 3 changes + off-wire lifecycle layer | [PRs 314–333](https://github.com/schubydoo/claustrum/pull/333) |
| `7d193f89…` | 2026-08-25 | 6 changes + off-wire git rewrite. See below | [PR 286](https://github.com/schubydoo/claustrum/pull/286) |
| `5db5e4a1…` | 2026-07-06 | none (off-wire: `daemon.token` persistence) | [PR 131](https://github.com/schubydoo/claustrum/pull/131) |
| `7c2f88d1…` | 2026-07-02 | 5 changes. See below | [PR 120](https://github.com/schubydoo/claustrum/pull/120) |
| `d20a77da…` | 2026-06-09 | none (pure rebuild) | [PR 97](https://github.com/schubydoo/claustrum/pull/97) (pin bump only) |
| `7cbfa471…` | 2026-06-04 | `git.info` gained `root` (+ off-wire churn, described below) | [PR 60](https://github.com/schubydoo/claustrum/pull/60) |
| `8de85faa…` | 2026-05-21 | baseline (initial clean-room target) | initial implementation |

Each per-build section has three parts. The **wire delta** is what claustrum
must match byte-for-byte. **Off-wire churn** is any source that moved but never
reaches the JSON-RPC surface. **How it was bounded** gives the measurement that
showed that nothing else changed.

### `90fca6e6a55c4d4c659e8c6ed511b7969ab17315` — 2026-09-14 (built)

Pinned by Claude Desktop for Linux `2.110.0`. A small build on top of `19f30c46`,
built with the same Go toolchain. It adds nothing to the JSON-RPC surface: no
method, no field, no new error string. The `server.capabilities` method list and
feature list are unchanged, and the static drift check passes against this build
with no change to its canon. The frame battery, run on the reattach slice, showed
no divergence beyond `instanceId` and `startedAt`. Those are per-boot values that
cannot match between two daemons. The slices that landed after it move no frame.

Recorded here anyway, because "the drift check was quiet" is exactly the answer a
future triager must not take for "nothing changed": this build carries seven
behavior changes, and three of them a client can observe.

**Wire delta.** Exactly one item changes the bytes of a frame: item 3, and claustrum
had to change its own frames to stay byte-identical. Items 1 and 4 are observable
off the frame instead. They show in the connection lifecycle and in the seeded
worktree, which is precisely what a frame diff cannot catch.

**The seven.**

1. A reattach closes the connection it replaces. `process.reattach` already
   transferred the frame stream to the reattaching connection. It now also closes
   the connection it took the process from, once, with a logged reason. Before, that
   connection stayed open and never carried another frame for that process, while
   still answering `server.ping`. That was measured on `19f30c46`. This is the build's
   most client-visible change. It is consistent with the Claude Desktop changelog
   note about messages around a disconnect, which is a correlation, not something
   this reconciliation established.
2. The idle close shares the deliberate-close flag with that supersede, so a
   superseded connection is not closed and logged a second time.
3. A reaped process is not running. `process.stdin` and `process.reattach` test
   the reap as well as the running flag, so inside the bounded exit drain a reattach
   answers `running:false` and a fresh-bytes stdin write is refused.
4. A new worktree no longer inherits Claude runtime state. Nine names under
   `.claude/` are skipped by both seeding passes, matched as whole path components
   and case-insensitively.
5. The host cleaner's busy sampler went from a fixed sample count to a 3-second
   window with a two-sample minimum. Its outcome is externally visible in principle:
   a daemon whose client disconnects one second into the sample loses this gate's
   protection under a 3-second window and keeps it under a shorter one. The gate is
   not the whole decision, so losing it is not the same as being retired.
6. The linux orphan reap treats a `/proc` process whose `vsize` is 0 as gone,
   beside the state letter it already read. The reference changed its two readers in
   the process layer, not the host cleaner's own stat read. Claustrum therefore changed
   `childreap_linux.go` and deliberately left `hostclean_linux.go`'s reader alone.
7. The host cleaner waits longer for a stalled `lsof`. It gives a run 15 s where
   `19f30c46` gave it 5 s, and gives up on a wedged one at 17 s rather than 7 s.
   Darwin only. This is the one a full re-check found after the first four slices
   had merged.

The five that change nothing observable: two seams that nothing writes, two worktree
durations that moved without changing value, and one no-op cancel path. They are
recorded rather than reproduced.

**How it was bounded.** A per-function body diff of all six platform targets, which
reports the same change set on each, with three differences. The windows builds
carry no `/proc` paths. One function counts as a shape change on arm64 and a
constant-only change on amd64, which is reported on both arches and hides nothing.
And item 7 is reported on darwin-amd64 alone. The constant pass reads x86
immediate syntax and is blind on arm64. That blind spot is what hid it. The method
and feature lists were read out of both binaries and compared.

Items 1, 3 and 4 were measured on throwaway VMs against both reference binaries, one
fixture per case. Items 2, 5, 6 and 7 are read from the binaries, not
probe-measured. For 5, 6 and 7 the callers sit behind a 5-minute process age and a
30-day idle age, which makes staging them expensive. Each carries a pointer-class
label in the code that implements it.

The post-merge re-check added one step the first pass did not have: diffing every
symbol name in the raw binaries rather than only the recovered function table. That
is what found two functions the table alone did not list.

Item 7 escaped the first pass. A second, full re-check after the first four
PRs had merged is what found it, for the reason named above. A green body diff
bounds less than it appears to. The forensics stay outside the committed tree.

**Reconciled in.** PRs 387 through 390 are the four implementation slices. PR 391
corrects claims those four left wrong. PR 392 adds item 7, brings the host cleaner's
check order into line, and carries the one deliberate exception in this build's
reconciliation: on the macOS busy probe claustrum reads an `lsof` run it gave up on
as busy where the reference reads it as idle ([DIVERGENCES.md](DIVERGENCES.md) D17).
This bump follows all six.

### `19f30c46353dde1606cad1fede73d0e9be222140` — 2026-09-11 (observed)

Pinned by Claude Desktop for Linux `1.52386.0`. The upstream build date is not
published. The date above is the date of capture and pinning. A large
build on top of `3ef9370`. This build has two wire changes, both matched
byte-for-byte, plus four off-wire subsystems, one new off-wire CLI mode, and a
startup file-limit raise.

**Wire delta.**

1. `plugins.prune` added. The method count goes from 18 to 19. This is a new
   method in a new `plugins` namespace. It prunes cached CLI plugin directories
   under the plugin root and reports what it removed. `server.capabilities` lists it
   in `methods`. [PROTOCOL.md → plugins.prune](PROTOCOL.md) holds the frames.
2. `git.worktree_create` gains `existingBranch`. A caller passes
   `existingBranch:"<branch>"`, naming an existing local branch, to reuse it rather
   than create one. The result gains a `branch` field, and `server.capabilities`
   gains the matching feature. An absent or empty value keeps the create-a-branch
   behavior, which is parity.
   [PROTOCOL.md → git.worktree_create](PROTOCOL.md) holds the frames.

**Off-wire.** These source changes move no client-visible frame. Four are subsystems.
One is a CLI mode. One raises the inherited file limit. A closing note covers windows.

- `-probe-cli` CLI mode. A one-shot flag runs a bounded `<cli> --version` probe
  and exits 0. For a CLI that runs it prints nothing. For one that times out it
  prints `__CLI_HUNG__`. For one that is missing or does not run it prints
  `__CLI_BAD__`. Claude Desktop drives it out of band to classify a CLI binary.
- Exec-child trampoline. Under a run-shaped socket the daemon re-execs itself to
  stamp a spawned child with `CLAUDE_SSH_RUN_DIR` and a `CLAUDE_SSH_CHILD` identity.
  Linux and darwin only.
- Orphan-child record. The daemon records each spawned child to
  `<runDir>/children/<pid>.json`, written atomically. Linux and darwin only.
- Orphan reap. At startup the daemon reaps a child that a since-exited daemon of
  this run dir left behind. It first makes sure that the live process is that child. Linux
  and darwin only.
- Host cleaner. A periodic sweep ends stranded sibling daemons and orphaned
  Claude Code process groups under this install's roots. It also tidies stale run
  dirs. Linux and darwin only.
- Inherited file limit. At serve startup the daemon sets its own RLIMIT_NOFILE
  soft limit to min(hard, 65536) so process.spawn children inherit a high open-file
  limit. Linux and darwin only (windows has no RLIMIT_NOFILE).
- Windows. The exec-child, record, reap, and cleaner subsystems are inert on
  windows. Windows ships no
  run-dir lock, so the daemon is never the run-dir lock holder there. It records no
  children, reaps nothing, and stamps no child markers. A windows VM showed this.
  A windows VM showed again that D16 (git.status of a linked worktree) persists on this
  build.

**How it was bounded.** The static drift check passes for `19f30c46`. The 19 methods,
the CLI flags, the `-version` format, and the tracked strings all match. The new
`-probe-cli` flag appears in both binaries. The two wire changes went through the
frame battery. Claustrum reconciled each off-wire subsystem 1:1. A throwaway VM
checked the destructive paths on linux, darwin, and windows. The forensics stay
outside the committed tree.

**Reconciled in.** PRs 356 through 371, with later follow-up parity fixes (the
plugins.prune sibling-daemon classification and the inherited file-limit raise).

### `3ef9370ec5b07a0e728ca5de4137d450e95eb2b6` — 2026-09-03

Observed in Claude Desktop for Linux `1.49585.0`. A near-identical rebuild on top
of `4534d86`. No wire change: the JSON-RPC surface, the 18 methods, the CLI flags
and the `-version` format are byte-identical. One off-wire change, linux only.

**Wire delta.** None.

**Off-wire.** The `-install` libc probe (`detectLibc`, linux only) was reordered.
Build 4534d86 and earlier consulted the musl loader glob (`/lib/ld-musl-*.so.*`)
first and ran `ldd --version` only on a miss. Build 3ef9370 runs `ldd` on every
call and lets its output decide. A "musl" banner reports `musl`. Any other output
reports `glibc`. The loader glob is consulted only after `ldd` produced no
output. The exit code is not consulted. This value is off the JSON-RPC wire, and it
appears only in the `__INSTALL_RESULT__` line. Claustrum still matches it, because
the driver uses `libc` to choose which CLI build to download. See
[DIVERGENCES.md](DIVERGENCES.md) D14 and `install.go` `classifyLibc`.

**How it was bounded.** The static drift check passed. A function-inventory diff
across all six platforms found exactly one changed function (`detectLibc`, linux
only). Darwin and windows carry no `detectLibc` symbol, and their recovered
function inventory is unchanged. Their
binaries differ only by ordinary rebuild churn. String constants, `-help`,
`-install -help` and the `-version` format are unchanged. The reorder was measured
on both reference binaries on a mixed host (glibc `ldd` plus a musl marker):
4534d86 reports `musl`, 3ef9370 reports `glibc`. The forensics stay outside the
committed tree.

### `4534d8648b686881955c6f13baf46ae72ee72f4c` — 2026-09-04 (observed)

Observed in Claude Desktop for Linux `1.46388.2`. The upstream build date is not
published. The date above is the date of capture and pinning. A
lifecycle-focused build on top of `7d193f89`. Three wire changes, all matched
byte-for-byte, plus a large off-wire daemon-lifecycle layer and one new documented
divergence (D16). The RPC method set is unchanged at 18.

**Wire delta.**

1. `server.capabilities` gains two fields and two features. The result adds
   `instanceId` (32 hex characters, per boot) and `startedAt` (Unix milliseconds)
   between `methods` and `features`. `features` gains `git.worktree_create.timeoutMs`
   (at index 2 on every OS) and `server.instance_id` (last on every OS).
   `git.worktree.external_root` is still dropped on Windows only.
2. The process exit frame gains `signal` and `killedBy` on the kill path. A
   process that a client killed, or that a shutdown killed, now reports the
   terminating `signal` and a `killedBy` value (`client` or `shutdown`). The
   `signal` is omitted on Windows, which has no SIGTERM machinery. A normal exit
   is unchanged.
3. `git.worktree_create` gains a caller `timeoutMs`. An integer millisecond
   deadline on the add and checkout. Absent or 0 means no deadline, which is parity.
   On fire it returns `errorCode:"timeout"`. It is create-only.

`git.status` also gained `--attr-source=<empty-tree>` in this cycle, so the repo's
in-repo `.gitattributes` no longer runs a clean filter during status. The first
change line's leading space was reconciled here too. `5db5e4a` dropped that space,
and `7d193f89` and `4534d86` keep it. Claustrum had carried the stale `5db5e4a` trim.

New divergence D16. On Windows the reference's own `git.status` of a linked worktree
errors `-32603 "exit status 128"` (measured). Claustrum reproduces the same
temp-gitdir assembly and returns the status. The reference's Windows failure mechanism
is not yet pinned. An earlier hardcoded-`/tmp` hypothesis is contradicted, because the
reference respects `$TMPDIR`. The behavior is byte-identical on Linux and macOS. On
Windows it is a reachable divergence, with claustrum more correct. See
[DIVERGENCES.md](DIVERGENCES.md) D16.

**Off-wire.** A daemon-lifecycle layer that does not move a client-visible frame. It
holds `-install` download progress plus a 60-second read-idle abort. It also holds a
run-dir lock with an owner record, an orphan-exit self-probe, and session supersede.
It bounds the `git.worktree_create` post-checkout drain. The macOS run-dir holder
check is the always-on divergence D15.

**How it was bounded.** Full frame captures against the new binary, plus VM
measurements on Windows and macOS. Two decompile passes and the three-OS osparity
sweep (`scratch/osparity/results/*-4534d86.json`) ran as well. Linux and macOS are
byte-identical between claustrum and the reference. Windows is byte-identical apart from two things. One is
the documented D16 divergence (git.status of a linked worktree). The other is the
shared Windows worktree-timeout timing race, which the reference exhibits too.

**Reconciled in.** PRs 314–333.

### `7d193f89fc02cf1035a391245312e34ad419f63e` — 2026-08-25

Pinned by Claude Desktop for Linux `1.37937.1`. A large build that rebuilt the
session-worktree surface. Seven wire changes, all matched byte-for-byte. Four are
listed below. The other three are the `git.status` untracked handling, the
`worktree_remove` registration prune, and the `worktree_remove` locked-worktree
refusal. The off-wire git rewrite and VM probes surfaced those three.

**Wire delta.**

1. `server.version` removed. The method is gone. Calling it now answers
   `-32601 "Unknown method: server.version"`. `server.capabilities` drops it from
   `methods` (18 methods now) and adds two `features`: `git.status.baseRepo` and
   `git.worktree.external_root`. (The `-version` CLI flag is unaffected. It is not
   the RPC method.)
2. `git.status` requires `baseRepo` and keys off a session worktree. The
   params are now `{path, baseRepo}`. An absent `baseRepo` answers
   `-32602 "baseRepo is required"`. Status is reported for one case only: `path` is a
   linked worktree whose main repository is `baseRepo`. A plain path, a plain
   subdirectory and a nested repository all answer the bare
   `{"isRepo":false,"clean":false}`. So do the repository root itself, a worktree of
   a *different* repository, and the right worktree named against the wrong
   `baseRepo`. The porcelain shape is otherwise
   unchanged, except for the first change line's leading space. 7d193f89 stopped
   trimming it. 5db5e4a returned entry 0 without it, while 7d193f89 and 4534d86 return
   every line verbatim. That was measured, and claustrum reconciled it in a later fix.
3. `git.worktree_create` and `git.worktree_remove` confine worktrees to inside the
   repository. A supplied `worktreeRoot` is the exception. See the `external_root`
   feature in the off-wire notes below. A `worktreePath` that is not absolute, carries
   a `..` component, or does not sit strictly under `baseRepo` is refused with an
   `errorCode:"unsafe_path"` message. The messages are "is a relative path",
   "contains a \"..\" component", and "is not inside the
   repository … under `<repository>/.claude/worktrees`". On create an already-existing
   target is refused as "already exists … a fresh directory". Both also refuse a path
   that crosses a symlinked component under the repo (a planted `.claude` /
   `.claude/worktrees` link) with `errorCode:"symlinked_component"` on create. The
   create therefore cannot escape, and the remove fallback's `os.RemoveAll` cannot
   follow the link out of the repo. `worktree_remove` applies the same location checks
   (no `errorCode` field). The success shapes are unchanged. Create now makes the
   parent directory before `git worktree add`, so a nested session path succeeds on a
   fresh repository.
4. `files.list` fails at the open for a non-directory. A regular file, readable
   or not, now answers `open <p>: not a directory`. An unreadable directory answers
   `open <p>: permission denied`. The build before said `readdirent <p>: not a
   directory`. On the wire this is one op-name change on the error path.

The empty `worktreePath` messages ("failed to create parent directory: \"\" does not
name a directory" on create, "failed to remove worktree: \"\" does not name a directory"
on remove) are matched too.

Three of these were found by matching the OFF-wire git-invocation rewrite (below)
and by VM probes, rather than by the frame battery. Those three are the `git.status`
untracked handling, the `worktree_remove` registration prune, and the
`worktree_remove` locked-worktree refusal. The frame battery's top-level-untracked
and non-locked-worktree fixtures did not exercise them.

- `git.status` untracked files. `--untracked-files=all` lists an untracked file
  inside an untracked directory individually (`?? sub/u.txt`) rather than as the
  directory (`?? sub/`).
- `git.worktree_remove` refuses a LOCKED worktree. A locked worktree now answers
  `{"success":false,"error":"refusing to remove worktree: <p> is locked (git worktree
  lock); unlock it to remove it"}` (message fixed regardless of the lock reason) and
  the directory survives. Pre-`7d193f89` the reference DELETED it via the recursive
  fallback and answered `{"success":true}`. Any OTHER non-zero git exit (an ordinary
  directory, a non-repo `baseRepo`) still reaches that fallback. Measured on an
  ephemeral VM against `7d193f89`. The frame battery never removes a locked worktree.
- `git.worktree_remove` registration prune. A completed removal drops
  `$GIT_DIR/worktrees/<name>`, so a re-create at the same path succeeds where it
  previously failed `already registered`.

**Off-wire churn.** The Go toolchain moved `go1.23` → `go1.25`, which accounts for
most of the size growth. The build also rewrote the entire git layer. Every git
operation now runs under one of two `-c` hardening profiles. One is a light set. The
other is a heavy set that forbids all transport protocols and clears the credential
and askpass helpers. Each command also carries a `config -z --list --name-only`
precursor and config-hook pinning (`GIT_CONFIG_KEY_0=hook.enabled=false`).
`git.status` and
`git.worktree_create` are additionally reimplemented as multi-command plumbing in an
isolated temporary gitdir.

claustrum matches the hardening profiles, the precursor,
the base hook pins, and the wire-visible flags across `git.status`, `git.info`,
`git.list_branches`, and `git.worktree_create`. The isolated-gitdir status assembly and
the read-tree worktree checkout are process-shape that produces identical output. They
are matched for the observable and security behavior, but not reproduced
command-for-command.

`git.worktree_remove` is an off-wire exception. The reference implements it as a
hardened `rev-parse --absolute-git-dir`. It then deletes the worktree and its
`$GIT_DIR/worktrees/<name>` registration directly from the filesystem. It then deletes
the branch with a hardened `update-ref`. It runs no `git worktree remove` at all. Claustrum instead runs an unhardened
`git worktree remove --force` with an `os.RemoveAll` fallback for the worktree itself.
Its branch delete now matches the reference: a raw `update-ref --no-deref -d`. It was
`git branch -D`, which also dropped the branch's `[branch "<name>"]` config section, where
the reference leaves it. Both now spare it, measured against 4534d86. Both yield
byte-identical frames on every probed case (locked refusal, non-worktree delete, stale
re-create, normal removal). The residual difference is the worktree-removal mechanism,
and that difference is off-wire.

Beyond git, the build adds a daemon-to-daemon reconnect handoff
and idle-connection management (the SSH-reliability items in the Desktop changelog).
It also adds install stale-partial pruning and the `git.worktree.external_root`
feature. With a client-supplied `worktreeRoot` on a unix host, the session worktree is
placed OUTSIDE the repository, under `<worktreeRoot>/<directory>/<name>`. On Windows
the reference gates this capability off. It refuses any
`worktreeRoot` with "a custom worktree location is not supported on Windows hosts yet".
Containment guards the placement: absolute, no `..`,
exactly two levels under the root. An ownership and writability check runs on the
root. The `<directory>` level must start out empty. A 285-byte
`.claude-managed-worktrees` marker is written at that `<directory>` level. A separate
check refuses a `baseRepo` that itself sits under a managed-worktrees marker
(`errorCode:"nested_base_repo"`). Both are reproduced and measured byte-for-byte
against `7d193f89` on an ephemeral VM. That VM covered the external-worktree refusals,
the marker body, and the nested-base-repo refusal through a planted marker.

**How it was bounded.** The cross-binary frame battery reports claustrum ≡
`7d193f89` byte-identical across the method suite. Every worktree, status and list edge
shape above was additionally probed per-method against the reference on an ephemeral
VM. Those probes covered the containment ordering and the `git.status` worktree gate
over six repository shapes. They also covered the `files.list` open error over a file,
a link and a directory, plus the unreadable
cases.

### `5db5e4a12f88487e47c2c48259b69a2d630bb3f7` — 2026-07-06

**Wire delta.** None.

**Off-wire churn.** The daemon now persists its auth token to `daemon.token`
(mode `0600`) in the socket's directory. At startup it writes the file
atomically: an `os.CreateTemp("daemon.token-*")` and then a rename. At graceful
shutdown it unlinks the file. A client can therefore reconnect to a daemon that
already runs, and authenticate again after the original `-token-file` was
unlinked or the `-token-fd` pipe closed. claustrum matches this in
`tokenpersist.go`, wired through `runServe` and `teardown`.
[PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken)
holds the wire contract.

**How it was bounded.** The frame battery and differential binary analysis
against `7c2f88d` both point to token persistence and to nothing else. No other
method or phase changed. The forensics stay outside the committed tree.

### `7c2f88d13e5f269762dd4d463aa4eb3102214110` — 2026-07-02

**Wire delta.** Method count 18 → 19. claustrum matches all five changes
byte-for-byte (values and timing) against the reference:

1. `process.killAndWait` is a new method, between `process.kill` and
   `process.reattach`. The method blocks until the process is gone. It then
   reports the outcome as a result. Its params are `timeoutMs` (grace) and
   `escalate`. Its result is `{found,died[,alreadyExited][,escalated]}`.
   [PROTOCOL.md → process.killAndWait](PROTOCOL.md#processkillandwait) is
   canonical for the defaults, the grace clamp and the escalation timing.
2. The `process.stdin.offset` contract changed. A `process.stdin` reply now always
   carries `applied`, the cumulative count of decoded bytes. An `offset` param
   makes stdin idempotent across reconnects. A duplicate becomes a
   `duplicate:true` no-op. An offset ahead of the applied count gives a `-32003`
   gap error. A partial overlap applies only the fresh tail.
3. `process.reattach` gained `stdinApplied`, the cumulative counter. A client
   that reconnects uses it to continue stdin at the correct offset.
4. `git.info` gained `repoSlug` (`owner/repo` from `remote.origin.url`) and
   `defaultBranch` (from `refs/remotes/origin/HEAD`). Both are always present,
   including on the non-repo body. The slug rule requires the host to be
   `github.com` and applies a charset check to both segments. The rule is not
   "exactly two path segments".
   [PROTOCOL.md → git.info](PROTOCOL.md#gitinfo) is authoritative for that rule,
   and its 42-shape table backs it.
5. `server.capabilities` gained a `features` array
   (`["process.stdin.offset"]`).

**Off-wire churn.** `git.list_branches` switched to `--sort=refname`, which keeps
the same lexical order. `git.worktree_create` gained a `safeRefName` guard on ref
names. Both changes are measured byte-identical. `git.refs`,
`process.validateGroupKillPid` and `killProcessGroup` are internal symbols, not
wire methods. `server.capabilities` is authoritative on the method set.

**How it was bounded.** The frame battery gates all five changes. Differential
binary analysis showed that the off-wire deltas are real source, and not
compiler noise. The forensics stay outside the committed tree. Provenance: Claude
Desktop for Linux 1.18286.0 (2026-07-02) embeds a manifest that pins this SHA,
and it calls 15 of the 19 methods. The uncalled four include
`process.killAndWait`, `server.version` and `server.shutdown`. The `--stop`
CLI drives shutdown, and the client reads the version from `--version` CLI
stdout, not over RPC. A
capture of a real session will therefore not exercise the new method, and the
synthetic battery stays the gate for it. That client also bears on D1's trust
boundary: `--install` carries `--cli-checksum` on the `--cli-url` download. A
later argv capture (2026-08-10, two cold starts on one host) shows this
independently. [D1](DIVERGENCES.md#d1) records what Desktop supplies on the
`--cli-zst` SFTP fallback, and the limits of that record.

### `d20a77da22b7d4822f758654b226299ad7021c22` — 2026-06-09

**Wire delta.** None (pure rebuild).

**Off-wire churn.** The only source delta was an internal `ccd-cli-version`
cache-existence check in the `-install` bootstrap. This check is off the JSON-RPC
wire.

**How it was bounded.** The full frame battery stayed byte-identical. The pin
bump needed no code changes.

### `7cbfa471529b0dd33a5cc2f69c41c11bfe7fef6f` — 2026-06-04

**Wire delta.** `git.info` gained `root`, from `git rev-parse --show-toplevel`.
`root` gives the top level of the repo. That holds for a `path` that is a
subdirectory.

**Off-wire churn.** Two more app-code changes came with this build. A re-audit on
2026-07-02 examined them. claustrum already covers both, and neither is a missed
divergence:

- The `-install` path gained an HTTPS download, a SHA-256 verify and a rename
  that clears an existing target first. claustrum mirrors the substance:
  `install.go` verifies SHA-256 with
  `verifyChecksum`, unconditionally on the `-cli-url` path, and downloads with
  `fetchToFile`, which streams to a temp file. claustrum does not mirror one
  detail: the reference's EEXIST-clear before the rename. claustrum uses a plain
  atomic `os.Rename`, which POSIX-replaces a target *file* anyway. This is a
  minor difference for Windows and for a directory target, and it has no
  wire impact.
- A refactor of the `process.Spawn` failure path, which is a wire-reachable path. It was
  therefore the killAndWait-shaped risk: a change that the happy-path battery
  never stresses.

**How it was bounded.** The static drift check passed on this build. Only the
byte-for-byte frame battery caught `root`. The lesson: the battery is the
authoritative gate, not the static check. A differential probe on 2026-07-02 ran
the spawn refactor across three failure modes and found it byte-identical. The
refactor kept the synchronous `-32603` and Go-exec-error-string contract that
claustrum already emits (`methods_process.go` `processSpawn` → `codeInternal`).
Generalized lesson: a one-line ledger entry can under-record a build, because an
off-wire change or a failure-path change never surfaces in the frame battery. The
bump procedure therefore compares more than the wire (see
[Upstream tracking](UPSTREAM-TRACKING.md)). The forensics stay outside the
committed tree.

### `8de85faaa11694321e937499a18c7ab88f37c76c` — 2026-05-21

This build is the baseline. The clean-room reimplementation was first built
against it.

## When you bump the pin

Do these three steps after you reconcile a new build.
[Upstream tracking](UPSTREAM-TRACKING.md) gives the reconcile step:

1. Add a row and a detail section here, newest first.
2. Update [`scripts/UPSTREAM_SHA`](https://github.com/schubydoo/claustrum/blob/main/scripts/UPSTREAM_SHA).
3. Document any wire change in [`PROTOCOL.md`](PROTOCOL.md).
