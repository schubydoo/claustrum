# claustrum architecture

Claustrum is one Go binary. A flag selects the mode. The build is static
(`CGO_ENABLED=0`). It has one cross-platform dependency, `klauspost/compress`
(zstd). Two more dependencies go into Windows builds only. They are
`golang.org/x/sys` (Job Object teardown) and `github.com/Microsoft/go-winio`
(the opt-in `-listen-pipe` named-pipe transport).

## Source layout (flat `package main`)

| file | responsibility |
|---|---|
| `main.go` | flag parsing, mode dispatch, version resolution |
| `server.go` | `-serve` daemon: AF_UNIX listener, per-connection loop, concurrent dispatch, graceful shutdown (kills children, or leaves them with `-keep-children`) |
| `rpc.go` | JSON-RPC request and response types, error codes, dispatch + params gate |
| `results.go` | result structs (field order is part of the wire contract) |
| `methods_server.go` / `methods_files.go` / `methods_git.go` / `methods_process.go` / `methods_plugins.go` | the 19 method handlers (`19f30c46` added `plugins.prune`) |
| `handlers/prune.go` | the sole non-`main` package: `handlers.PruneParams`, so `plugins.prune`'s `-32602` body carries the reference's `handlers.PruneParams` type name byte-for-byte |
| `process.go` | process manager: registry, per-process seq, replay buffer, subscribers. It captures the immutable `pid`/`startTime` pair behind the CT-1 `wantPid` opt-in |
| `bridge.go` | `-bridge` relay and `-stop` |
| `install.go` | `-install`: download/verify/extract/prune + `__INSTALL_RESULT__` facts |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`). The level tag precedes the byte-intact `[Component]` prefixes |
| `metrics.go` | opt-in Prometheus counters at `/metrics` (`-metrics-addr`, with no listener by default) |
| `wirelog.go` | opt-in `-wire-log` frame capture (CT-3). A pure side channel over already-marshaled bytes, off by default, with by-key credential redaction |
| `sysproc_unix.go` / `sysproc_windows.go` | whole-tree kill: process group (setpgid + negative-pid signal) vs Windows Job Object (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`). It also holds the `-keep-children` POSIX-only policy (`honorKeepChildren`) |
| `pipetransport.go` | `-listen-pipe` shared helpers: `rpc.pipe` name-file lifecycle (atomic write / remove), owner-only SDDL builder, pipe-name + instance-id generation (all platform-neutral) |
| `pipetransport_windows.go` / `pipetransport_other.go` | the optional Windows named-pipe listener (`startPipeTransport` via go-winio, owner-only DACL) vs the non-Windows no-op stub + `honorListenPipe` warning |
| `detach_unix.go` / `detach_windows.go` | daemonize attr (setsid vs DETACHED_PROCESS) |
| `shellenv_unix.go` / `shellenv_windows.go` | login-shell PATH extraction (Unix) / no-op (Windows) |
| `shellagent.go` / `shellagent_unix.go` / `shellagent_windows.go` | `process.spawn` SSH agent hand-off: the login shell's `SSH_AUTH_SOCK`, checked and cached (Unix) / no-op (Windows) |

The JSON-RPC surface is the same on every OS. Only the `*_unix.go` /
`*_windows.go` files are different.

## The three runtime roles

### 1 · CLI-version manager (`-install`)

This mode makes sure the pinned `claude` CLI is present under `-cli-dir`.

