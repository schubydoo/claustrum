# claustrum architecture

A single Go binary, mode-switched by flag. Static (`CGO_ENABLED=0`). One
cross-platform dependency, `klauspost/compress` (zstd); two more compiled **only
into Windows builds** — `golang.org/x/sys` (Job Object teardown) and
`github.com/Microsoft/go-winio` (the opt-in `-listen-pipe` named-pipe transport).

## Source layout (flat `package main`)

| file | responsibility |
|---|---|
| `main.go` | flag parsing, mode dispatch, version resolution |
| `server.go` | `-serve` daemon: AF_UNIX listener, per-connection loop, concurrent dispatch, graceful shutdown (kills children, or leaves them with `-keep-children`) |
| `rpc.go` | JSON-RPC request/response types, error codes, dispatch + params gate |
| `results.go` | result structs (field order is part of the wire contract) |
| `methods_server.go` / `methods_files.go` / `methods_git.go` / `methods_process.go` | the 19 method handlers |
| `process.go` | process manager: registry, per-process seq, replay buffer, subscribers; captures the immutable `pid`/`startTime` pair behind the CT-1 `wantPid` opt-in |
| `bridge.go` | `-bridge` relay and `-stop` |
| `install.go` | `-install`: download/verify/extract/prune + `__INSTALL_RESULT__` facts |
| `fetch`-style helpers live in `install.go` | HTTP GET + SHA-256 + in-process zstd |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`); level tag precedes the byte-intact `[Component]` prefixes |
| `metrics.go` | opt-in Prometheus counters at `/metrics` (`-metrics-addr`; no listener by default) |
| `sysproc_unix.go` / `sysproc_windows.go` | whole-tree kill: process group (setpgid + negative-pid signal) vs Windows Job Object (`KILL_ON_JOB_CLOSE`); the `-keep-children` POSIX-only policy (`honorKeepChildren`) |
| `pipetransport.go` | `-listen-pipe` shared helpers: `rpc.pipe` name-file lifecycle (atomic write / remove), owner-only SDDL builder, pipe-name + instance-id generation (all platform-neutral) |
| `pipetransport_windows.go` / `pipetransport_other.go` | the optional Windows named-pipe listener (`startPipeTransport` via go-winio, owner-only DACL) vs the non-Windows no-op stub + `honorListenPipe` warning |
| `detach_unix.go` / `detach_windows.go` | daemonize attr (setsid vs DETACHED_PROCESS) |
| `shellenv_unix.go` / `shellenv_windows.go` | login-shell PATH extraction (Unix) / no-op (Windows) |

The JSON-RPC surface is identical on every OS; only the `*_unix.go` / `*_windows.go`
files differ.

## The three runtime roles

### 1 · CLI-version manager (`-install`)

Ensures the pinned `claude` CLI is present under `-cli-dir`.

- If `<cli-dir>/<cli-version>` exists *and* is runnable (`<cli> --version`
  exits 0), it's kept as-is.
- Otherwise the blob is acquired from one of two sources:
    - `-cli-zst` — a local `.zst` (consumed on success); checksum-verified
      **only when a `-cli-checksum` is supplied** — an opt-in divergence from
      the reference, see [PROTOCOL.md](PROTOCOL.md).
    - `-cli-url` — downloaded, SHA-256-verified against `-cli-checksum`
      *unconditionally* (even an empty checksum fails).
- The blob is zstd-decompressed, `chmod 0755`, and re-checked for runnability
  **at a temp path, then atomically renamed into place** — an interrupted
  install never leaves a half-written CLI.
- The directory is pruned to the `-cli-keep` most-recent files (by mtime,
  default 3).
- It prints one line: `__INSTALL_RESULT__{json}`.

### 2 · Daemon / process supervisor (`-serve`)

Self-daemonizes (detaches from the controlling session, reparents to init on
Unix), opens the `0600` socket, and supervises spawned children.

- The auth token arrives via `-token-file` (read once, unlinked) or
  `-token-fd` (read from an open descriptor and forwarded to the detached
  child over a pipe — never touches disk).
- Once listening, the daemon **persists the token to `daemon.token` (`0600`)
  beside the socket** (atomic temp-write + rename) so a reconnecting client can
  re-authenticate after that source is gone, and **unlinks it on graceful
  shutdown** — behavioral parity with reference `5db5e4a` (`tokenpersist.go`; see
  [PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken)).
- The self-daemonize re-exec is gated by an **internal** sentinel,
  `CLAUSTRUM_DAEMON_CHILD` — deliberately claustrum-namespaced, *not* the
  reference's `CLAUDE_SSH_DAEMON_CHILD`, which a surrounding claude-ssh session
  exports to every descendant and would make the launcher skip its own
  daemonize/token-forward path. The child unsets the sentinel after reading it.
  Reference parity is kept separately: `daemonizeWithToken` still sets
  `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's environ so `process.spawn` children
  inherit it exactly as the reference does (see [PROTOCOL.md](PROTOCOL.md)).
- On Unix, interactive PATH extraction from the login shell runs concurrently
  (goroutine), so a slow login shell does not delay socket availability.
