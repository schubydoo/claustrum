# claustrum

A tiny, **dependency-light Go daemon** — a clean-room reimplementation of the
small daemon that hosts a remote Claude Code session over SSH. It is, in one
binary: a local CLI-version manager, a process supervisor, and a JSON-RPC
multiplexer (with a replay buffer) over an `AF_UNIX` socket.

It was built to a behavioral contract captured by probing the reference binary
at the wire level — no code was copied, and no decompiler output was
transcribed into the implementation (see
[`NOTICE`](https://github.com/schubydoo/claustrum/blob/main/NOTICE)).

!!! note "The one hard rule"
    Stay **byte-identical** to the reference daemon's JSON-RPC frames. The wire
    surface *is* the product.

## What it does

The daemon is one binary, mode-switched by flag:

- **`-serve`** — the daemon: an `AF_UNIX` listener (mode `0600`), a per-connection
  read loop, concurrent request dispatch, self-daemonization, and graceful
  shutdown.
- **`-bridge`** — a dumb stdio↔socket relay (what an SSH session attaches to).
- **`-install`** — CLI download / SHA-256 verify / zstd extract / prune.
- **`-stop`** / **`-version`** — send `server.shutdown`; report the build.

It exposes **19 methods** across the `server.*`, `files.*`, `git.*`, and
`process.*` namespaces. Auth is in-band per request; spawned processes stream
base64 stdout/stderr frames that a late or reconnecting client can replay via
`reattach`.

## Operational extras

Beyond the wire contract, claustrum carries a few claustrum-only operational
extras. Each is either default-off or invisible to clients, so none of them
changes the frames a client sees. See the [protocol reference](PROTOCOL.md) for
details.

- **Logging** — leveled stderr logging, **always on** (emits everything by
  default). `CLAUSTRUM_LOG_LEVEL` only *raises* the threshold to quiet it; it
  never turns logging off entirely.
- **Metrics** — a Prometheus `/metrics` endpoint via `-metrics-addr`. Off by
  default: no listener exists unless the flag is set.
- **Token handoff** — a disk-free token via `-token-fd` (nothing touches disk).
- **Windows process kill** — whole-tree child kill on Windows via Job Objects.
- **`-keep-children`** (CT-2, POSIX-only) — leaves spawned processes running
  across a graceful shutdown so they survive a daemon restart. Off by default;
  the default shutdown kills them.

## Protocol extension

claustrum has one opt-in protocol extension that is visible to clients — a
deliberate addition, not a reference behavior. Passing `"wantPid":true` to
`process.spawn` / `process.reattach` adds `pid` + `startTime` to the result for
PID-reuse detection (CT-1).

A client that does not opt in sees byte-identical frames, so the hard rule above
still holds. It is catalogued as a deliberate divergence in the
[divergence catalog](DIVERGENCES.md).

## Where to go next

<div class="grid cards" markdown>

- :material-sitemap: **[Architecture](ARCHITECTURE.md)** — the three runtime
  roles, the concurrency & replay model, and how a driver uses it.
- :material-protocol: **[Protocol reference](PROTOCOL.md)** — every method, its
  params, result shape, and error codes.
- :material-console: **[Examples](EXAMPLES.md)** — worked client sessions over
  the socket.
- :material-sync: **[Upstream tracking](UPSTREAM-TRACKING.md)** — how
  compatibility with the reference daemon is kept in lock-step.
- :material-source-branch: **[Divergences](DIVERGENCES.md)** — every deliberate
  departure from the reference, its default, and how to activate it.
- :material-format-list-checks: **[Shipped ledger](IMPROVEMENTS.md)** —
  the completed hardening work, one line per item.

</div>

## Safety model

`process.spawn` runs arbitrary commands as the daemon's user **by design** —
treat the socket + token as equivalent to shell access. The full threat model
lives in the
[security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).
There is **no telemetry, ever**.
