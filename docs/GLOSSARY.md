# Glossary

This page defines the project terms. Each entry gives the term, its part of
speech (noun, adjective or verb), and its meaning in these documents.

## battery

Noun. A battery is a set of requests that a script sends to claustrum and to a
reference build. The script then compares the frames of the two daemons. See
[Upstream tracking](UPSTREAM-TRACKING.md#step-3-authoritative-byte-for-byte-recheck-local-only).

## busy

Adjective. A daemon with a client connection is busy. On macOS the host cleaner
learns this from an `lsof` run. If the host cleaner gives up on that run before
a result comes, the daemon is also busy. The host cleaner sends no `SIGTERM` to
a busy daemon. See
[Host cleaner](PROTOCOL.md#host-cleaner-off-wire-linux-and-macos) and
[D17](DIVERGENCES.md#d17).

## cleaner

Noun. The cleaner, or host cleaner, is a part of the daemon on Linux and macOS.
At intervals it stops orphan Claude Code process groups and daemons that are
not in use, and it removes stale run directories. It works for only one layout
of the socket path. See
[Host cleaner](PROTOCOL.md#host-cleaner-off-wire-linux-and-macos).

## containment

Noun. Containment is the rule that a path must be inside a given directory.
For example, `worktreePath` must be inside `baseRepo` and not equal to it. See
[`git.worktree_remove`](PROTOCOL.md#gitworktree_remove).

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

Noun. Eviction is the step in which a new daemon stops the daemon that holds
the same socket. The new daemon does this before it binds the socket. See
[`daemon.lock`](PROTOCOL.md#run-dir-lock-daemonlock).

## guard

Noun. A guard is a test that claustrum runs before an operation that can cause
damage. If the input is dangerous, the guard refuses it. For example,
`wipesHomeDir` is a guard. See [D2](DIVERGENCES.md#d2).

## identity

Noun. This term has two uses:

- The identity of a file or a directory is the value by which the OS
  identifies it. Two paths with the same identity lead to the same object.
- The identity of a process is the set of values by which the daemon knows
  that process, for example its PID together with its start time.
- The identity of a machine is the set of values by which the daemon knows
  the host and its PID namespace.

See [`daemon.lock`](PROTOCOL.md#run-dir-lock-daemonlock).

## parity

Noun. Parity is the condition in which claustrum behaves the same as the
reference. A behavior that has parity is not a divergence. See
[Divergences](DIVERGENCES.md#how-we-decide-the-rules).

## pass

Noun. A pass is one full cycle of an operation through its items. For example,
each run of the host cleaner is a pass. The documents also use the term for
one cycle of measurements. See
[Host cleaner](PROTOCOL.md#host-cleaner-off-wire-linux-and-macos).

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
  See [`-probe-cli`](PROTOCOL.md#-probe-cli-classify-a-cli-binary).
- A probe is a measurement of a reference binary that uses only its inputs
  and its outputs. See
  [Upstream tracking](UPSTREAM-TRACKING.md#which-divergences-a-probe-can-see).

## reconciliation

Noun. Reconciliation is the work that makes claustrum agree with a new
reference build. See
[Upstream tracking](UPSTREAM-TRACKING.md#step-4-reconcile).

## reference

Noun. The reference is the daemon that claustrum reimplements in a clean room.
The project compares each behavior of claustrum with a reference build, and a
git SHA identifies each build. In the title "Protocol reference", the word is
part of the name of a document. See [Reference builds](REFERENCE-BUILDS.md).

## refusal

Noun. A refusal is a result in which an operation does not happen because a
rule, a guard, the OS or another program rejects it. The response or the output
gives the cause. See
[catalog of error texts](PROTOCOL.md#error-string-catalogue).

## seed

Noun. This term has two uses:

- The seed is the set of files that `git.worktree_create` copies into a new
  worktree. See [`git.worktree_create`](PROTOCOL.md#gitworktree_create).
- A seed is an input from which a fuzz test starts. See
  [IMPROVEMENTS.md](IMPROVEMENTS.md).

## sleeper

Noun. A sleeper is a test process that only waits. See
[`process.killAndWait`](PROTOCOL.md#processkillandwait).

## spelling

Noun. The spelling of a path is its sequence of characters. Two different
spellings can lead to the same file. See
[Path handling](PROTOCOL.md#path-handling).

## stage

Noun and verb. This term has three uses:

- To stage a file is the git operation that adds a change to the index.
- A staging file or a staging name is a temporary file or a temporary name
  that an operation uses until it completes. For example, `-install` writes
  the CLI to a staging file and then renames it. See
  [Architecture](ARCHITECTURE.md#1-cli-version-manager-install).
- A staged fixture is a test state that a person prepares before a
  measurement.

## stale

Adjective. A stale object is left over from an operation or a daemon that
stopped, and it is not in use. Examples are a stale socket file, a stale run
directory, and a stale worktree entry. See
[Host cleaner](PROTOCOL.md#host-cleaner-off-wire-linux-and-macos).

## stamp

Noun. A stamp is a small piece of data that a program puts on an object to
identify it. A build stamp identifies the build or the toolchain of a binary.
A time stamp gives the time of a log line. See
[Reference builds](REFERENCE-BUILDS.md).

## success

Noun. This term has two uses:

- A success is a response that contains `"success":true`. See
  [Message shapes](PROTOCOL.md#message-shapes).
- Success is the result of an operation that completes with no error.

## sweep

Noun. A sweep is an operation that goes through all the entries of a directory
or a list and removes the entries that a rule selects. Examples are the sweeps
of `-install` in the CLI directory and the sweep of the host cleaner. See
[Architecture](ARCHITECTURE.md#1-cli-version-manager-install).

## undo

Noun. The undo is the rollback of `git.worktree_create`. If the rollback
cannot complete, the daemon adds an undo text to the error. See
[`git.worktree_create`](PROTOCOL.md#gitworktree_create).

## wire

Noun. The wire is the byte stream of the JSON-RPC connection between a client
and the daemon. Data on the wire is in a frame. A behavior that is off the wire
changes no frame. The wire surface is the set of all the methods and frames that
a client can see on the wire. See [Transport](PROTOCOL.md#transport).
