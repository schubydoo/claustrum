# claustrum architecture

Claustrum is one Go binary. A flag selects the mode. The build is static
(`CGO_ENABLED=0`). It has one cross-platform dependency, `klauspost/compress`
(zstd). Two more dependencies go **only into Windows builds** —
`golang.org/x/sys` (Job Object teardown) and `github.com/Microsoft/go-winio`
(the opt-in `-listen-pipe` named-pipe transport).

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
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`); level tag precedes the byte-intact `[Component]` prefixes |
| `metrics.go` | opt-in Prometheus counters at `/metrics` (`-metrics-addr`; no listener by default) |
| `sysproc_unix.go` / `sysproc_windows.go` | whole-tree kill: process group (setpgid + negative-pid signal) vs Windows Job Object (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`); the `-keep-children` POSIX-only policy (`honorKeepChildren`) |
| `pipetransport.go` | `-listen-pipe` shared helpers: `rpc.pipe` name-file lifecycle (atomic write / remove), owner-only SDDL builder, pipe-name + instance-id generation (all platform-neutral) |
| `pipetransport_windows.go` / `pipetransport_other.go` | the optional Windows named-pipe listener (`startPipeTransport` via go-winio, owner-only DACL) vs the non-Windows no-op stub + `honorListenPipe` warning |
| `detach_unix.go` / `detach_windows.go` | daemonize attr (setsid vs DETACHED_PROCESS) |
| `shellenv_unix.go` / `shellenv_windows.go` | login-shell PATH extraction (Unix) / no-op (Windows) |

The JSON-RPC surface is the same on every OS. Only the `*_unix.go` /
`*_windows.go` files are different.

## The three runtime roles

### 1 · CLI-version manager (`-install`)

This mode makes sure the pinned `claude` CLI is present under `-cli-dir`.