- With **`-listen-pipe`** (Windows-only, opt-in), it *additionally* opens a Windows
  named pipe serving the identical JSON-RPC dispatch (a second `acceptLoop` over
  the same `serveConn`), publishes the chosen pipe name to `rpc.pipe` beside the
  socket before accepting, and removes it on graceful shutdown. Strictly additive
  — the socket path is unchanged.
- It makes **no** outbound network connections, and no inbound listener beyond
  the socket unless the operator opts into `-metrics-addr` (TCP) or `-listen-pipe`
  (a local, owner-only Windows named pipe).

### 3 · JSON-RPC multiplexer + replay

One persistent socket, concurrent request dispatch, in-band token auth, and a
per-process frame buffer (`process.*`) that lets a late or reconnecting client
catch up via `reattach`.

(`-bridge` is a fourth, trivial mode: a dumb stdio↔socket relay — what an SSH
session attaches to. It injects no auth.)

## Operational logging

Mirrors the reference daemon:

- The readiness banner (`… remote server listening on <socket>`) goes to
  **stdout**, without a prefix. Only its product name differs (rebranded).
- Every operational/diagnostic line — `[Server]` connection lifecycle,
  `[process.Manager]` spawn/stream/exit, `[shellenv]`, `[frameSink]` — goes to
  **stderr** via the standard `log` package (a `2006/01/02 15:04:05` timestamp
  prefix).
- These logs are not part of the JSON-RPC wire contract, but they are kept
  byte-faithful (sans timestamp/PID) so anything tailing the daemon log behaves
  identically.

A tiny leveled logger
([`logging.go`](https://github.com/schubydoo/claustrum/blob/main/logging.go))
sits in front of those calls so operators can quiet the daemon:

- Each line carries a level (`DEBUG`/`INFO`/`WARN`/`ERROR`), emitted as a short
  tag *before* the `[Component]` prefix — `INFO  [Server] New connection
  from: …` — so the prefixes stay byte-intact and any grep for `[Server]`,
  `[process.Manager]`, `[frameSink]`, or `[shellenv]` keeps matching.
- The threshold is set once at startup from `CLAUSTRUM_LOG_LEVEL`
  (`debug`|`info`|`warn`|`error`).
- It **defaults to `debug`** — an unset (or unrecognized) value emits exactly
  what the daemon always has; raising it drops everything below the chosen
  level.
- Purely a local diagnostic knob: it touches stderr only, never the wire.

## `__INSTALL_RESULT__` facts

```jsonc
{
  "serverVersion": "<daemon id>",
  "os":   "linux",            // GOOS
  "arch": "amd64",            // GOARCH
  "libc": "glibc",            // or "musl"
  "cliPath": "<cli-dir>/<cli-version>",
  "cliWasPresent": false,     // true only if it already existed AND was runnable
  "cliError": "…"             // omitted on success
}
```

**`cliError` strings.** Every error `ensureCLI` returns lands here verbatim. The
list had drifted — it named four of these while the code produced thirteen — so it
is grouped by phase rather than left as prose:

| phase | string |
|---|---|
| version check | `cli version "<v>" must be a single path component` |
| | `cli version "<v>" collides with the install temp sweep` |
| source | `cli <v> missing and no --cli-url or --cli-zst provided` |
| | `opening input: <err>` (`-cli-zst` read) |
| download | `download failed: <err>` — **transport** failure only |
| | `download failed with status <code>` — **non-200**, no URL, no reason phrase |
| | `response exceeds <n> bytes` |
| verify | `checksum mismatch: expected=<a>, actual=<b>` |
| install | `mkdir cli dir: <err>` |
| | `staging cli: <err>` |
| | `decompressing: <err>` |
| | `decompressed CLI exceeds <n> bytes` |
| | `installed cli at <path> is not runnable` |
| | `clearing stale dir at <path>: <err>` |
| | `staging file vanished before install: <err>` |

The two download forms are deliberately worded differently, matching the
reference: only the transport failure carries the `download failed: ` prefix, and
the status form omits the URL so a signed URL cannot reach whatever captures the
`__INSTALL_RESULT__` line. A bare `chmod`/`rename` failure propagates as the raw
Go error.

## Deployment lifecycle (how a driver uses it)

A driver (Claude Desktop, or your own tool such as clauster) typically:

1. probes the remote OS/arch,
2. ensures the daemon binary is present on the remote (e.g. uploads it),
3. runs `claustrum -install …` to ensure the agent CLI is present,
4. starts `claustrum -serve -socket … -token-file …`,
5. attaches per session with `claustrum -bridge -socket …` and speaks JSON-RPC
   (in-band auth) through it,
6. drives the agent and any MCP servers as `process.spawn` children, feeding each
   child's stdin via `process.stdin` (base64) and reading its stdout via the
   stream notifications.

## Concurrency & replay model

- Each connection's requests run in their own goroutine; the per-connection writer
  is mutex-serialized so responses and stream frames interleave safely.
- Each managed process has: a monotonic `seq`, an append-only frame buffer, and a
  set of subscriber connections. `spawn` subscribes the spawning connection;
  `reattach` subscribes the requester and replays buffered frames with
  `seq > fromSeq`. A dead subscriber is detached (`[frameSink] replay write
  failed, detaching`).
