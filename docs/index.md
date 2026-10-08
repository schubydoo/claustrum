# claustrum

A tiny, dependency-light Go daemon. It is a clean-room reimplementation of
the small daemon that hosts a remote Claude Code session over SSH. One binary
holds three parts: a local CLI-version manager, a process supervisor, and a
JSON-RPC multiplexer with a replay buffer over an `AF_UNIX` socket.

Wire-level [probes](GLOSSARY.md#probe) of the [reference](GLOSSARY.md#reference)
binary captured a behavioral contract, and the daemon implements it. This
project copied no code. It also transcribed no decompiler output into the
implementation (see
[`NOTICE`](https://github.com/schubydoo/claustrum/blob/main/NOTICE)).

!!! note "The one hard rule"
    Stay byte-identical to the JSON-RPC frames of the reference daemon. The
    [wire](GLOSSARY.md#wire) surface *is* the product.

## What it does

The daemon is one binary. A flag selects the mode:

- `-serve` is the daemon. It opens an `AF_UNIX` listener and runs one read loop
  for each connection. It sets the socket file to mode `0600`. That mode is
  owner-only on Linux and macOS, and it is not an owner-only ACL on Windows. It
  dispatches requests concurrently, daemonizes itself, and shuts down
  gracefully.
- `-bridge` is a simple relay between stdio and the socket. An SSH session
  attaches to this mode.
- `-install` is the installer. It downloads the CLI, makes sure that the
  SHA-256 matches, and extracts the zstd archive. See
  [`-install`](PROTOCOL.md#-install-ensure-the-agent-cli).
- `-probe-cli` runs `<cli> --version` on one CLI binary with a deadline. If
  the CLI hangs or does not run, it prints a marker. See
  [`-probe-cli`](PROTOCOL.md#-probe-cli-classify-a-cli-binary).
- `-stop` sends `server.shutdown`. `-version` reports the build.

The daemon supplies 20 methods across the `server.*`, `files.*`, `git.*`,
`launcher.*`, `process.*`, and `plugins.*` namespaces. Each request carries its
own authentication in the `auth` member. `server.shutdown` is the one method
that is not authenticated. Spawned processes stream base64 stdout and stderr
frames. A client that connects late, or that connects again, can
replay the retained frames with `reattach`. The replay buffer has a bound of
16 MiB for each process.

## Operational extras

claustrum carries a few claustrum-only operational extras that the wire contract
does not cover. Each extra is either off by default or invisible to clients.
Thus no extra changes the frames that a client sees. For details, see the
[protocol reference](PROTOCOL.md).

- Logging: leveled stderr logging, always on. It emits everything by
  default. `CLAUSTRUM_LOG_LEVEL` only *raises* the threshold and makes the
  daemon quieter. It never turns logging off entirely.
- Metrics: a Prometheus `/metrics` endpoint that `-metrics-addr` supplies.
  It is off by default. If you do not set the flag, no listener exists.
- Wire log: `-wire-log <path>` appends every JSON-RPC frame to a JSONL file
  for diagnostics. It is off by default, and it has no effect on the wire. It
  captures frame payloads and redacts credentials by key only, so a capture is
  sensitive. See [PROTOCOL.md](PROTOCOL.md).
- Token handoff: `-token-fd` supplies the token on a file descriptor, so
  you write no token file. The daemon also writes `daemon.token` beside the
  socket with each token flag. See [PROTOCOL.md](PROTOCOL.md).
- `-keep-children` (CT-2) leaves spawned processes running at a graceful
  shutdown. They lose their stdio. The flag is off by default. The default
  shutdown kills the whole tree on Linux and macOS, and the direct child on
  Windows.

## Protocol extension

claustrum has one opt-in addition to the response frames. `wantPid` is
claustrum's own parameter. If a client passes `"wantPid":true` to
`process.spawn` or `process.reattach`, the response has `pid` and `startTime`.
These two fields let the client detect PID reuse (CT-1).

Without `wantPid`, the two responses have no `pid` and no `startTime`. The
[divergence catalog](DIVERGENCES.md) records the addition as CT-1.

## Where to go next

<div class="grid cards" markdown>

- :material-sitemap: **[Architecture](ARCHITECTURE.md)**. The three runtime
  roles, the concurrency and replay model, and how a
  [driver](GLOSSARY.md#driver) uses it.
- :material-protocol: **[Protocol reference](PROTOCOL.md)**. Every method, its
  parameters, response shape, and error codes.
- :material-console: **[Examples](EXAMPLES.md)**. Worked client sessions over
  the socket.
- :material-sync: **[Upstream tracking](UPSTREAM-TRACKING.md)**. How the
  project keeps compatibility with the reference daemon in lock-step.
- :material-history: **[Reference builds](REFERENCE-BUILDS.md)**. The history
  of the reference builds, and what each build changed on the wire.
- :material-source-branch: **[Divergences](DIVERGENCES.md)**. Every deliberate
  [divergence](GLOSSARY.md#divergence) from the reference, its default, and how
  to activate it.
- :material-format-list-checks: **[Shipped ledger](IMPROVEMENTS.md)**.
  The completed hardening work, one line per item.
- :material-book-alphabet: **[Glossary](GLOSSARY.md)**. The project terms and
  their definitions.

</div>

## Safety model

`process.spawn` runs arbitrary commands as the user of the daemon by design.
Treat the socket and the token as equivalent to shell access. The
[security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)
holds the full threat model. There is no telemetry, ever.
