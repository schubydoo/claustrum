# Glossary

This page defines the project terms. Each entry gives the part of speech and
the meaning of a term in these documents.

## battery

Noun. A battery is a set of requests that a script sends to claustrum and to a
reference build, to compare the frames of the two daemons. See
[Upstream tracking](UPSTREAM-TRACKING.md#step-3-authoritative-byte-for-byte-recheck-local-only).

## blob

Noun. The blob is the zstd archive of the CLI in the file that `-cli-zst`
names. See [`-install`](INSTALL.md#-install-ensure-the-agent-cli).

## busy

Adjective. A daemon that the host cleaner reads as in use, for example with a
client connection, is busy. The host cleaner sends no `SIGTERM` to a busy
daemon. See
[Host cleaner](DAEMON.md#host-cleaner-off-wire-linux-and-macos).

## cleaner

Noun. The cleaner, or host cleaner, is a part of the daemon on Linux and macOS,
for one socket layout only. At intervals it stops orphan Claude Code process
groups and daemons that are not in use, and it removes stale run directories.
See [Host cleaner](DAEMON.md#host-cleaner-off-wire-linux-and-macos).

## cli-dir

Noun. The cli-dir is the folder of the CLI version files of `-install`. The
flag `-cli-dir` names it, with the default `<home>/.claude/remote/ccd-cli`. See
[`-install`](INSTALL.md#-install-ensure-the-agent-cli).

## containment

Noun. Containment is the rule that a path must be inside a given directory.
For example, `worktreePath` must be inside `baseRepo` and not equal to it. See
[`git.worktree_remove`](protocol/git-worktree-remove.md#where-the-paths-can-be).

## divergence

Noun. A divergence is a difference from the reference that claustrum has by
decision. Each divergence has an entry in the
[divergence catalog](DIVERGENCES.md#catalog).

## drift

Noun. Drift is a difference from the reference that the project did not choose.
A new reference build or a new toolchain can cause drift. See
[Upstream tracking](UPSTREAM-TRACKING.md#step-2-run-the-drift-check).

## driver

Noun. A driver is the client program that controls the daemon. Claude Desktop
is a driver. See
[Architecture](ARCHITECTURE.md#deployment-lifecycle-how-a-driver-uses-it).

## eviction

Noun. Eviction is the step in which a new daemon, before it binds the socket,
stops the daemon that holds the same socket. It runs on Linux
and macOS only. See
[`daemon.lock`](DAEMON.md#run-dir-lock-daemonlock).

## facts line

Noun. The facts line is the `__INSTALL_RESULT__<json>` line that `-install`
prints. It also carries a failure, in `cliError`. See
[`__INSTALL_RESULT__` facts](INSTALL.md#__install_result__-facts).

## guard

Noun. A guard is a test that claustrum runs before an operation that can cause
damage. If the input is dangerous, the guard refuses it. `wipesHomeDir` is
one. See [D2](DIVERGENCES.md#d2).

## held git directory

Noun. In the state `shadow`, `git.worktree_create` holds the git directory of
`baseRepo` open until it replies. See
[The held git directory](protocol/git-worktree-create.md#the-held-git-directory).

## identity

Noun. This term has three uses:

- The identity of a file or a directory is the value by which the OS
  identifies it. Two paths with the same identity lead to the same object.
- The identity of a process is the set of values by which the daemon knows
  that process, for example its PID together with its start time.
- The identity of a machine is the set of values by which the daemon knows
  the host and its PID namespace.

See [`daemon.lock`](DAEMON.md#run-dir-lock-daemonlock).

## leaf

Noun. The leaf is the worktree directory itself, the last component of
`worktreePath`. See
[`git.worktree_remove`](protocol/git-worktree-remove.md#where-the-paths-can-be).

## managed launcher

Noun. A managed launcher is a program that runs the Claude Code CLI on this
host. On Linux and macOS the host administrator sets it in the managed settings
files. See
[`launcher.resolve`](protocol/launcher-resolve.md).

## orphan

Noun. This term has two uses:

- On Linux and macOS an orphan is a child process group whose daemon is gone.
  See [Passes, orphans and stale run dirs](DAEMON.md#passes-orphans-and-stale-run-dirs).
- An orphaned, or stranded, daemon is one whose socket path no longer leads to
  it. See [Orphan-exit self-probe](DAEMON.md#orphan-exit-self-probe).

## parity

Noun. Parity is the condition in which claustrum behaves the same as the
reference. A behavior that has parity is not a divergence. See
[Divergences](DIVERGENCES.md#how-we-decide-the-rules).

## pass

Noun. A pass is one full cycle of an operation through its items. For example,
each run of the host cleaner is a pass. See
[Host cleaner](DAEMON.md#host-cleaner-off-wire-linux-and-macos).

## pin

Noun. This term has two uses:

- The pin is the one reference build that the project compares claustrum
  against at this time. The file `scripts/UPSTREAM_SHA` holds its SHA. See
  [Reference builds](REFERENCE-BUILDS.md#when-you-bump-the-pin).
- A pin is a value that the daemon gives to a git call and that the git
  configuration cannot change. Examples are the `GIT_COMMON_DIR` pin and the
  hook pins. See [Hardened git calls](PROTOCOL.md#hardened-git-calls).

## probe

Noun. This term has two uses:

- A probe is a test that claustrum runs on a program to find its state.
  Examples are the `<cli> --version` probe of `-probe-cli` and the libc probe.
  See [`-probe-cli`](INSTALL.md#-probe-cli-classify-a-cli-binary).
- A probe is a measurement of a reference binary that uses only its inputs
  and its outputs. See
  [Upstream tracking](UPSTREAM-TRACKING.md#which-divergences-a-probe-can-see).

## reconciliation

Noun. Reconciliation is the work that makes claustrum agree with a new
reference build. See
[Upstream tracking](UPSTREAM-TRACKING.md#step-4-reconcile).

## reference

Noun. The reference is the daemon that claustrum reimplements in a clean room.
A git SHA identifies each reference build. In "protocol reference" the phrase
names the document PROTOCOL.md. See [Reference builds](REFERENCE-BUILDS.md).

## refusal

Noun. A refusal is a result in which an operation does not happen because a
rule, a guard, the OS or another program rejects it. See
[catalog of error texts](PROTOCOL.md#error-string-catalogue).

## registration

Noun. A registration is the entry `<git dir>/worktrees/<name>` of a linked
worktree. Its `gitdir` record names the `.git` file of the worktree. See
[After a failure](protocol/git-worktree-create.md#after-a-failure).

## run dir

Noun. The run dir is the folder of the socket path. It holds `daemon.token`.
On Linux and macOS it also holds `daemon.lock` and the child records. See
[`daemon.lock`](DAEMON.md#run-dir-lock-daemonlock).

## seed

Noun. This term has two uses:

- The seed is the set of ignored files that `git.worktree_create` copies into
  a new worktree. See
  [The copy of ignored files](protocol/git-worktree-create.md#the-copy-of-ignored-files).
- A seed is an input from which a fuzz test starts. See
  [IMPROVEMENTS.md](IMPROVEMENTS.md).

## settings copy

Noun. The daemon's own copy of a repository's git settings. See
[The settings copy](PROTOCOL.md#the-settings-copy).

## spelling

Noun. The spelling of a path is its sequence of characters. Two different
spellings can lead to the same file. See
[Path handling](PROTOCOL.md#path-handling).

## stage

Noun and verb. This term has two uses:

- To stage a file is the git operation that adds a change to the index.
- A staging file or name is a temporary one that an operation uses until it
  completes. For example, `-install` writes the CLI to a staging file and then
  renames it. See
  [`-install`](INSTALL.md#-install-ensure-the-agent-cli).

## stale

Adjective. A stale object is left over from an operation or a daemon that
stopped, and it is not in use. Examples are a stale socket file, a stale run
directory, and a stale worktree entry. See
[Host cleaner](DAEMON.md#host-cleaner-off-wire-linux-and-macos).

## stamp

Noun. A stamp is a small piece of data that a program puts on an object to
identify it. A build stamp identifies the build or the toolchain of a binary.
See [Reference builds](REFERENCE-BUILDS.md).

## state

Noun. The value of `settingsControl`: `shadow`, `fallback-git` or `off-switch`.
See [The settings copy](PROTOCOL.md#the-settings-copy).

## success

Noun. A success is a response that contains `"success":true`. See
[Message shapes](PROTOCOL.md#message-shapes).

## sweep

Noun. A sweep is an operation that goes through all the entries of a directory
or a list and removes the entries that a rule selects. Examples are the sweeps
of `-install` in the CLI directory and the sweep of the host cleaner. See
[`-install`](INSTALL.md#-install-ensure-the-agent-cli).

## undo

Noun. The undo is the rollback of `git.worktree_create`. If the rollback
cannot complete, the daemon adds an undo text to the error. See
[After a failure](protocol/git-worktree-create.md#after-a-failure).

## wire

Noun. The wire is the byte stream of the JSON-RPC connection between a client
and the daemon. A behavior that is off the wire changes no frame. The wire
surface is the set of all the methods and frames that a client can see on the
wire. See [Transport](PROTOCOL.md#transport).
