# claustrum

A tiny, dependency-light Go daemon. It is a clean-room reimplementation of
the small daemon that hosts a remote Claude Code session over SSH. One binary
holds three parts: a local CLI-version manager, a process supervisor, and a
JSON-RPC multiplexer with a replay buffer over an `AF_UNIX` socket.

Wire-level probes of the reference binary captured a behavioral contract, and
the daemon implements it. This project copied no code. It also transcribed no
decompiler output into the implementation (see
[`NOTICE`](https://github.com/schubydoo/claustrum/blob/main/NOTICE)).

!!! note "The one hard rule"
    Stay byte-identical to the reference daemon's JSON-RPC frames. The wire
    surface *is* the product.

## What it does

The daemon is one binary. A flag selects the mode:

- `-serve` is the daemon. It opens an `AF_UNIX` listener and runs one read loop
  for each connection. On Linux and macOS it sets the socket file to mode
  `0600`. It dispatches requests concurrently, daemonizes itself, and shuts
  down gracefully.
- `-bridge` is a simple relay between stdio and the socket. An SSH session
  attaches to this mode.
- `-install` is the installer. It downloads the CLI, verifies the SHA-256,
  extracts the zstd archive, and removes the cli-dir entries past the
  `-cli-keep` count. It verifies a local `-cli-zst` blob only with a
  caller-supplied checksum.
- `-probe-cli` runs the bounded `<cli> --version` probe on one CLI binary, and it
  exits 0. If the CLI runs, it prints nothing. If the 30 s deadline killed the
  CLI, it prints `__CLI_HUNG__`. If the CLI is missing or does not run, it prints
  `__CLI_BAD__`. With `CLAUDE_SSH_MANAGED_LAUNCHER=1` it runs the CLI through the
  host's managed launcher, and that run stops at 33 s with `__CLI_HUNG__`. It
  prints `__CLI_LAUNCHER__` for an unusable launcher, unreadable settings or a
  failed launcher run. With no launcher, and on Windows, it runs directly.
- `-stop` sends `server.shutdown`. `-version` reports the build.

The daemon supplies 20 methods across the `server.*`, `files.*`, `git.*`,
`launcher.*`, `process.*`, and `plugins.*` namespaces. Auth is in-band per request. Spawned processes
stream base64 stdout and stderr frames. A client that connects late, or that
connects again, can replay the retained frames with `reattach`. The replay
buffer has a bound of 16 MiB for each process.

## Operational extras

claustrum carries a few claustrum-only operational extras that the wire contract
does not cover. Each extra is either off by default or invisible to clients.
Thus no extra changes the frames that a client sees. For details, see the
[protocol reference](PROTOCOL.md).

- Logging: leveled stderr logging, always on. It emits everything by
  default. `CLAUSTRUM_LOG_LEVEL` only *raises* the threshold and makes the
  daemon quieter. It never turns logging off entirely.
- Metrics: a Prometheus `/metrics` endpoint that `-metrics-addr` supplies.
  It is off by default. No listener exists unless you set the flag.
- Wire log: `-wire-log <path>` appends every JSON-RPC frame to a JSONL file
  for diagnostics. It is off by default, and it has no effect on the wire. It
  captures frame payloads and redacts credentials by key only, so a capture is
  sensitive. See [PROTOCOL.md](PROTOCOL.md).
- Token handoff: `-token-fd` supplies the token on a file descriptor, so
  you write no token file. The daemon still persists `daemon.token` beside the
  socket. See [PROTOCOL.md](PROTOCOL.md).
- `-keep-children` (CT-2) leaves spawned processes running at a graceful
  shutdown. They lose their stdio. The flag is off by default. The default
  shutdown kills the whole tree on Linux and macOS, and the direct child on
  Windows.

## Protocol extension

claustrum has one opt-in addition to the result frames. `wantPid` is
claustrum's own parameter. A client that passes
`"wantPid":true` to `process.spawn` or `process.reattach` gets `pid` and
`startTime` in the result. These two fields let the client detect PID reuse
(CT-1).

Without `wantPid`, the two results have no `pid` and no `startTime`. The
[divergence catalog](DIVERGENCES.md) records the addition as CT-1.

## Where to go next

<div class="grid cards" markdown>

- :material-sitemap: **[Architecture](ARCHITECTURE.md)**. The three runtime
  roles, the concurrency and replay model, and how a driver uses it.
- :material-protocol: **[Protocol reference](PROTOCOL.md)**. Every method, its
  params, result shape, and error codes.
- :material-console: **[Examples](EXAMPLES.md)**. Worked client sessions over
  the socket.
- :material-sync: **[Upstream tracking](UPSTREAM-TRACKING.md)**. How the
  project keeps compatibility with the reference daemon in lock-step.
- :material-history: **[Reference builds](REFERENCE-BUILDS.md)**. The history
  of the reference builds, and what each build changed on the wire.
- :material-source-branch: **[Divergences](DIVERGENCES.md)**. Every deliberate
  departure from the reference, its default, and how to activate it.
- :material-format-list-checks: **[Shipped ledger](IMPROVEMENTS.md)**.
  The completed hardening work, one line per item.

</div>

## Safety model

`process.spawn` runs arbitrary commands as the daemon's user by design.
Treat the socket and the token as equivalent to shell access. The
[security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)
holds the full threat model. There is no telemetry, ever.
