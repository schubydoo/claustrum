# claustrum architecture

A single Go binary, mode-switched by flag. Static (`CGO_ENABLED=0`), two
dependencies: `klauspost/compress` (zstd) and `golang.org/x/sys` (Windows Job
Object teardown — only compiled into Windows builds).

## Source layout (flat `package main`)

| file | responsibility |
|---|---|
| `main.go` | flag parsing, mode dispatch, version resolution |
| `server.go` | `-serve` daemon: AF_UNIX listener, per-connection loop, concurrent dispatch, graceful shutdown (kills children, or leaves them with `-keep-children`) |
| `rpc.go` | JSON-RPC request/response types, error codes, dispatch + params gate |
| `results.go` | result structs (field order is part of the wire contract) |
| `methods_server.go` / `methods_files.go` / `methods_git.go` / `methods_process.go` | the 18 method handlers |
| `process.go` | process manager: registry, per-process seq, replay buffer, subscribers; captures the immutable `pid`/`startTime` pair behind the CT-1 `wantPid` opt-in |
| `bridge.go` | `-bridge` relay and `-stop` |
| `install.go` | `-install`: download/verify/extract/prune + `__INSTALL_RESULT__` facts |
| `fetch`-style helpers live in `install.go` | HTTP GET + SHA-256 + in-process zstd |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`); level tag precedes the byte-intact `[Component]` prefixes |
| `metrics.go` | opt-in Prometheus counters at `/metrics` (`-metrics-addr`; no listener by default) |
| `sysproc_unix.go` / `sysproc_windows.go` | whole-tree kill: process group (setpgid + negative-pid signal) vs Windows Job Object (`KILL_ON_JOB_CLOSE`); the `-keep-children` POSIX-only policy (`honorKeepChildren`) |
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
- On Unix, interactive PATH extraction from the login shell runs concurrently
  (goroutine), so a slow login shell does not delay socket availability.
- It makes **no** outbound network connections, and no inbound listener beyond
  the socket unless the operator opts into `-metrics-addr`.

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

`cliError` strings: `cli <v> missing and no --cli-url or --cli-zst provided` ·
`download failed: <err>` · `checksum mismatch: expected=<a>, actual=<b>` ·
`installed cli at <path> is not runnable`.

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
