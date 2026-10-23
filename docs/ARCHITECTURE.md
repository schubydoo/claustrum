# claustrum architecture

Claustrum is one Go binary. A flag selects the mode. The build is static
(`CGO_ENABLED=0`). It has two cross-platform dependencies, `klauspost/compress`
(zstd) and `golang.org/x/sys` (system calls on Linux, macOS and Windows). One
more dependency goes into Windows builds only. It is
`github.com/Microsoft/go-winio` (the opt-in `-listen-pipe` named-pipe transport).

## Source layout (flat `package main`)

| file | responsibility |
|---|---|
| `main.go` | flag parsing, mode dispatch, version resolution |
| `server.go` | `-serve` daemon: AF_UNIX listener, per-connection loop, concurrent dispatch, graceful shutdown (kills children, or leaves them with `-keep-children`). The flag works on Windows too, as claustrum's own flag: `89cb6289` exits 2 on it |
| `rpc.go` | JSON-RPC request and response types, error codes, dispatch + params gate |
| `results.go` | result structs (field order is part of the wire contract) |
| `methods_server.go` / `methods_files.go` / `methods_git.go` / `methods_launcher.go` / `methods_process.go` / `methods_plugins.go` | the 20 method handlers (`19f30c46` added `plugins.prune`, `89cb6289` added `launcher.resolve`). `methods_launcher.go` also holds the settings reader, the `process.spawn` launcher checks and the child env strip |
| `managedlauncher*.go` | the managed launcher's OS split and the launcher runs of `-install` and `-probe-cli` (`89cb6289`) |
| `handlers/prune.go` | the sole non-`main` package: `handlers.PruneParams`, so `plugins.prune`'s `-32602` body carries the reference's `handlers.PruneParams` type name byte-for-byte |
| `process.go` | process manager: registry, per-process seq, replay buffer, subscribers. It captures the immutable `pid`/`startTime` pair behind the CT-1 `wantPid` opt-in |
| `bridge.go` | `-bridge` relay and `-stop` |
| `install.go` | `-install`: download/verify/extract/prune + `__INSTALL_RESULT__` facts |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`). The level tag precedes the byte-intact `[Component]` prefixes |
| `metrics.go` | opt-in Prometheus counters at `/metrics` (`-metrics-addr`, with no listener by default) |
| `wirelog.go` | opt-in `-wire-log` frame capture. A pure side channel over already-marshaled bytes, off by default, with by-key credential redaction |
| `sysproc_unix.go` / `sysproc_windows.go` | the kill of a child: the process group on Unix (setpgid + negative-pid signal), the direct child only on Windows (no Job Object, as on the reference). A Windows VM measured that against `89cb6289`: no child is in a job of the daemon |
| `pipetransport.go` | `-listen-pipe` shared helpers: `rpc.pipe` name-file lifecycle (atomic write / remove), owner-only SDDL builder, pipe-name + instance-id generation (all platform-neutral) |
| `pipetransport_windows.go` / `pipetransport_other.go` | the optional Windows named-pipe listener (`startPipeTransport` via go-winio, owner-only DACL) vs the non-Windows no-op stub + `honorListenPipe` warning |
| `detach_unix.go` / `detach_windows.go` | daemonize attr (setsid vs DETACHED_PROCESS with `CREATE_BREAKAWAY_FROM_JOB`) and the start of the daemonized child. On Windows the daemon starts outside the job of its launcher. If that job refuses breakaway, the daemon starts inside it and the launcher logs one line, as `89cb6289` does (row JB of a Windows VM) |
| `shellenv_unix.go` / `shellenv_windows.go` | login-shell PATH extraction (Unix) / the daemon's own PATH for `process.spawn` children (Windows) |
| `shellagent.go` / `shellagent_unix.go` / `shellagent_windows.go` | `process.spawn` SSH agent hand-off: the login shell's `SSH_AUTH_SOCK`, checked and cached (Unix) / no-op (Windows) |

Every OS serves the same JSON-RPC methods. Only the OS-split files
(`*_unix.go`, `*_linux.go`, `*_windows.go` and their kin) are different.

## The three runtime roles

### 1 · CLI-version manager (`-install`)

This mode makes sure the pinned `claude` CLI is present under `-cli-dir`. With
no `-cli-dir` the folder is `<home>/.claude/remote/ccd-cli`. On Windows the CLI
file is `<cli-version>.exe`.

- If `<cli-dir>/<cli-version>` exists *and* is runnable (`<cli> --version`
  exits 0), claustrum keeps it as it is. That direct run is stopped when it has
  not ended after 30 s, as on the reference. `-install` then reports the CLI as
  unresponsive and installs nothing. The direct run of a CLI that this `-install`
  put in place is stopped after 120 s. A managed launcher run has 33 s and
  123 s. See
  [PROTOCOL.md](PROTOCOL.md) → `-install`. The retired
  [D11](DIVERGENCES.md#d11) holds the history of this bound.
- If not, claustrum gets the blob from one of two sources:
    - `-cli-zst` names a local `.zst` file. Claustrum removes it once the new
      CLI is in place, before the `--version` run. Every earlier failure keeps
      it. A supplied `-cli-checksum` makes claustrum
      verify the blob against it, case-sensitively. Without that flag claustrum
      does not verify the blob. The reference does the same. Claustrum creates
      the cli-dir before it opens the blob.
    - `-cli-url` makes claustrum download the blob. Claustrum verifies its
      SHA-256 against `-cli-checksum` *unconditionally* (even an empty checksum
      fails).
- Claustrum decompresses the source with zstd to a temp path, verifies the
  checksum, applies
  `chmod 0755` and renames the file into place atomically. An interrupted
  install never leaves a half-written CLI. A folder at the final path is removed
  before the rename, and a file there is replaced by it. Claustrum then runs
  `<cli> --version` at the final path, and it removes a new CLI that exits
  non-zero.
- Claustrum prunes the version files to the `-cli-keep` newest (by mtime,
  default 3). The new CLI always stays, and a negative keep counts as 0. Every
  other entry stays. The prune and the sweeps skip an entry that is the home
  folder or holds it ([D2](DIVERGENCES.md#d2)).
- Claustrum prints one line: `__INSTALL_RESULT__{json}`.

### 2 · Daemon / process supervisor (`-serve`)

Claustrum daemonizes itself: it detaches from the controlling session and, on
Unix, reparents to init. It then opens the `0600` socket. It supervises the
children it spawns. The rules of each step are in PROTOCOL.md. This list names
the files and the seams.

- **Token.** `-token-file` or `-token-fd` gives the token. `tokenpersist.go`
  writes `daemon.token` and unlinks it at a graceful stop if it is still its
  own file. See
  [The token source](PROTOCOL.md#the-token-source) and
  [Token persistence](PROTOCOL.md#token-persistence-daemontoken).
- **Re-exec sentinel.** An internal sentinel, `CLAUSTRUM_DAEMON_CHILD`, gates the
  self-daemonize re-exec. The name is claustrum's own on purpose. It is *not* the
  reference's `CLAUDE_SSH_DAEMON_CHILD`. A surrounding claude-ssh session exports
  that name to every descendant. If claustrum used that name as its sentinel, the
  launcher mistakes itself for the child and skips its own daemonize and
  token-forward path. The child unsets the sentinel after it reads it. Claustrum
  keeps reference parity by a separate route. `daemonizeWithToken` still sets
  `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's environ, so `process.spawn` children
  inherit it as they do under the reference (see
  [The re-exec marker](PROTOCOL.md#the-re-exec-marker)).
- **Identity markers.** On Linux and macOS each `process.spawn` child starts
  through the exec-child trampoline, or directly when the trampoline does not
  start: `execchild_unix.go`, with the start-time
  source in `execchild_linux.go` and `execchild_darwin.go`. The target's Go-runtime
  env is held and restored across the re-exec. See
  [process.spawn](protocol/process-spawn.md#the-identity-markers-linux-and-macos).
- **Child records and the reap.** `childrecord.go`, `childrecord_unix.go` and
  `childrecord_linux.go` / `childrecord_darwin.go` write and remove the records.
  `childreap.go`, with the live-process read in `childreap_linux.go` /
  `childreap_darwin.go`, reaps them at the start. Both act through the run dir that
  the daemon holds open, so no write or read follows a symlink out of it. The kill
  and every live-process read sit behind package-var seams, so tests exercise the
  decision without ending a real process. After the `SIGKILL` the reap waits 1 s
  (`reapEscalate`, claustrum's own value). See
  [Child records and the reap](PROTOCOL.md#child-records-and-the-reap-linux-and-macos).
- **Host cleaner.** `hostclean.go` holds the shared decision layer, and
  `hostclean_linux.go` / `hostclean_darwin.go` hold the OS reads. Windows links no
  cleaner. The gate, the signals, the `lsof` reads and the log lines are in
  [Host cleaner](PROTOCOL.md#host-cleaner-off-wire-linux-and-macos). These rules
  are not under that heading:
    - The first pass runs 15 s after the start, then one pass a day. The cleaner
      acts only on this user's own processes and on the `run`, `srv` and `ccd-cli`
      folders of its own root. A pass acts only after it confirms that its own
      socket still leads to itself.
    - An orphan is a `stream-json` daemon child that leads its own process group.
      Its parent is pid 1, its binary sits directly in `<root>/ccd-cli`, and its
      stdin, stdout and stderr are pipes. Its group gets `SIGTERM`, a 3 s grace,
      then `SIGKILL`. One pass ends 64 groups at most. A daemon or an orphan
      younger than 5 minutes is never touched. An ambiguous orphan is spared with
      a logged reason.
    - A stale run dir is idle past 30 days, or its name carries `.removing-`. Its
      socket does not answer, and no live process holds its lock. The pass reads
      the idle age of each run dir before it dials any daemon, because a dial
      writes a line to that daemon's log. It skips its own run dir and every entry
      that is not a plain directory, a symlink included. It reads the idle age
      again before it probes a dir, and it keeps a dir that came back into use,
      unless the dir has a staging name. It renames a stale dir to
      `<name>.removing-<own pid>-<base-36 time>` inside the run root and then
      removes it. If a socket appears after the rename, or the lock becomes held
      or unreadable, it renames the dir back. A dir whose daemon it retires goes
      in the same pass, once that daemon's socket is gone. A dir it cannot probe
      conclusively stays.
    - "Busy" is a 3 s sampling window, not a state. A daemon that is idle across
      the window and gains a client a moment later still gets `SIGTERM`.
    - The limit lines are four: more than 4096 processes, more than 64 orphan
      groups, more than 64 run dirs, and 8 retire attempts in one pass. The log
      lines carry the `[hostclean]` prefix after claustrum's level tag. The line
      before a `SIGKILL` and the reasons for a held lock and for a listener that
      is not our daemon use the texts of the reference too.
    - The 15 s delay, the 3 s grace and the limit of 64 groups match the
      reference. The daily period, the 5 minute age and the 30-day idle age are
      claustrum's own.

  This path is destructive and host-wide. Every process read, kill, dial, rename
  and remove sits behind a seam, and tests never touch a real process. A test that
  lets a rename or a remove run for real does so inside its own temp dir.
- **Login-shell PATH.** `shellenv_unix.go` runs the read at the first
  `process.spawn`, in a process group of its own. At the 4 s cap it kills that
  group. `shellenv_windows.go` reads the daemon's own PATH. See
  [The login-shell PATH](PROTOCOL.md#the-login-shell-path-linux-and-macos).
- **Named pipe.** With `-listen-pipe` (Windows only, opt-in) a second
  `acceptLoop` serves the same `serveConn` over a named pipe
  (`pipetransport*.go`). See
  [Named-pipe transport](PROTOCOL.md#named-pipe-transport-windows-opt-in).
- **Network.** The daemon makes no outbound network connections. Every dial it
  makes is to a local `AF_UNIX` socket. It opens no inbound listener beyond the
  socket unless the operator opts into `-metrics-addr` (TCP) or `-listen-pipe`.

### 3 · JSON-RPC multiplexer + replay

This role uses one persistent socket, concurrent request dispatch, and in-band
token auth. It also keeps a per-process frame buffer (`process.*`). A client that
connects late, or that reconnects, catches up on that buffer with `reattach`.

(`-bridge` is a fourth, trivial mode: a dumb stdio↔socket relay. An SSH
session attaches to it. It injects no auth.)

## Concurrency & replay model

- Each request that passes the parse and auth checks runs in its own
  goroutine. The read loop writes the parse and auth errors itself. A mutex
  serializes the per-connection writer, so responses and stream frames
  interleave safely.
- Each managed process has a monotonic `seq`, an append-only frame buffer, and a
  set of subscriber connections. `spawn` subscribes the connection that spawned
  the process. A process whose `id` a later spawn took keeps its subscribers, as
  on `89cb6289` (Linux and macOS row P2, Windows row EVc). `reattach` REPLACES the whole subscriber set with the requester. It then
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
  and exit lines, `[daemon]`, `[shellenv]`, and `[frameSink]`.
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
                              // still running at 5 s is killed, its output is
                              // dropped, and the loader glob decides.
                              // The driver uses
                              // this field to pick which CLI build to download —
                              // a third-binary claim; see the provenance note below.
  "cliPath": "<cli-dir>/<cli-version>", // <cli-version>.exe on Windows. It is "" when the
                              // cli-dir cannot be made, and without -cli-version
  "cliWasPresent": false,     // true only if it existed AND answered --version within 30 s.
                              // That is without CLAUDE_SSH_MANAGED_LAUNCHER=1; the gated rules
                              // are in PROTOCOL.md → -install
  "cliError": "…",            // omitted on success
  "cliUnresponsive": true,    // a --version run that its bound stopped. A direct run has 30 s,
                              // and 120 s after an install in the same run. A managed launcher
                              // run has 33 s and 123 s. Omitted when it does not apply.
  // 89cb6289: the launcher* fields appear only with CLAUDE_SSH_MANAGED_LAUNCHER=1,
  // each omitted when it does not apply. See PROTOCOL.md → -install.
  "fetch": {                  // 4534d86: present whenever a -cli-url download was
    "bytes": 0,               // attempted, even a 0-byte 404. Omitted on -cli-zst / cache hit.
    "ms": 0,                  // download duration
    "longestPauseMs": 0       // largest gap between reads (~60000 on a read-idle stall abort)
  },                          // It is the last field without the gate.
  "launcherStatus": "usable", // none / usable / unusable / unreadable / probe_failed / unresponsive
  "launcher": ["<argv>"],     // the launcher argv, when one was resolved
  "launcherSource": "<file>", // the settings file that gave the value
  "launcherPath": "<file>",   // the unreadable file or folder
  "launcherReason": "…",      // why it was refused, failed or stopped
  "launcherStderr": "…"       // the failed run's stderr, 8192 bytes kept
}
```

This section covers the `cliError` strings. Every error that `ensureCLI` returns
lands here verbatim, with the wrapping prefix that its phase adds. That is the
form a driver sees. The table below groups the strings by phase and quotes no
count. If you need a count, re-derive it from `ensureCLI`:

| phase | string |
|---|---|
| version check | `cli version "<v>" must be a single path component` |
| source | `cli <v> missing and no --cli-url or --cli-zst provided` |
| | `opening input: <err>` (`-cli-zst` read) |
| download | `download failed: <err>`. This is the catch-all wrap for any download error that is not one of the bare forms below. Those errors are a transport failure before the body and the 60 s response-header limit (`Get "<url>": net/http: timeout awaiting response headers`). They also include the D10 cap (`response exceeds <n> bytes`) and the D12 deadline (`context deadline exceeded (…)`, off by default) |
| | `download failed with status <code>`. Non-200, no URL, no reason phrase (bare, no prefix) |
| | `download stalled: no data for <n>s after <got>/<total> bytes`. This is the 60s read-idle abort (always-on, `4534d86` parity, bare, no prefix) |
| | `download interrupted after <got>/<total> bytes: <err>`. A body read that ended with a transport error (always-on, parity, bare, no prefix) |
| verify | `checksum mismatch: expected=<a>, actual=<b>` |
| install | `mkdir cli dir: <err>` |
| | `staging cli: <err>` |
| | `decompressing: <err>` |
| | `decompressing: decompressed CLI exceeds <n> bytes` |
| | `installed cli at <path> is not runnable` |
| | `cli unresponsive: the installed Claude Code binary started but did not answer --version within 30s (120s for a first run) and was stopped; the host is not letting it run (endpoint security software or a stalled network home are the usual causes)`. A direct `--version` run that its bound stopped |
| | `cli path must not be or contain the home directory: "<path>"`. The D2 home guard, claustrum-only |
| | `clearing stale dir at <path>: <err>` |
| | `rename: staging file vanished before install: <err>` |
| | `rename: <err>` |
| managed launcher | `cli unresponsive: the installed Claude Code binary was started through the host's managed launcher <argv0> and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish`. Only with `CLAUDE_SSH_MANAGED_LAUNCHER=1` |

The download forms use different wording on purpose, to match the reference. The
status, stall and interrupted forms are fully worded and go out bare.
`staging cli:` and `decompressing:` also go out bare on `-cli-url`. Every other
download error carries the `download failed: ` prefix. Those other errors are a
transport failure before the body, the response-header limit, the D10 cap, the
D12 deadline. The
status form omits the URL, so a signed URL cannot reach whatever captures the
`__INSTALL_RESULT__` line. A bare `chmod` failure propagates as the raw
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
  on Desktop-driven hosts. This premise lets D3, D10 and D12 be
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
driver result, not a parity one. Two argv dependents (D3, D12) carry
reopen triggers for their own behavior. The other one (D10) carries a rider about
the `cliError` claim. All live in [DIVERGENCES.md](DIVERGENCES.md). If
someone finds a route to influence the daemon's argv, the argv claim reopens. A
Desktop release that adds such a route reopens it too. That route is a new configuration
field, or a configuration file it turns into argv. A forwarded env var does not
qualify, because nothing in `config.go` or `main.go` reads the
environment for these knobs. The dependents list is maintained by hand, and it was
incomplete every time somebody checked it. Treat it as best-known, not complete. The
argv claim underpins D3, D10 and D12. `cliError` underpins D10
and clause (c)'s error-string rider. Both underpinned the retired D11. `cliError` also underpinned the retired D13. The argv
claim underpinned the retired D5. `libc`
underpins no current entry. It underpinned the retired D14. One further driver claim is
untracked and unprovenanced: D6's clause-(b) evidence, which rests on what Desktop
emits as `-cli-version`.