- If `<cli-dir>/<cli-version>` exists *and* is runnable (`<cli> --version`
  exits 0), claustrum keeps it as it is. The probe has **no deadline by
  default**. That matches the reference on every input measured: the reference
  installed a CLI that answered at 90 s. Above 90 s, nothing was probed. The `-cli-probe-timeout`
  flag, or the `cli-probe-timeout` config key, opts into a deadline. A CLI that
  works but is slower than that deadline then fails this guard — see
  [D11](DIVERGENCES.md#d11).
- If not, claustrum gets the blob from one of two sources:
    - `-cli-zst` — a local `.zst` file. Claustrum consumes it as soon as
      **decompression** succeeds, even if the install then fails the runnability
      probe. Claustrum verifies its checksum **only when a `-cli-checksum` is
      supplied** — a conditional divergence from the reference, which the caller
      activates; see [D1](DIVERGENCES.md#d1).
    - `-cli-url` — claustrum downloads the blob and verifies its SHA-256 against
      `-cli-checksum` *unconditionally* (even an empty checksum fails).
- Claustrum decompresses the blob with zstd, applies `chmod 0755`, and checks it
  again for runnability. It does all of this **at a temp path, then renames the
  file into place atomically** — an interrupted install never leaves a
  half-written CLI.
- Claustrum prunes the directory to the `-cli-keep` most-recent files (by mtime,
  default 3).
- Claustrum prints one line: `__INSTALL_RESULT__{json}`.

### 2 · Daemon / process supervisor (`-serve`)

Claustrum daemonizes itself: it detaches from the controlling session and, on
Unix, reparents to init. It then opens the `0600` socket. It supervises the
children it spawns.

- The auth token arrives through `-token-file` or `-token-fd`. With
  `-token-file`, claustrum reads the file once and then unlinks it. With
  `-token-fd`, claustrum reads the token from an open descriptor and forwards it
  to the detached child over a pipe — this handoff never touches disk.
- Once listening, the daemon **persists the token to `daemon.token` (`0600`)
  beside the socket**. It writes a temp file and renames it, so the write is
  atomic. A client that reconnects can then authenticate again after that source
  is gone. The daemon **unlinks `daemon.token` on graceful shutdown**. This is
  behavioral parity with reference `5db5e4a` (`tokenpersist.go`; see
  [PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken)).
- An **internal** sentinel, `CLAUSTRUM_DAEMON_CHILD`, gates the self-daemonize
  re-exec. The name is claustrum's own on purpose. It is *not* the reference's
  `CLAUDE_SSH_DAEMON_CHILD`, which a surrounding claude-ssh session exports to
  every descendant; that name would make the launcher skip its own
  daemonize/token-forward path. The child unsets the sentinel after it reads it.
  Claustrum keeps reference parity by a separate route: `daemonizeWithToken`
  still sets `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's environ, so
  `process.spawn` children inherit it exactly as they do under the reference (see
  [PROTOCOL.md](PROTOCOL.md)).
- On Unix, claustrum extracts the interactive PATH from the login shell in a
  separate goroutine. A slow login shell therefore does not delay the moment the
  socket becomes available.
- With **`-listen-pipe`** (Windows-only, opt-in), the daemon *additionally*
  opens a Windows named pipe. That pipe serves the identical JSON-RPC dispatch
  through a second `acceptLoop` over the same `serveConn`. Before it accepts on
  the pipe, the daemon publishes the chosen pipe name to `rpc.pipe` beside the
  socket. It removes `rpc.pipe` on graceful shutdown. The transport is strictly
  additive — the socket path does not change.
- The daemon makes **no** outbound network connections. It opens no inbound
  listener beyond the socket unless the operator opts into `-metrics-addr` (TCP)
  or `-listen-pipe` (a local, owner-only Windows named pipe).

### 3 · JSON-RPC multiplexer + replay

This role uses one persistent socket, concurrent request dispatch, and in-band
token auth. It also keeps a per-process frame buffer (`process.*`). A client that
connects late, or that reconnects, catches up on that buffer with `reattach`.

(`-bridge` is a fourth, trivial mode: a dumb stdio↔socket relay — what an SSH
session attaches to. It injects no auth.)

## Concurrency & replay model

- Each request on a connection runs in its own goroutine. A mutex serializes the
  per-connection writer, so responses and stream frames interleave safely.
- Each managed process has a monotonic `seq`, an append-only frame buffer, and a
  set of subscriber connections. `spawn` subscribes the connection that spawned
  the process. `reattach` subscribes the requester and replays buffered frames
  with `seq > fromSeq`. Claustrum detaches a dead subscriber (`[frameSink] replay
  write failed, detaching`).

## Deployment lifecycle (how a driver uses it)

A driver (Claude Desktop, or your own tool such as clauster) typically:

1. probes the remote OS/arch,
2. ensures the daemon binary is present on the remote (e.g. uploads it),
3. runs `claustrum -install …` to ensure the agent CLI is present,
4. starts `claustrum -serve -socket … -token-file …`,
5. attaches per session with `claustrum -bridge -socket …` and speaks JSON-RPC
   (in-band auth) through it,
6. drives the agent and any MCP servers as `process.spawn` children. It writes to
   each child's stdin with `process.stdin` (base64), and it reads that child's
   stdout from the stream notifications.

## Inherited wire bytes

**Go's standard library, not claustrum's code**, produces some of claustrum's
byte-identical output. The reference is also a Go program, so its stdlib does
unpaid parity work for us. That is a real asset, but inherited agreement is
agreement nobody verified.

Two things follow. First, a Go upgrade that changes an escaping rule moves the
wire, and this repo shows no diff. Second, a reimplementation in another language
must re-derive every row below by hand. The list is the honest cost estimate for
that work.

The stdlib decides the rows marked **inherited**. **Deliberate** means claustrum
chose the mechanism *because* it reproduced the reference — inherited behaviour
that someone measured and adopted, rather than inherited behaviour nobody looked
at.

| Surface | Where | Source | Status |
|---|---|---|---|
| `id` echo: `1.0`→`1`, `1e2`→`100`, big-int precision loss, map keys sorted | `rpc.go` (`request.ID interface{}`) | `encoding/json` round-trip through `interface{}` | **deliberate** — `json.RawMessage` reproduced none of it; measured |
| Invalid UTF-8 in a result string → one `\ufffd` escape per bad byte; NUL → `\u0000` | `files.read` `content`, any string field | `encoding/json` encode | inherited |
| `<` `>` `&` → `\u003c` `\u003e` `\u0026` | every string field and error message | `encoding/json` HTML escaping, on by default | inherited |
| Invalid UTF-8 in a **request** param replaced with U+FFFD before dispatch sees it | every path param | `encoding/json` **decode** | inherited |
| `chdir <p>: stat <p>: no such file or directory` | error messages built from `err.Error()` | `os` `*PathError` (`op + " " + path + ": " + errno`) | inherited |
| Frame `data` alphabet and `=` padding | `process.*` stdout/stderr frames | `base64.StdEncoding` | inherited |
| Result field ORDER | `results.go` | `encoding/json` emits struct fields in declaration order | **deliberate** — order is chosen to match, and results are structs, never maps, precisely because maps sort |
| Path cleaning for `~`-prefixed paths | `expandpath.go` | `filepath.Join`/`Clean` | **deliberate** — the lexical clean was measured and matched (PR 205) |

The decode-side row is the one row with a user-visible consequence: **a file
whose name is not valid UTF-8 cannot be addressed through the protocol at all**,
on either daemon. The request decoder substitutes U+FFFD before any method runs,
so the daemon operates on a name that does not exist. This is not a claustrum
limitation to fix. It is the reference's behaviour, and claustrum inherits it by
the same route.

`inherited_encoding_test.go` and `inherited_encoding_unix_test.go` pin these
rules against regression. For the encode-side rules they assert escape TEXT
(`\ufffd`). For the decode-side rule they assert the U+FFFD CHARACTER. Which side
substituted is what distinguishes them.

## Operational logging

The logging mirrors the reference daemon:

- The readiness banner (`… remote server listening on <socket>`) goes to
  **stdout**, without a prefix. Only its product name is different (rebranded).
- Every operational or diagnostic line goes to **stderr** through the standard
  `log` package, which adds a `2006/01/02 15:04:05` timestamp prefix. These lines
  are the `[Server]` connection lifecycle, the `[process.Manager]` spawn, stream
  and exit lines, `[shellenv]`, and `[frameSink]`.
- These logs are not part of the JSON-RPC wire contract. Claustrum still keeps
  them byte-faithful (minus the timestamp and PID), so anything tailing the
  daemon log behaves identically.

A tiny leveled logger
([`logging.go`](https://github.com/schubydoo/claustrum/blob/main/logging.go))
sits in front of those calls so operators can quiet the daemon:

- Each line carries a level (`DEBUG`/`INFO`/`WARN`/`ERROR`). The logger emits it
  as a short tag *before* the `[Component]` prefix — `INFO  [Server] New
  connection from: …`. The prefixes thus stay byte-intact, and any grep for
  `[Server]`, `[process.Manager]`, `[frameSink]`, or `[shellenv]` keeps matching.
- Claustrum sets the threshold once, at startup, from `CLAUSTRUM_LOG_LEVEL`
  (`debug`|`info`|`warn`|`error`).
- The threshold **defaults to `debug`**. An unset (or unrecognized) value emits
  exactly what the daemon always has. A higher threshold drops everything below
  the chosen level.
- The level is a local diagnostic knob. It touches stderr only, never the wire.

## `__INSTALL_RESULT__` facts

```jsonc
{
  "serverVersion": "<daemon id>",
  "os":   "linux",            // GOOS
  "arch": "amd64",            // GOARCH
  "libc": "glibc",            // or "musl"; "" off linux (no probe). On linux an ldd
                              // slower than -libc-probe-timeout falls back to glibc
                              // when that is set; no deadline by default (D14).
                              // The driver uses
                              // this field to pick which CLI build to download —
                              // a third-binary claim; see the provenance note below.
  "cliPath": "<cli-dir>/<cli-version>",
  "cliWasPresent": false,     // true only if it existed AND answered --version — within
                              // -cli-probe-timeout when that is set; no deadline by default (D11)
  "cliError": "…"             // omitted on success
}
```

**`cliError` strings.** Every error that `ensureCLI` returns lands here
verbatim, **with the wrapping prefix that its phase adds**. That is the form a
driver sees. The table below groups the strings by phase and quotes no count.
Re-derive a count from `ensureCLI` if you need one:

| phase | string |
|---|---|
| version check | `cli version "<v>" must be a single path component` |
| | `cli version "<v>" collides with the install temp sweep` |
| | `cli version "<v>" collides with the install download blob` |
| source | `cli <v> missing and no --cli-url or --cli-zst provided` — also reached when a **present, working** CLI answers `--version` more slowly than an opted-in `-cli-probe-timeout` and no source flag was given (D11; unreachable at the default, which has no deadline) |
| | `opening input: <err>` (`-cli-zst` read) |
| download | `download failed: <err>` — **transport** failure only, plus `context deadline exceeded (…)` when the opt-in `-cli-download-timeout` bound fires (D12; off by default, so unreachable unless asked for) |
| | `download failed with status <code>` — **non-200**, no URL, no reason phrase |
| | `download failed: response exceeds <n> bytes` |
| verify | `checksum mismatch: expected=<a>, actual=<b>` |
| install | `mkdir cli dir: <err>` |
| | `staging cli: <err>` |
| | `decompressing: <err>` |
| | `decompressing: decompressed CLI exceeds <n> bytes` |
| | `installed cli at <path> is not runnable` |
| | `clearing stale dir at <path>: <err>` |
| | `staging file vanished before install: <err>` |

The two download forms use different wording on purpose, to match the
reference. Only the transport failure carries the `download failed: ` prefix. The
status form omits the URL, so a signed URL cannot reach whatever captures the
`__INSTALL_RESULT__` line. A bare `chmod`/`rename` failure propagates as the raw
Go error.

These strings are not free-form diagnostics: the driver reads them. A change to a
row's wording — or a new guard whose error pre-empts a row — can change what a
user sees even when no JSON-RPC frame moves (D10's opt-in cap is the worked
example). How the driver classifies them is a third-binary claim; see
[Driver claims and their provenance](#driver-claims-and-their-provenance).

## Driver claims and their provenance

A few facts these docs rely on describe a **third binary** — the driver (Claude
Desktop, or a tool such as clauster). The reference-vs-claustrum harness compares
two daemons, so it cannot confirm or refute any of these facts. Three of them are
load-bearing:

- **`cliError` classification** — the driver reads `cliError` as text. It
  surfaces a disk-full-shaped message as a terminal error with an actionable
  code, and everything else as retryable. The claim is about that classification
  only. A `-cli-url` rung whose download was forced to fail returned a
  non-disk-full `cliError`, and Desktop answered it with a further rung rather
  than a terminal report. That shows escalation on a failed rung. It does not
  distinguish reading the `cliError` text from reacting to any non-success, and
  the terminal arm is still unobserved.
- **`libc` build selection** — the driver uses the `__INSTALL_RESULT__` `libc`
  field to pick which CLI build to download.
- **"Desktop owns the argv"** — an operator has no way to influence the daemon's
  argv. A divergence reachable only through an argv flag is therefore unreachable
  on Desktop-driven hosts. This premise lets D3, D4, D5, D10, D11, D12 and D14 be
  opt-in. It also defines the "(opt-in)" tagging convention (a flag **and**
  a config key, since the config key is the reachable knob).

"The harness cannot confirm or refute it" is not "unverifiable". Each claim has a
fixture that can settle it. That fixture runs against the *driver*, with a control
that could come out wrong. The argv row's fixtures were run. The `cliError` and
`libc` fixtures were not run.

| claim | fixture | control that must fire |
|---|---|---|
| `cliError` classification | two `-install` failures whose messages straddle the disk-full shape; observe a retry vs a terminal report | a genuine disk-full failure observed as terminal with an actionable code, proving the terminal side is reachable |
| `libc` build selection | a stub `ldd` printing a musl banner and exiting 0, with `/lib/ld-musl-*.so.*` absent (else the glob short-circuits and `ldd` never runs); see which build the client fetches | a glibc host where the daemon reports `glibc` fetches the glibc build, so the musl arm is distinguishable from the client's default |
| argv | inspect the shipped client for where the daemon's argv is built; corroborate with the setup UI, a capture of the argv Desktop passes (cache-hit and fetching), and an enumeration of Desktop's config files | if the argv is assembled from a setting or a config file, the claim is false |

The argv claim is **discharged**. Claude Desktop builds the daemon's argv from a
**fixed set of flags** — only their operands (the download URL, its checksum, the
uploaded blob path) are runtime values — with no operator-reachable route to add or
change a flag. Found by inspecting the shipped client (2026-08-09). The fetching
`-install` argv is observed over two cold starts on one host (2026-08-10): bare,
then `-cli-url` + `-cli-checksum`; and once, with that download forced to fail, an
SFTP upload re-invoked as `-cli-zst`. Both records live in `scratch/` (gitignored).

The `cliError` and `libc` claims remain design constraints. The argv claim is a
driver result, not a parity one. Five argv dependents (D3, D4, D5, D12, D14) carry
reopen triggers for their own behaviour. The other two (D10, D11) carry riders about
the `cliError` claim, as does D13. All live in [DIVERGENCES.md](DIVERGENCES.md). The
argv claim reopens if someone finds a route to influence the daemon's argv, or if a
Desktop release adds one — a settings field, or a config file it turns into argv (a
forwarded env var does not qualify: nothing in `config.go` or `main.go` reads the
environment for these knobs). The dependents list is maintained by hand, and it was
incomplete every time somebody checked it. Treat it as best-known, not complete. The
argv claim underpins D3, D4, D5, D10, D11, D12 and D14. `cliError` underpins D10,
D11 (retraction rider), D13 and clause (c)'s error-string rider. `libc` underpins
D14's residual delta. Two further driver claims — D6's and D7's clause-(b) evidence,
resting on what Desktop emits as `-cli-version` — are untracked and unprovenanced.
