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
| `7d193f89…` | 2026-08-25 | 6 changes + off-wire git rewrite — see below | [PR 286](https://github.com/schubydoo/claustrum/pull/286) |
| `5db5e4a1…` | 2026-07-06 | none (off-wire: `daemon.token` persistence) | [PR 131](https://github.com/schubydoo/claustrum/pull/131) |
| `7c2f88d1…` | 2026-07-02 | 5 changes — see below | [PR 120](https://github.com/schubydoo/claustrum/pull/120) |
| `d20a77da…` | 2026-06-09 | none (pure rebuild) | [PR 97](https://github.com/schubydoo/claustrum/pull/97) (pin bump only) |
| `7cbfa471…` | 2026-06-04 | `git.info` gained `root` (+ off-wire churn — see below) | [PR 60](https://github.com/schubydoo/claustrum/pull/60) |
| `8de85faa…` | 2026-05-21 | baseline (initial clean-room target) | initial implementation |

Each per-build section has three parts. The **wire delta** is what claustrum
must match byte-for-byte. **Off-wire churn** is any source that moved but never
reaches the JSON-RPC surface. **How it was bounded** gives the measurement that
confirmed that nothing else changed.

### `7d193f89fc02cf1035a391245312e34ad419f63e` — 2026-08-25

Pinned by Claude Desktop for Linux `1.37937.1`. A large build that rebuilt the
session-worktree surface. Six wire changes, all matched byte-for-byte — the four
below, plus two more (the `git.status` untracked handling and the `worktree_remove`
registration prune) that the off-wire git rewrite surfaced.

**Wire delta.**

1. **`server.version` removed.** The method is gone; calling it now answers
   `-32601 "Unknown method: server.version"`. `server.capabilities` drops it from
   `methods` (18 methods now) and adds two `features`: `git.status.baseRepo` and
   `git.worktree.external_root`. (The `-version` CLI flag is unaffected — it is not
   the RPC method.)
2. **`git.status` requires `baseRepo` and keys off a session worktree.** The
   params are now `{path, baseRepo}`; an absent `baseRepo` answers
   `-32602 "baseRepo is required"`. Status is reported only when `path` is a linked
   worktree whose main repository is `baseRepo` — a plain path, a plain subdirectory,
   a nested repository, the repository root itself, a worktree of a *different*
   repository, and the right worktree named against the wrong `baseRepo` all answer
   the bare `{"isRepo":false,"clean":false}`. The porcelain shape is otherwise
   unchanged.
3. **`git.worktree_create` / `git.worktree_remove` confine worktrees to inside the
   repository.** A `worktreePath` that is not absolute, carries a `..` component, or
   does not sit strictly under `baseRepo` is refused with an `errorCode:"unsafe_path"`
   message ("is a relative path" / "contains a \"..\" component" / "is not inside the
   repository … under `<repository>/.claude/worktrees`"); on create an already-existing
   target is refused as "already exists … a fresh directory". `worktree_remove` applies
   the same location checks (no `errorCode` field). The success shapes are unchanged.
   Create now makes the parent directory before `git worktree add`, so a nested session
   path succeeds on a fresh repository.
4. **`files.list` fails at the open for a non-directory.** A regular file — readable
   or not — now answers `open <p>: not a directory` and an unreadable directory answers
   `open <p>: permission denied`, where the build before said `readdirent <p>: not a
   directory`. On the wire this is one op-name change on the error path.

The empty `worktreePath` messages ("failed to create parent directory: \"\" does not
name a directory" on create, "failed to remove worktree: \"\" does not name a directory"
on remove) are matched too.

Two of these — the `git.status` untracked handling and the `worktree_remove`
registration prune — were found by matching the OFF-wire git-invocation rewrite
(below) rather than by the frame battery, which the top-level untracked and
non-locked-worktree fixtures did not exercise.

- **`git.status` untracked files.** `--untracked-files=all` lists an untracked file
  inside an untracked directory individually (`?? sub/u.txt`) rather than as the
  directory (`?? sub/`).
- **`git.worktree_remove` registration prune.** The removal now drops
  `$GIT_DIR/worktrees/<name>`, so a re-create at the same path succeeds where it
  previously failed `already registered` (the divergence surfaced only on the
  locked-worktree fallback path).

**Off-wire churn.** The Go toolchain moved `go1.23` → `go1.25`, which accounts for
most of the size growth. The build also **rewrote the entire git layer**: every git
operation now runs under one of two `-c` hardening profiles (a light set and a heavy
set forbidding all transport protocols and clearing credential/askpass helpers), with
a `config -z --list --name-only` precursor and config-hook pinning
(`GIT_CONFIG_KEY_0=hook.enabled=false`) injected on each command; `git.status` and
`git.worktree_create` are additionally reimplemented as multi-command plumbing in an
isolated temporary gitdir. claustrum matches the hardening profiles, the precursor,
the base hook pins, and the wire-visible flags across every git method; the
isolated-gitdir status assembly and the read-tree worktree checkout are process-shape
that produces identical output, matched for the observable and security behaviour but
not reproduced command-for-command. Beyond git: a daemon-to-daemon reconnect handoff
and idle-connection management (the SSH-reliability items in the Desktop changelog),
install stale-partial pruning, and a trust-root validation for worktrees rooted
OUTSIDE `.claude/worktrees` (the `git.worktree.external_root` feature). That
external-root path is reachable only when `baseRepo` is itself a managed worktree — a
nested case Desktop's normal usage does not produce, so `git.worktree_create` never
validates a trust root for an ordinary repository. It is a bounded gap rather than
reproduced.

**How it was bounded.** The cross-binary frame battery reports **claustrum ≡
`7d193f89` byte-identical** across the method suite. Every worktree/status/list edge
shape above was additionally probed per-method against the reference on an ephemeral
VM — the containment ordering, the `git.status` worktree gate over six repository
shapes, and the `files.list` open error over file/link/directory and the unreadable
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

**Wire delta.** Method count **18 → 19**. claustrum matches all five changes
byte-for-byte (values and timing) against the reference:

1. **`process.killAndWait`** — a new method, between `process.kill` and
   `process.reattach`. The method blocks until the process is gone. It then
   reports the outcome as a result. Its params are `timeoutMs` (grace) and
   `escalate`. Its result is `{found,died[,alreadyExited][,escalated]}`.
   [PROTOCOL.md → process.killAndWait](PROTOCOL.md#processkillandwait) is
   canonical for the defaults, the grace clamp and the escalation timing.
2. **`process.stdin.offset` contract** — a `process.stdin` reply now always
   carries `applied`, the cumulative count of decoded bytes. An `offset` param
   makes stdin idempotent across reconnects: a duplicate becomes a
   `duplicate:true` no-op, an offset ahead of the applied count gives a `-32003`
   gap error, and a partial overlap applies only the fresh tail.
3. **`process.reattach`** gained `stdinApplied`, the cumulative counter. A client
   that reconnects uses it to continue stdin at the correct offset.
4. **`git.info`** gained `repoSlug` (`owner/repo` from `remote.origin.url`) and
   `defaultBranch` (from `refs/remotes/origin/HEAD`). Both are always present,
   including on the non-repo body. The slug rule requires the host to be
   `github.com` and applies a charset check to both segments; the rule is not
   "exactly two path segments".
   [PROTOCOL.md → git.info](PROTOCOL.md#gitinfo) is authoritative for that rule,
   and its 42-shape table backs it.
5. **`server.capabilities`** gained a `features` array
   (`["process.stdin.offset"]`).

**Off-wire churn.** `git.list_branches` switched to `--sort=refname`, which keeps
the same lexical order. `git.worktree_create` gained a `safeRefName` guard on ref
names. Both changes are verified byte-identical. `git.refs`,
`process.validateGroupKillPid` and `killProcessGroup` are internal symbols, not
wire methods; `server.capabilities` is authoritative on the method set.

**How it was bounded.** The frame battery gates all five changes. Differential
binary analysis confirmed that the off-wire deltas are real source, and not
compiler noise. The forensics stay outside the committed tree. Provenance: Claude
Desktop for Linux 1.18286.0 (2026-07-02) embeds a manifest that pins this SHA,
and it calls 15 of the 19 methods. The uncalled four include
`process.killAndWait`, `server.version` and `server.shutdown` — the `--stop`
CLI drives shutdown, and the client reads the version from `--version` CLI
stdout, not over RPC. A
capture of a real session will therefore not exercise the new method, and the
synthetic battery stays the gate for it. That client also bears on D1's trust
boundary: `--install` carries `--cli-checksum` on the `--cli-url` download. A
later argv capture (2026-08-10, two cold starts on one host) confirms this
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
`root` gives the top level of the repo, even when `path` is a subdirectory.

**Off-wire churn.** Two more app-code changes came with this build. A re-audit on
2026-07-02 examined them. claustrum already covers both, and neither is a missed
divergence:

- The `-install` path gained an HTTPS download, a SHA-256 verify and a robust
  rename. claustrum mirrors the substance: `install.go` verifies SHA-256 with
  `verifyChecksum`, unconditionally on the `-cli-url` path, and downloads with
  `fetchToFile`, which streams to a temp file. claustrum does not mirror one
  detail: the reference's EEXIST-clear before the rename. claustrum uses a plain
  atomic `os.Rename`, which POSIX-replaces a target *file* anyway. This is a
  minor robustness nuance for Windows and for a directory target, and it has no
  wire impact.
- A refactor of the `process.Spawn` failure path — a wire-reachable path. It was
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
