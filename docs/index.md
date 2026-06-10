# claustrum

A tiny, **dependency-light Go daemon** — a clean-room reimplementation of the
small daemon that hosts a remote Claude Code session over SSH. It is, in one
binary: a local CLI-version manager, a process supervisor, and a JSON-RPC
multiplexer (with a replay buffer) over an `AF_UNIX` socket.

It was built to a behavioral contract captured by black-box probing the
reference binary — no code was copied or decompiled (see
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

It exposes **18 methods** across the `server.*`, `files.*`, `git.*`, and
`process.*` namespaces. Auth is in-band per request; spawned processes stream
base64 stdout/stderr frames that a late or reconnecting client can replay via
`reattach`.

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
