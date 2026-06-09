# claustrum architecture

A single Go binary, mode-switched by flag. Static (`CGO_ENABLED=0`), one
dependency (`klauspost/compress` for zstd).

## Source layout (flat `package main`)

| file | responsibility |
|---|---|
| `main.go` | flag parsing, mode dispatch, version resolution |
| `server.go` | `-serve` daemon: AF_UNIX listener, per-connection loop, concurrent dispatch, graceful shutdown |
| `rpc.go` | JSON-RPC request/response types, error codes, dispatch + params gate |
| `results.go` | result structs (field order is part of the wire contract) |
| `methods_server.go` / `methods_files.go` / `methods_git.go` / `methods_process.go` | the 18 method handlers |
| `process.go` | process manager: registry, per-process seq, replay buffer, subscribers |
| `bridge.go` | `-bridge` relay and `-stop` |
| `install.go` | `-install`: download/verify/extract/prune + `__INSTALL_RESULT__` facts |
| `fetch`-style helpers live in `install.go` | HTTP GET + SHA-256 + in-process zstd |
| `sysproc_unix.go` / `sysproc_windows.go` | process-group attr + signalling (setpgid+kill vs CreateProcess) |
| `detach_unix.go` / `detach_windows.go` | daemonize attr (setsid vs DETACHED_PROCESS) |
| `shellenv_unix.go` / `shellenv_windows.go` | login-shell PATH extraction (Unix) / no-op (Windows) |

The JSON-RPC surface is identical on every OS; only the `*_unix.go` / `*_windows.go`
files differ.

## The three runtime roles

1. **CLI-version manager (`-install`)** — ensures the pinned `claude` CLI is
   present under `-cli-dir`. If `<cli-dir>/<cli-version>` exists *and* is runnable
   (`<cli> --version` exits 0) it's kept; otherwise the blob is acquired from
   `-cli-zst` (a local `.zst`, consumed on success, **not** checksum-verified) or
   downloaded from `-cli-url` (SHA-256-verified against `-cli-checksum`
   *unconditionally* — even an empty checksum fails), zstd-decompressed, `chmod 0755`,
   re-checked for runnability, then the directory is pruned to `-cli-keep`
   most-recent files (by mtime, default 3). It prints one line:
   `__INSTALL_RESULT__{json}`.

2. **Daemon / process supervisor (`-serve`)** — self-daemonizes (detaches from
   the controlling session, reparents to init on Unix), opens the `0600` socket,
   and supervises spawned children. On Unix, interactive PATH extraction from the
   login shell runs concurrently (goroutine) so a slow login shell does not delay
   socket availability. It makes **no** network connections.

3. **JSON-RPC multiplexer + replay** — one persistent socket, concurrent request
   dispatch, in-band token auth, and a per-process frame buffer (`process.*`) that
   lets a late or reconnecting client catch up via `reattach`.

**Operational logging** mirrors the reference daemon: the readiness banner
(`… remote server listening on <socket>`) is printed to **stdout** without a
prefix, while every operational/diagnostic line — `[Server]` connection
lifecycle, `[process.Manager]` spawn/stream/exit, `[shellenv]`, `[frameSink]` —
goes to **stderr** via the standard `log` package (a `2006/01/02 15:04:05`
timestamp prefix). Only the banner's product name differs (rebranded). These logs
are not part of the JSON-RPC wire contract, but they are kept byte-faithful (sans
timestamp/PID) so anything tailing the daemon log behaves identically.

`-bridge` is a fourth, trivial mode: a dumb stdio↔socket relay (what an SSH
session attaches to). It injects no auth.

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
