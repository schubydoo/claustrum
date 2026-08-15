# claustrum

A tiny, **dependency-light Go daemon**. It is a clean-room reimplementation of
the small daemon that hosts a remote Claude Code session over SSH. One binary
holds three parts: a local CLI-version manager, a process supervisor, and a
JSON-RPC multiplexer with a replay buffer over an `AF_UNIX` socket.

Wire-level probes of the reference binary captured a behavioral contract, and
the daemon implements it. This project copied no code. It also transcribed no
decompiler output into the implementation (see
[`NOTICE`](https://github.com/schubydoo/claustrum/blob/main/NOTICE)).

!!! note "The one hard rule"
    Stay **byte-identical** to the reference daemon's JSON-RPC frames. The wire
    surface *is* the product.

## What it does

The daemon is one binary. A flag selects the mode:

- **`-serve`** — the daemon. It opens an `AF_UNIX` listener with mode `0600` and
  runs one read loop for each connection. It dispatches requests concurrently,
  daemonizes itself, and shuts down gracefully.
- **`-bridge`** — a simple relay between stdio and the socket. An SSH session
  attaches to this mode.
- **`-install`** — the installer. It downloads the CLI, verifies the SHA-256,
  extracts the zstd archive, and prunes old CLI versions. (It verifies a local
  `-cli-zst` blob only when the caller supplies a checksum —
  [D1](DIVERGENCES.md#d1).)
- **`-stop`** / **`-version`** — `-stop` sends `server.shutdown`. `-version`
  reports the build.

The daemon supplies **19 methods** across the `server.*`, `files.*`, `git.*`,
and `process.*` namespaces. Auth is in-band per request. Spawned processes
stream base64 stdout and stderr frames. A client that connects late, or that
connects again, can replay those frames with `reattach`.

## Operational extras

claustrum carries a few claustrum-only operational extras that the wire contract
does not cover. Each extra is either off by default or invisible to clients.
Thus no extra changes the frames that a client sees. For details, see the
[protocol reference](PROTOCOL.md).

- **Logging** — leveled stderr logging, **always on**. It emits everything by
  default. `CLAUSTRUM_LOG_LEVEL` only *raises* the threshold and makes the
  daemon quieter. It never turns logging off entirely.
- **Metrics** — a Prometheus `/metrics` endpoint that `-metrics-addr` supplies.
  It is off by default: no listener exists unless you set the flag.
- **Wire log** — `-wire-log <path>` appends every JSON-RPC frame to a JSONL file
  for diagnostics, off by default and with no effect on the wire. It captures frame
  payloads and redacts credentials by key only, so a capture is sensitive — see
  [PROTOCOL.md](PROTOCOL.md).
- **Token handoff** — `-token-fd` supplies the token on a file descriptor, so
  you write no token file. The daemon still persists `daemon.token` beside the
  socket (see [PROTOCOL.md](PROTOCOL.md)).
- **Windows process kill** — on Windows, Job Objects kill the full tree of child
  processes.
- **`-keep-children`** (CT-2, POSIX-only) — this flag keeps spawned processes
  alive across a graceful shutdown, so the processes survive a daemon restart.
  The flag is off by default, and the default shutdown kills the processes.

## Protocol extension

claustrum has one opt-in protocol extension that clients can see. It is a
deliberate addition, not a reference behavior. A client that passes
`"wantPid":true` to `process.spawn` or `process.reattach` gets `pid` and
`startTime` in the result. These two fields let the client detect PID reuse
(CT-1).

A client that does not opt in sees byte-identical frames. The
[divergence catalog](DIVERGENCES.md) records this extension as a deliberate
divergence.

## Where to go next

<div class="grid cards" markdown>

- :material-sitemap: **[Architecture](ARCHITECTURE.md)** — the three runtime
  roles, the concurrency and replay model, and how a driver uses it.
- :material-protocol: **[Protocol reference](PROTOCOL.md)** — every method, its
  params, result shape, and error codes.
- :material-console: **[Examples](EXAMPLES.md)** — worked client sessions over
  the socket.
- :material-sync: **[Upstream tracking](UPSTREAM-TRACKING.md)** — how the
  project keeps compatibility with the reference daemon in lock-step.
- :material-source-branch: **[Divergences](DIVERGENCES.md)** — every deliberate
  departure from the reference, its default, and how to activate it.
- :material-format-list-checks: **[Shipped ledger](IMPROVEMENTS.md)** —
  the completed hardening work, one line per item.

</div>

## Safety model

`process.spawn` runs arbitrary commands as the daemon's user **by design**.
Treat the socket and the token as equivalent to shell access. The
[security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)
holds the full threat model. There is **no telemetry, ever**.
