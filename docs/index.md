# claustrum

claustrum is a very small Go daemon that has a small number of dependencies.
It is a clean-room reimplementation of the small daemon that operates a remote
Claude Code session through SSH. One binary contains three parts: a local
manager of CLI versions, a process supervisor, and a JSON-RPC multiplexer. The
multiplexer has a replay buffer, and it uses an `AF_UNIX` socket.

[Probes](GLOSSARY.md#probe) of the [reference](GLOSSARY.md#reference) binary on
the [wire](GLOSSARY.md#wire) recorded a behavior contract, and the daemon
obeys this contract. This project copied no code. It also wrote no decompiler
output into the implementation (refer to
[`NOTICE`](https://github.com/schubydoo/claustrum/blob/main/NOTICE)).

!!! note "The one mandatory rule"
    Keep the JSON-RPC frames the same as the frames of the reference daemon,
    byte for byte. The wire surface is the output of this project.

## Functions of the daemon

The daemon is one binary. A flag selects the mode:

- `-serve` is the daemon. It starts an `AF_UNIX` listener and one read loop
  for each connection. On Linux and macOS, it sets the mode of the socket file
  to `0600`. It can do the work for two or more requests at the same time. It
  daemonizes, and it stops with a graceful shutdown.
- `-bridge` is a relay between stdio and the socket. An SSH session connects
  to this mode.
- `-install` is the installer. It downloads the CLI, does a check of the
  SHA-256, and extracts the zstd archive. Then it removes the entries of the
  CLI directory that are more than the `-cli-keep` number. If the caller
  supplies a checksum, it does a check of a local `-cli-zst` blob. If not, it
  does no check of that blob.
- `-probe-cli` does the `<cli> --version` probe on one CLI binary, and its
  exit code is 0. The probe has a time limit. If the CLI operates, it prints
  no text. If the 30 s deadline killed the CLI, it prints `__CLI_HUNG__`. If
  the CLI is not there or does not operate, it prints `__CLI_BAD__`.

    With `CLAUDE_SSH_MANAGED_LAUNCHER=1`, it starts the CLI through the managed
    launcher of the host, and that run stops at 33 s with `__CLI_HUNG__`. It
    prints `__CLI_LAUNCHER__` for a launcher that it cannot use, for managed
    settings that it cannot read, or for a failure of the launcher run. With
    no launcher, and on Windows, it starts the CLI directly.
- `-stop` sends `server.shutdown`. `-version` gives the build.

The daemon supplies 20 methods in the namespaces `server.*`, `files.*`,
`git.*`, `launcher.*`, `process.*`, and `plugins.*`. The authentication is in
each request. A spawned process sends its stdout and its stderr as base64
frames. A client that connects after the daemon sent frames, or that connects
again, can get the kept frames with `reattach`. The replay buffer has a limit
of 16 MiB for each process.

## Functions for operation

claustrum has some functions for operation that the wire contract does not
include. These functions are only in claustrum. Each function is off by
default, or clients cannot find it in the frames. Thus no function changes
the frames that a client gets. For the full data, refer to the
[protocol reference](PROTOCOL.md).

- Logging: The daemon writes log lines with a level to stderr. This function
  is always on. By default, the daemon writes all the lines.
  `CLAUSTRUM_LOG_LEVEL` only increases the threshold, and then the daemon
  writes a smaller number of lines. No value of it stops all logging.
- Metrics: `-metrics-addr` supplies a Prometheus `/metrics` endpoint. It is
  off by default. Unless you set the flag, there is no listener.
- Wire log: `-wire-log <path>` appends each JSON-RPC frame to a JSONL file for
  diagnostics. It is off by default, and it has no effect on the wire. It
  records the payloads of the frames, and it redacts credentials by key only.
  Thus a capture can contain secret data. Refer to [PROTOCOL.md](PROTOCOL.md).
- Token supply: `-token-fd` supplies the token on a file descriptor. Thus
  you write no token file. Also with this flag, the daemon writes
  `daemon.token` in the directory of the socket. Refer to
  [PROTOCOL.md](PROTOCOL.md).
- `-keep-children` (CT-2): With this flag, a graceful shutdown does not kill
  the spawned processes. They have no stdio after the shutdown. The flag is
  off by default. The default shutdown kills the full process tree on Linux
  and macOS, and the direct child on Windows.

## The `wantPid` parameter

claustrum has one optional function that adds data to the response frames. It
is off by default. `wantPid` is a parameter of claustrum. If a client sends
`"wantPid":true` to `process.spawn` or `process.reattach`, the response
contains `pid` and `startTime`. With these two fields, the client can
detect that a different process has the same PID (CT-1).

Without `wantPid`, the two responses have no `pid` and no `startTime`. The
[divergence catalog](DIVERGENCES.md) records this function as CT-1.

## Related documents

<div class="grid cards" markdown>

- :material-sitemap: **[Architecture](ARCHITECTURE.md)**. The three runtime
  roles, the concurrency model, the replay model, and how a
  [driver](GLOSSARY.md#driver) uses the daemon.
- :material-protocol: **[Protocol reference](PROTOCOL.md)**. Each method, with
  its parameters, the format of its response, and its error codes.
- :material-console: **[Examples](EXAMPLES.md)**. Full examples of client
  sessions through the socket.
- :material-sync: **[Upstream tracking](UPSTREAM-TRACKING.md)**. How the
  project stays compatible with the reference daemon.
- :material-history: **[Reference builds](REFERENCE-BUILDS.md)**. The history
  of the reference builds, and the changes that each build made on the wire.
- :material-source-branch: **[Divergences](DIVERGENCES.md)**. Each
  [divergence](GLOSSARY.md#divergence) from the reference, with its default
  and how to activate it.
- :material-format-list-checks: **[Shipped ledger](IMPROVEMENTS.md)**.
  The completed hardening work, with one line for each item.
- :material-book-alphabet: **[Glossary](GLOSSARY.md)**. The project terms and
  their definitions.

</div>

## Safety model

`process.spawn` starts the commands that the caller selects, as the user of
the daemon. The project made this decision. Make the same security decisions
for the socket and the token as for shell access. The
[security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)
contains the full threat model. claustrum has no telemetry, and the project
will not add telemetry.