- If `<cli-dir>/<cli-version>` exists *and* is runnable (`<cli> --version`
  exits 0), claustrum keeps it as it is. The probe has no deadline by
  default. That matches the reference on every input measured: the reference
  installed a CLI that answered at 90 s. Above 90 s, nothing was probed. The `-cli-probe-timeout`
  flag, or the `cli-probe-timeout` configuration key, opts into a deadline. A CLI that
  works but is slower than that deadline then fails this guard. See
  [D11](DIVERGENCES.md#d11).
- If not, claustrum gets the blob from one of two sources:
    - `-cli-zst` names a local `.zst` file. Claustrum consumes it as soon as
      decompression succeeds. A runnability probe that fails after that point
      does not undo the consumption. A supplied `-cli-checksum` makes claustrum
      verify the blob against it, and without that flag claustrum does not
      verify the blob. That is a conditional divergence from the reference, and
      the caller activates it. See [D1](DIVERGENCES.md#d1).
    - `-cli-url` makes claustrum download the blob. Claustrum then verifies its
      SHA-256 against `-cli-checksum` *unconditionally* (even an empty checksum
      fails).
- Claustrum decompresses the blob with zstd, applies `chmod 0755`, and probes it
  again for runnability. It does all of this at a temp path, then renames the
  file into place atomically. An interrupted install never leaves a
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
  to the detached child over a pipe. This handoff never touches disk.
- Once listening, the daemon persists the token to `daemon.token` (`0600`)
  beside the socket. It writes a temp file and renames it, so the write is
  atomic. A client that reconnects can then authenticate again after that source
  is gone. The daemon unlinks `daemon.token` on graceful shutdown. This is
  behavioral parity with reference `5db5e4a` (`tokenpersist.go`). See
  [PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken).
- An internal sentinel, `CLAUSTRUM_DAEMON_CHILD`, gates the self-daemonize
  re-exec. The name is claustrum's own on purpose. It is *not* the reference's
  `CLAUDE_SSH_DAEMON_CHILD`. A surrounding claude-ssh session exports that name
  to every descendant. If claustrum used that name as its sentinel, the launcher
  mistakes itself for the child and skips its own daemonize and token-forward
  path. The child unsets the sentinel after it reads it.
  Claustrum keeps reference parity by a separate route. `daemonizeWithToken`
  still sets `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's environ, so
  `process.spawn` children inherit it exactly as they do under the reference (see
  [PROTOCOL.md](PROTOCOL.md)).
- Under a `run/<clientId>/` socket, `19f30c46` launches `process.spawn` children
  through a self-re-exec exec-child trampoline. The child then carries a
  `CLAUDE_SSH_CHILD=<pid>:<startTicks>` identity and `CLAUDE_SSH_RUN_DIR`. That pair is a
  stable marker in the child's environment, and pid reuse cannot confuse it. The target's
  Go-runtime env is held and restored across the re-exec. Claustrum reproduces this
  on linux and darwin (shared
  `execchild_unix.go`, with the start-time source in `execchild_linux.go` and
  `execchild_darwin.go`). On windows the reference daemon reports it is not the run-dir lock
  holder and stamps neither marker. A windows VM showed a child under a run-shaped socket has
  the same environment as one under a bare socket.
  This behavior is off-wire. See [PROTOCOL.md](PROTOCOL.md) → process.spawn.
- Under that same socket the daemon also records each spawned child to
  `<runDir>/children/<pid>.json` (`childrecord.go` + `childrecord_linux.go` /
  `childrecord_darwin.go`). The file is an ordered JSON record. A later daemon reads it to
  reap children that a since-exited daemon left behind. The write is atomic on linux and
  darwin. On windows the
  reference daemon reports it is not the run-dir lock holder, so it records no children. A
  windows VM showed a spawned child leaves the children dir empty. This behavior is
  off-wire. See [PROTOCOL.md](PROTOCOL.md) → process.spawn.
- At `-serve` startup the daemon reaps those
  records (shared `childreap.go`, with the live-process read in `childreap_linux.go` /
  `childreap_darwin.go`). The reap happens right after the daemon claims the run dir.
  That claim evicts a live predecessor on the same socket, unless eviction is refused.
  Three conditions must hold before the daemon reaps a child. The record's node matches ours
  (same boot and machine). The owning daemon is gone. The live process still is that
  recorded child. For that third condition, the start-time matches, so there is no pid
  reuse. The process leads its own group and runs the recorded program. It also carries
  `CLAUDE_SSH_RUN_DIR` for this run dir plus a self-naming `CLAUDE_SSH_CHILD`. A verified
  orphan's process group gets `SIGTERM`, a 2s grace, then `SIGKILL` and a 1s escalate.
  These two timings are claustrum's own values, not probe-measured.
  The record is then forgotten. A record whose owner is still alive, or from another boot
  or machine, is never reaped. It runs on linux and darwin. On windows the reference daemon
  reports it is not the run-dir lock holder, so it reaps nothing. A windows VM showed planted
  child records survive a daemon startup untouched. The kill and every live-process read sit
  behind seams, so tests exercise the decision
  without ending a real process. This behavior is off-wire. See
  [PROTOCOL.md](PROTOCOL.md) → process.spawn.
- Also at `-serve` startup the daemon starts a periodic host cleaner (the shared decision
  layer in `hostclean.go`, with the OS reads in `hostclean_linux.go` / `hostclean_darwin.go`).
  The cleaner is a background sweep that keeps the install tidy. It runs 15s after startup
  and then daily. It is confined to the install roots derived from the daemon's own
  socket and executable (`<root>/run` and `<root>/srv`). It is also confined to this
  user's own processes. It therefore never reaches an unrelated process or path. Each pass first makes sure that
  its own socket still leads to itself, and without that the pass does not act.
  A pass then ends stranded sibling daemons. A stranded sibling is our `--serve` daemon,
  older than 5 min, and carrying the daemon-child marker. Its socket no longer leads to it,
  and it holds no run-dir lock. Each one gets `SIGKILL`, and its leftover child groups get
  `SIGTERM`→`SIGKILL`.
  A pass also ends orphaned Claude Code process groups. An orphan is a `stream-json` daemon
  child whose daemon is gone. It gets `SIGTERM`, a 3s grace, then `SIGKILL`.
  The 15s delay, the daily period, the 5 min age, the 3s grace and the 30-day idle age
  are claustrum's own values, not probe-measured.
  A pass also retires stale run dirs. A stale run dir is idle past 30
  days, its socket is unanswered, and it has no live lock. The cleaner renames it aside and
  then removes it. If a socket or lock reappears mid-removal, the cleaner undoes the removal.
  The cleaner spares an ambiguous daemon or orphan with a logged
  reason. It keeps a run dir it cannot probe conclusively. It never touches a fresh, locked,
  or foreign daemon. "Busy" is weaker than the other three: it is a 3-second sampling
  window, not a state. A daemon that is idle across the window and gains a client a
  moment later is still signalled. This path is DESTRUCTIVE and host-wide. Every process read,
  kill, dial, rename and remove therefore sits behind a seam, and tests never touch a real
  process or file. Its real behavior is measured only on a throwaway VM, never on a host that
  runs sibling daemons. It runs on linux and darwin. On windows it is a no-op. This
  behavior is off-wire. Claustrum reproduces this
  cleaner's behavior rather than matching it byte
  for byte. It approximates some spare-reason bookkeeping. On macOS it reads an `lsof` run it
  gave up on as busy, and the reference side of that is not probe-measured. That one is a numbered divergence,
  [DIVERGENCES.md](DIVERGENCES.md) D17. The reap path acts only on a dead socket. After 30 days
  of run-dir idleness the retire path can SIGTERM a socket-live daemon.
- On Unix, claustrum extracts the interactive PATH from the login shell in a
  separate goroutine. A slow login shell therefore does not delay the moment the
  socket becomes available.
- With `-listen-pipe` (Windows-only, opt-in), the daemon *additionally*
  opens a Windows named pipe. That pipe serves the identical JSON-RPC dispatch
  through a second `acceptLoop` over the same `serveConn`. Before it accepts on
  the pipe, the daemon publishes the chosen pipe name to `rpc.pipe` beside the
  socket. It removes `rpc.pipe` on graceful shutdown. The transport is strictly
  additive, and the socket path does not change.
- The daemon makes no outbound network connections. Every dial it makes is to a
  local `AF_UNIX` socket. It opens no inbound
  listener beyond the socket unless the operator opts into `-metrics-addr` (TCP)
  or `-listen-pipe` (a local, owner-only Windows named pipe).

### 3 · JSON-RPC multiplexer + replay

This role uses one persistent socket, concurrent request dispatch, and in-band
token auth. It also keeps a per-process frame buffer (`process.*`). A client that
connects late, or that reconnects, catches up on that buffer with `reattach`.

(`-bridge` is a fourth, trivial mode: a dumb stdio↔socket relay. An SSH
session attaches to it. It injects no auth.)

## Concurrency & replay model

- Each request on a connection runs in its own goroutine. A mutex serializes the
  per-connection writer, so responses and stream frames interleave safely.
- Each managed process has a monotonic `seq`, an append-only frame buffer, and a
  set of subscriber connections. `spawn` subscribes the connection that spawned
  the process. `reattach` REPLACES the whole subscriber set with the requester. It then
  replays buffered frames with `seq > fromSeq`. Any connection attached before
  stops receiving frames for that process. Since `90fca6e6` it also closes each
  connection it displaced. Claustrum detaches a dead
  subscriber (`[frameSink] replay write failed, detaching`).

## Deployment lifecycle (how a driver uses it)

A driver (Claude Desktop, or your own tool such as clauster) typically does this:

1. Probes the remote OS and arch.
2. Makes sure that the daemon binary is present on the remote, for example by
   uploading it.
3. Runs `claustrum -install …` to make sure that the agent CLI is present.
4. Starts `claustrum -serve -socket … -token-file …`.
5. Attaches per session with `claustrum -bridge -socket …` and speaks JSON-RPC
   (in-band auth) through it.
6. Drives the agent and any MCP servers as `process.spawn` children. It writes to
   each child's stdin with `process.stdin` (base64), and it reads that child's
   stdout from the stream notifications.

## Inherited wire bytes

Go's standard library, not claustrum's code, produces some of claustrum's
byte-identical output. The reference is also a Go program, so its stdlib does
unpaid parity work for us. That is a real asset, but inherited agreement is
agreement nobody verified.

Two things follow. First, a Go upgrade that changes an escaping rule moves the
wire, and this repo shows no diff. Second, a reimplementation in another language
must re-derive every row below by hand. The list is the honest cost estimate for
that work.

The stdlib decides the rows marked **inherited**. **Deliberate** means claustrum
chose the mechanism *because* it reproduced the reference. That is inherited
behavior that someone measured and adopted, rather than inherited behavior nobody
looked at.

| Surface | Where | Source | Status |
|---|---|---|---|
| `id` echo: `1.0`→`1`, `1e2`→`100`, big-int precision loss, map keys sorted | `rpc.go` (`request.ID interface{}`) | `encoding/json` round-trip through `interface{}` | **deliberate**. `json.RawMessage` reproduced none of it. Measured |
| Invalid UTF-8 in a result string → one `\ufffd` escape per bad byte. NUL → `\u0000` | `files.read` `content`, any string field | `encoding/json` encode | inherited |
| `<` `>` `&` → `\u003c` `\u003e` `\u0026` | every string field and error message | `encoding/json` HTML escaping, on by default | inherited |
| Invalid UTF-8 in a request param replaced with U+FFFD before dispatch sees it | every path param | `encoding/json` decode | inherited |
| `chdir <p>: stat <p>: no such file or directory` | error messages built from `err.Error()` | `os` `*PathError` (`op + " " + path + ": " + errno`) | inherited |
| Frame `data` alphabet and `=` padding | `process.*` stdout/stderr frames | `base64.StdEncoding` | inherited |
| Result field ORDER | `results.go` | `encoding/json` emits struct fields in declaration order | **deliberate**. Order is chosen to match, and results are structs, never maps, precisely because maps sort |
| Path cleaning for `~`-prefixed paths | `expandpath.go` | `filepath.Join`/`Clean` | **deliberate**. The lexical clean was measured and matched (PR 205) |

The decode-side row is the one row with a user-visible consequence: a file
whose name is not valid UTF-8 cannot be addressed through the protocol at all,
on either daemon. The request decoder substitutes U+FFFD before any method runs,
so the daemon operates on a name that does not exist. This is not a claustrum
limitation to fix. It is the reference's behavior, and claustrum inherits it by
the same route.

`inherited_encoding_test.go` and `inherited_encoding_unix_test.go` pin these
rules against regression. For the encode-side rules they assert escape TEXT
(`\ufffd`). For the decode-side rule they assert the U+FFFD CHARACTER. Which side
substituted is what distinguishes them.

## Operational logging

The logging mirrors the reference daemon:

- The readiness banner (`… remote server listening on <socket>`) goes to
  stdout, without a prefix. Only its product name is different (rebranded).
- Every operational or diagnostic line goes to stderr through the standard
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
  as a short tag *before* the `[Component]` prefix, as in `INFO  [Server] New
  connection from: …`. The prefixes thus stay byte-intact, and any grep for
  `[Server]`, `[process.Manager]`, `[frameSink]`, or `[shellenv]` keeps matching.
- Claustrum sets the threshold once, at startup, from `CLAUSTRUM_LOG_LEVEL`
  (`debug`|`info`|`warn`|`error`).
- The threshold defaults to `debug`. An unset (or unrecognized) value emits
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
                              // slower than -libc-probe-timeout falls back to the
                              // loader-glob result when that is set; no deadline by
                              // default (D14).
                              // The driver uses
                              // this field to pick which CLI build to download —
                              // a third-binary claim; see the provenance note below.
  "cliPath": "<cli-dir>/<cli-version>",
  "cliWasPresent": false,     // true only if it existed AND answered --version — within
                              // -cli-probe-timeout when that is set; no deadline by default (D11)
  "cliError": "…",            // omitted on success
  "fetch": {                  // 4534d86: present (LAST) whenever a -cli-url download was
    "bytes": 0,               // attempted, even a 0-byte 404; omitted on -cli-zst / cache hit
    "ms": 0,                  // download duration
    "longestPauseMs": 0       // largest gap between reads (~60000 on a read-idle stall abort)
  }
}
```

This section covers the `cliError` strings. Every error that `ensureCLI` returns
lands here verbatim, with the wrapping prefix that its phase adds. That is the
form a driver sees. The table below groups the strings by phase and quotes no
count. If you need a count, re-derive it from `ensureCLI`:

| phase | string |
|---|---|
| version check | `cli version "<v>" must be a single path component` |
| | `cli version "<v>" collides with the install temp sweep` |
| | `cli version "<v>" collides with the install download blob` |
| source | `cli <v> missing and no --cli-url or --cli-zst provided`. A second route reaches it. A present, working CLI answers `--version` more slowly than an opted-in `-cli-probe-timeout`, and no source flag was given. That route is unreachable at the default, which has no deadline (D11) |
| | `opening input: <err>` (`-cli-zst` read) |
| download | `download failed: <err>`. This is the catch-all wrap for any download error that is neither a bare status nor a bare stall. Those errors are a transport failure and a disk error while streaming the body. They also include the D10 cap (`response exceeds <n> bytes`) and the D12 deadline (`context deadline exceeded (…)`, off by default) |
| | `download failed with status <code>`. Non-200, no URL, no reason phrase (bare, no prefix) |
| | `download stalled: no data for <n>s after <got>/<total> bytes`. This is the 60s read-idle abort (always-on, `4534d86` parity, bare, no prefix) |
| verify | `checksum mismatch: expected=<a>, actual=<b>` |
| install | `mkdir cli dir: <err>` |
| | `staging cli: <err>` |
| | `decompressing: <err>` |
| | `decompressing: decompressed CLI exceeds <n> bytes` |
| | `installed cli at <path> is not runnable` |
| | `clearing stale dir at <path>: <err>` |
| | `staging file vanished before install: <err>` |

The download forms use different wording on purpose, to match the reference. The
status and stall forms are fully worded and go out bare. Every other download
error carries the `download failed: ` prefix. Those other errors are a transport
failure, the D10 cap, the D12 deadline, and a disk error. The
status form omits the URL, so a signed URL cannot reach whatever captures the
`__INSTALL_RESULT__` line. A bare `chmod`/`rename` failure propagates as the raw
Go error.

These strings are not free-form diagnostics: the driver reads them. A change to a
row's wording can change what a user sees. No JSON-RPC frame has to move. A
new guard whose error pre-empts a row can do the same. D10's opt-in cap is the
worked example. How the driver classifies them is a third-binary claim. See
[Driver claims and their provenance](#driver-claims-and-their-provenance).

## Driver claims and their provenance

A few facts these docs rely on describe a third binary. That binary is the driver
(Claude Desktop, or a tool such as clauster). The reference-vs-claustrum harness compares
two daemons, so it cannot settle any of these facts. Three of them are
load-bearing:

- `cliError` classification. The driver reads `cliError` as text. It
  surfaces a disk-full-shaped message as a terminal error with an actionable
  code, and everything else as retryable. The claim is about that classification
  only. A `-cli-url` rung whose download was forced to fail returned a
  non-disk-full `cliError`. Desktop answered it with a further rung rather
  than a terminal report. That shows escalation on a failed rung. It does not
  distinguish reading the `cliError` text from reacting to any non-success, and
  the terminal arm is still unobserved.
- `libc` build selection. The driver uses the `__INSTALL_RESULT__` `libc`
  field to pick which CLI build to download.
- "Desktop owns the argv". An operator has no way to influence the daemon's
  argv. A divergence reachable only through an argv flag is therefore unreachable
  on Desktop-driven hosts. This premise lets D3, D4, D5, D10, D11, D12 and D14 be
  opt-in. It also defines the "(opt-in)" tagging convention: a flag and
  a configuration key, because the configuration key is the reachable knob.

"The harness cannot settle it" is not "unverifiable". Each claim has a
fixture that can settle it. That fixture runs against the *driver*, with a control
that can come out wrong. The argv row's fixtures were run. The `cliError` and
`libc` fixtures were not run.

| claim | fixture | control that must fire |
|---|---|---|
| `cliError` classification | two `-install` failures whose messages straddle the disk-full shape. Observe a retry vs a terminal report | a genuine disk-full failure observed as terminal with an actionable code, proving the terminal side is reachable |
| `libc` build selection | a stub `ldd` printing a musl banner (its exit code is not consulted since 3ef9370, and its output outranks the loader glob). The daemon then reports `musl`. See which build the client fetches | a glibc host where the daemon reports `glibc` fetches the glibc build, so the musl arm is distinguishable from the client's default |
| argv | inspect the shipped client for where the daemon's argv is built. Corroborate with the setup UI, a capture of the argv Desktop passes (cache-hit and fetching), and an enumeration of Desktop's configuration files | if the argv is assembled from a setting or a configuration file, the claim is false |

The argv claim is discharged. Claude Desktop builds the daemon's argv from a
fixed set of flags. Only their operands are runtime values: the download URL, its
checksum, and the uploaded blob path. There is no operator-reachable route to add or
change a flag. This was found by inspecting the shipped client (2026-08-09). The fetching
`-install` argv is observed over two cold starts on one host (2026-08-10). The observed
argv was bare, then `-cli-url` + `-cli-checksum`. Once, with that download forced to
fail, an SFTP upload was re-invoked as `-cli-zst`. Both records live in `scratch/`
(gitignored).

The `cliError` and `libc` claims remain design constraints. The argv claim is a
driver result, not a parity one. Five argv dependents (D3, D4, D5, D12, D14) carry
reopen triggers for their own behavior. The other two (D10, D11) carry riders about
the `cliError` claim, as does D13. All live in [DIVERGENCES.md](DIVERGENCES.md). If
someone finds a route to influence the daemon's argv, the argv claim reopens. A
Desktop release that adds such a route reopens it too. That route is a new configuration
field, or a configuration file it turns into argv. A forwarded env var does not
qualify, because nothing in `config.go` or `main.go` reads the
environment for these knobs. The dependents list is maintained by hand, and it was
incomplete every time somebody checked it. Treat it as best-known, not complete. The
argv claim underpins D3, D4, D5, D10, D11, D12 and D14. `cliError` underpins D10,
D11 (retraction rider), D13 and clause (c)'s error-string rider. `libc` underpins
D14's residual delta. Two further driver claims are untracked and unprovenanced:
D6's and D7's clause-(b) evidence, which rests on what Desktop emits as
`-cli-version`.
