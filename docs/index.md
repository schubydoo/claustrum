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

- `-serve` is the daemon. It listens on an `AF_UNIX` socket with mode `0600`,
  dispatches requests concurrently, daemonizes itself, and shuts down
  gracefully. On Windows the mode `0600` is not an owner-only ACL.
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

A few extras are claustrum's own. Each one is off by default or invisible to
clients, so none changes the frames that a client sees:

- the log level, `CLAUSTRUM_LOG_LEVEL`
- Prometheus metrics, `-metrics-addr`
- a frame capture, `-wire-log`. A capture holds frame payloads, so treat it as
  sensitive.
- the token on a file descriptor, `-token-fd`
- `-keep-children` (CT-2)

[Examples](EXAMPLES.md#operational-knobs) shows each one but `-wire-log`, which
[Flags of claustrum alone](PROTOCOL.md#flags-of-claustrum-alone) describes. The
[flags table](PROTOCOL.md#flags-and-config-keys) lists the token, knob, divergence and deprecated flags with their keys.

One opt-in member of claustrum's own changes a frame: `"wantPid":true` adds `pid`
and `startTime` to the result of `process.spawn`, and of `process.reattach` for a
found process
([CT-1](DIVERGENCES.md#ct-1)).

## Where to go next

<div class="grid cards" markdown>

- :material-sitemap: **[Architecture](ARCHITECTURE.md)**. The three runtime
  roles, the concurrency and replay model, and how a
  [driver](GLOSSARY.md#driver) uses it.
- :material-protocol: **[Protocol reference](PROTOCOL.md)**. The transport,
  the error texts, the stream frames, and the modes and flags of the binary.
- :material-api: **Methods**. One page for each method:
  [server.*](protocol/server.md),
  [files.list](protocol/files-list.md),
  [files.validate](protocol/files-validate.md),
  [files.stat](protocol/files-stat.md),
  [files.read](protocol/files-read.md),
  [files.extract_tar](protocol/files-extract-tar.md),
  [git.info](protocol/git-info.md),
  [git.status](protocol/git-status.md),
  [git.list_branches](protocol/git-list-branches.md),
  [git.worktree_create](protocol/git-worktree-create.md),
  [git.worktree_remove](protocol/git-worktree-remove.md),
  [launcher.resolve](protocol/launcher-resolve.md),
  [process.spawn](protocol/process-spawn.md),
  [process.stdin](protocol/process-stdin.md),
  [process.kill](protocol/process-kill.md),
  [process.killAndWait](protocol/process-killandwait.md),
  [process.reattach](protocol/process-reattach.md) and
  [plugins.prune](protocol/plugins-prune.md).
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
