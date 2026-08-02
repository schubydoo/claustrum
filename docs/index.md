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

Beyond the wire contract, a few **claustrum-only operational extras** (all
opt-in and invisible to clients): leveled stderr logging via
`CLAUSTRUM_LOG_LEVEL`, a Prometheus `/metrics` endpoint via `-metrics-addr`
(no listener exists without it), a disk-free token handoff via `-token-fd`,
whole-tree process kill on Windows via Job Objects, and a `-keep-children`
flag (CT-2, POSIX-only) that leaves spawned processes running across a graceful
shutdown so they survive a daemon restart (off by default — shutdown kills them).
See the [protocol reference](PROTOCOL.md) for details.

There is also one opt-in **protocol extension** — visible to clients but still an
explicit **addition, not a reference behavior**: passing `"wantPid":true` to
`process.spawn` / `process.reattach` adds `pid` + `startTime` to the result for
PID-reuse detection (CT-1). A client that doesn't opt in sees byte-identical
frames, so the hard rule above still holds. It is catalogued as a deliberate
divergence in the [improvement backlog](IMPROVEMENTS.md#deliberate-divergences-post-parity-opt-in).

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
- :material-format-list-checks: **[Improvement backlog](IMPROVEMENTS.md)** —
  stack-ranked, all wire-compatible unless noted.

</div>

## Safety model

`process.spawn` runs arbitrary commands as the daemon's user **by design** —
treat the socket + token as equivalent to shell access. The full threat model
lives in the
[security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).
There is **no telemetry, ever**.
