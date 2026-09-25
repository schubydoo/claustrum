# claustrum protocol reference

claustrum uses newline-delimited JSON-RPC 2.0 over an `AF_UNIX`
`SOCK_STREAM` socket. This document is the complete wire contract. The validation
battery checks that same contract byte-for-byte. All reference behaviour was
probed at `5db5e4a` unless a line says otherwise. The divergence catalog, its
rules, and the reopen triggers are in [`DIVERGENCES.md`](DIVERGENCES.md).

## Transport

- One JSON object per line (NDJSON). No length prefix, no binary framing.
- A single request line has a cap of 1 MiB (`bufio` max token = `1024*1024`).
  The daemon serves a line up to 1048575 bytes. A line of 1048576 bytes or more
  closes the connection with no reply. Chunk large `process.stdin` payloads below
  this cap.
- `AF_UNIX` stream socket, created mode `0600` (owner only).
- The connection is persistent. It stays open after a response, and id-less
  stream notifications arrive on it asynchronously.
- The daemon dispatches a connection's requests concurrently. Responses can
  arrive out of request order. Match them by `id`.

### Named-pipe transport (Windows, opt-in)

This is a strictly additive claustrum extension (CT-5). The reference daemon has no
such transport. It is off by default. When it is off, claustrum is byte-for-byte
identical to the reference. Set `-serve -listen-pipe`, or set `listen-pipe = true`
in `claustrum.conf`. claustrum then *additionally* serves the exact same NDJSON
JSON-RPC dispatch over a Windows named pipe, concurrently with the socket. The
wire contract, field ordering, framing, and `"auth"` handshake are the same. It
exists so that a Windows client that cannot consume `AF_UNIX` can still connect.
One such client is Python `asyncio`, whose Unix transports are Unix-loop-only.

- **Windows-only.** Other platforms ignore it and log a warning.
- **Name and discovery.** claustrum chooses the name
  (`\\.\pipe\claustrum-<random-instance-id>`). It publishes that name to
  `rpc.pipe` in the socket's directory, beside `rpc.sock` and `daemon.token`.
  claustrum writes the file atomically before the pipe accepts and before the ready
  banner. On graceful shutdown claustrum removes the file only when the file is
  still the inode this daemon published. It uses `os.SameFile`, the same handoff
  guard that the socket and `daemon.token` carry. See
  [Token persistence](#token-persistence-daemontoken). A restart's successor
  therefore keeps its own pointer. The client reads that file to learn the opaque
  name.
- **Stale-file invariant.** The name is random for each boot. Therefore `rpc.pipe`
  exists if and only if a pipe is actively served this boot. Startup removes any
  leftover file from an unclean crash. A client can therefore never dial a stale
  name.
- **Owner-only and local.** Two independent mechanisms give this. The first is an
  owner-only DACL (SDDL `D:P(A;;GA;;;<current-user-SID>)`), the named-pipe
  analogue of the socket's `0600`. The second is remote-client rejection at
  creation (`FILE_PIPE_REJECT_REMOTE_CLIENTS`), set by go-winio's `ListenPipe`.
  See [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

See [`DIVERGENCES.md`](DIVERGENCES.md) → CT-5 for the full contract.

## Authentication

Every request carries a top-level `"auth":"<token>"`. `server.shutdown` is the one
exception, and it is not authenticated at all. A shutdown frame stops the daemon
whether its `auth` member is absent, empty, wrong, or valid. `-stop` sends no
`auth` member. This matches the reference, and the Desktop client depends on it.
Desktop stops the daemon with `server --stop --socket <sock>` from a bare SSH
command line, with no `CLAUDE_RPC_TOKEN` in its environment. The exemption covers
auth only. The daemon still rejects a shutdown frame that carries a bad or absent
`jsonrpc` version with `-32600`, and the daemon stays up. Every other method
rejects an unauthenticated request with
`-32001 Unauthorized: invalid or missing auth token`. It also logs
`[Server] Unauthorized request: method=…, id=…`.

The server's expected token comes from `-token-file` or from `-token-fd`. With
`-token-file` the daemon reads the file once at startup and then unlinks it. With
`-token-fd` the daemon reads the token from an open descriptor and forwards it to
the detached child over a pipe. That handoff never touches disk.

No claustrum mode reads `CLAUDE_RPC_TOKEN`. This holds for `-serve`, for
`-bridge`, and for `-stop`. `-bridge` is a simple relay and does not add auth. The
client that speaks through it must include `"auth"` itself, from the
`daemon.token` handshake below or from its launcher. claustrum only *removes* the
variable. It unsets the variable before it daemonizes, and it strips the variable
from every spawned child. Therefore a token never reaches a child through the
environment.

### Token persistence (`daemon.token`)

Once the socket is listenable, the daemon writes the token to `daemon.token` in
the socket's directory. The file has mode `0600`. The daemon writes it atomically
through a `daemon.token-*` temp file and a rename, and it unlinks the file on
graceful shutdown. A client can therefore reconnect to an already-running daemon
and re-authenticate after the original `-token-file` was unlinked, or after the
`-token-fd` pipe closed. The write does not depend on the token source, because it
uses the in-memory token. The write is also best-effort. The daemon logs a failure
(`[daemon] failed to persist token: …`) and continues. Reference build `5db5e4a`
added this, and claustrum matches it. It is off the JSON-RPC wire, because the
file sits beside the socket, not on it. An unclean kill (`SIGKILL` or a crash)
leaves the file behind, because the daemon removes it only on the graceful
`server.shutdown` / `SIGTERM` path. `-stop` also removes it after a failed
connect, unless it prints `survivor`. See [-stop](#-stop--ask-a-running-daemon-to-shut-down).

The fixed name and the socket-dir location are the reconnect contract, so they are
not configurable. On Windows `0600` is not an owner-only DACL. This is a Go
`os.CreateTemp` limitation, and the per-user session dir is the confinement. It is
a parity caveat that claustrum deliberately does not "fix". The former caveat "two
daemons in one directory collide on this file" is now bounded. On Linux and macOS
the run-dir lock (below) evicts a prior live daemon of the same socket before the
new one binds. Two same-socket daemons therefore no longer coexist to collide on
the honest path. A collision survives only where eviction is refused, and on
Windows, which ships no run-dir lock. Eviction is refused for a foreign or
cross-machine lock holder, and for a holder that survives `SIGKILL`.

The unlink carries an inode-ownership qualifier. On graceful shutdown the
daemon unlinks `daemon.token` only when the file on disk is still the inode this
daemon wrote. The daemon stats the file and
compares its identity (`os.SameFile`) to the one it recorded at startup. A
restart's successor can rebind the socket and
republish `daemon.token` to a new inode before the predecessor leaves. The
departing predecessor then leaves the file alone. It does not delete the
successor's token and break the new daemon's reconnect auth. The socket file
carries the same identity guard on the same graceful path. On Windows the
`rpc.pipe` pointer carries it too (`removeSocketIfOwned` /
`removePersistedToken` / `removePipeNameFileIfOwned`). Each removes its file only
when the recorded identity still matches. They differ only in the no-identity
fallback. A socket the daemon never recorded is removed with a plain unlink. A
token or pipe pointer with no recorded identity is left in place, not unlinked.
This is the departing-daemon half of the daemon-to-daemon handoff. The launcher
half is under [Daemon startup](#daemon-startup-serve). It is off the JSON-RPC
wire.

### Run-dir lock (`daemon.lock`)

Before it binds the socket, a `-serve` daemon takes an exclusive `flock` on
`daemon.lock` in the socket's directory (mode `0600`). It then writes an owner
record into that file. The record is a JSON object
`{pid, role, node, instanceId, startedAt}`, where `role` is `serve`. `node` is
omitted when the machine identity is unknown. Reference build `4534d86` added
this, and claustrum matches it. It is off the JSON-RPC wire, because it is a file
beside the socket. On graceful shutdown the daemon truncates the record and drops
the lock, but it leaves the file in place. `daemon.token` is unlinked instead. This
shutdown handling is claustrum's own, not probe-measured.

A prior live daemon can still hold the lock. The newcomer then evicts it before it
takes over, with `SIGTERM` and then `SIGKILL` after a grace period. A restart
therefore replaces its predecessor deterministically. The eviction only fires
against a holder that is verifiably one of our own `-serve` daemons for this
socket, on this machine. See the guards in `daemon_runlock_unix.go`. Claiming is
best-effort. Any failure logs a warning, and the daemon serves without run-dir
ownership rather than aborting.

The lock, the owner record, and the eviction run on Linux and macOS only. The
machine identity (`node`) is the boot id joined to the pid-namespace inode on
Linux, and `sysctl kern.bootsessionuuid` on macOS. Windows ships no run-dir
lock. Mutual exclusion on Windows stays the socket remove-then-rebind handoff.
On macOS claustrum makes sure of the holder through `sysctl KERN_PROCARGS2`,
where the reference skips that test. This holds for the
eviction and for `-stop`. See [DIVERGENCES.md](DIVERGENCES.md) D15.

### Host cleaner (off-wire, Linux and macOS)

A periodic sweep ends stranded sibling daemons and tidies stale run dirs. It
reaches no JSON-RPC frame, so nothing here is a wire contract. It is recorded
because one of its decisions is an always-on divergence that a client can feel as
a lost session.

Before it signals a daemon whose run dir went idle past the threshold, the cleaner
asks whether that daemon still has a live client. On macOS it asks `lsof`. That
run is bounded three ways: a command deadline, a wait for the output pipe, and a
last bound after which the run is given up on. Those bounds are claustrum's own
values and are not probe-measured.

An abandoned run reads as busy, not idle. See [DIVERGENCES.md](DIVERGENCES.md)
D17. A run that gave up and a run that finished and saw nothing both produce no
output. claustrum treats only the completed empty result as evidence. On a host
where `lsof` cannot answer, the cleaner therefore does not SIGTERM a daemon that
is serving a client. The reference side is not probe-measured.

### Daemon startup (`-serve`)

If the socket's parent directory is missing, the `-serve` launcher creates it
(mode `0700`). The launcher then does not return until the socket path exists. It
polls every 20 ms, up to a bound of 10 seconds. To make sure that the daemon is
ready, it dials the socket and closes the connection again. A freshly started
daemon's log therefore opens with a `New connection from: @` /
`Connection closed: @` pair from the launcher's own probe.

It waits for the path to exist, not for a successful dial. It also does not give
up early when the child dies. Both behaviours are measured against `5db5e4a`:

| start | what the launcher sees | outcome |
|---|---|---|
| normal | path appears, and the dial succeeds | exit `0` |
| socket path occupied by a directory | path exists immediately | exit `0` (~0.01 s, reference 0.08 s) |
| child can never bind (uncreatable parent dir) | path never appears | exit `1` at ~10.04 s (reference 10.06 s) |

On a timeout the launcher prints
`claustrum: timeout waiting for daemon to accept on <socket>` to stderr and
exits `1`. On success it prints the ready banner and exits `0`. After a successful
`-serve`, the socket accepts connections before `-serve` returns. Measurement
detail is condensed out of this reference.

Reference build `7d193f89` added a daemon-to-daemon socket handoff. A unix socket
path cannot be rebound in place, so a restart on a live socket unlinks the old
inode and binds a fresh one. Before it spawns the child, the launcher records
whether a live predecessor is already accepting on the path. If one is, the
launcher also records that socket's inode identity. The probe is a short dial on
unix. On Windows it is a no-op stub, and it has no observable effect. The launcher
then waits not merely for the path to exist but for the inode to become different
from the predecessor's. That is how it tells the successor's fresh socket from the
one the predecessor is still serving. A predecessor can be present and the
deadline can pass with the inode unchanged. The launcher then prints the distinct
message `claustrum: daemon did not take over <socket> (predecessor still owns it)`
to stderr and exits `1`, in place of the plain timeout line above. With no live
predecessor the wait is unchanged, because any present socket is the child's. The
inode-difference wait and that distinct message are unix-only. On Windows the
predecessor probe is a no-op that always reports no predecessor. Startup there
always takes the no-predecessor path, and on failure it emits only the ordinary
timeout line, never the "daemon did not take over" message. This is off the
JSON-RPC wire, because it is launcher lifecycle. The departing daemon's matching
half is the inode-ownership unlink under
[Token persistence](#token-persistence-daemontoken).

### Idle-connection close

A `-serve` daemon closes any accepted connection that goes 5 minutes with no read
or write activity. This matches `f6010b97`, as measured. The timeout is fixed and
always-on. There is no flag and no configuration key to change it or to disable
it. The daemon stamps the last activity time on every read and write of the
connection. A per-connection watcher polls for silence, at a quarter of the
timeout, bounded to at most 30 s. Once the idle span reaches the timeout, the
watcher closes the socket and logs
`[Server] closing idle connection <addr> (idle for <d>)`. The watcher stops as
soon as the connection closes for any other reason, and as soon as the daemon
shuts down. It therefore never outlives its connection. This is off the JSON-RPC wire, because it is connection lifecycle and
sends no frame. It closes only an idle *connection*, never the daemon. A
client-less orphan daemon is retired by the separate orphan-exit self-probe below.

### Daemon log (`remote-server.log`)

The launcher creates `remote-server.log` in the socket's directory with mode
`0600`. Every start gets a fresh file. The launcher rotates any existing log to
`remote-server.log.old` and creates a fresh one. This matches `4534d86`, which
keeps the previous session's log as `.old` on every restart. The launcher
redirects the daemonized child's stdout and stderr into that file, so the
launcher's own streams stay empty. The first line is the ready banner, with no
timestamp:

```
Claustrum remote server listening on /run/user/1000/claude/rpc.sock
2026/07/31 00:17:30 INFO  [Server] New connection from: @
```

For a planted symlink in a writable directory, claustrum renames the link, not its
target, to `remote-server.log.old`. It then creates a fresh regular log with
`O_EXCL`. The link is never followed, and the victim stays untouched. If claustrum
cannot rename the existing entry, the exclusive create fails too. One such case is a
sticky directory that holds another user's file or symlink. claustrum then declines the
log entirely and falls back to inherited stdio. In both cases claustrum never
follows the link, and it never writes into a file another user owns. This is
intentional divergence D8, and it is always-on. `4534d86` no longer
plain-truncates a foreign regular log, and claustrum matches the `.old` rotation. But in a root-owned sticky directory the
reference still follows a planted `remote-server.log` symlink. It then writes its
log into the victim, or it refuses to start. claustrum declines instead. The
trigger is not reachable on the deployed path, because the socket directory
(`~/.claude/remote/`) is per-user and not world-writable. That is why D8 is
always-on and not opt-in. See [`DIVERGENCES.md`](DIVERGENCES.md) → D8.

claustrum does not remove the log on graceful shutdown. This differs from the
socket and from `daemon.token`. The log outlives the daemon, so a post-mortem
stays readable. The fixed name and location are the deployment contract, and they
are not configurable.

### Orphan-exit self-probe

A running `-serve` daemon periodically tests whether its socket path still leads
back to itself. If it does not, the daemon shuts down. This matches reference
build 4534d86. It exists so that a predecessor whose socket a newer daemon took
over retires promptly, instead of lingering indefinitely. The 5-minute idle
timeout closes only an idle *connection*, never the daemon, so a client-less
orphan otherwise never exits. The probe is off the JSON-RPC wire. It has two
observable effects only: a self-directed `server.capabilities` request on the
socket once orphaned, which is a normal authed RPC, and stderr log lines. In
normal operation there is no self-RPC, only an `os.Stat`.

Every 60 seconds the daemon `os.Stat`s its socket path and compares the file
identity (`os.SameFile`) to the inode it bound. If the path is gone or now a
different inode, and no client is connected, it starts a 10-minute grace clock.
Any connected client resets the clock, and so does a path that still matches.
After the grace elapses with nobody connected, the daemon self-probes. It dials
its own socket, sends an authed `server.capabilities`, and compares the reply's
`instanceId` to its own. A probe that reaches itself resets the clock, because a
changed file identity that still leads back is not orphaned. Two consecutive
failed probes, 60 seconds apart, trigger a graceful shutdown. That shutdown closes
listeners, drops clients, and stops child process groups unless `-keep-children`
is set. It is never a bare exit. The 60-second interval and the 10-minute grace
are claustrum's own values, not probe-measured.

The behavior is identical on every OS, because `os.SameFile` gives the
file-identity compare portably. It is a no-op when the daemon has no captured
socket identity or no `instanceId`.

## Message shapes

```jsonc
// request
{"jsonrpc":"2.0","id":<n>,"method":"<ns>.<method>","params":{…},"auth":"<token>"}
// success
{"jsonrpc":"2.0","id":<n>,"result":{…}}
// error
{"jsonrpc":"2.0","id":<n>,"error":{"code":<c>,"message":"…"}}
// id-less stream notification (server -> client)
{"type":"stream","processId":"<id>","stream":"stdout|stderr|exit","seq":<n>,"data":"<base64>","exitCode":<n>,"signal":"<SIG>","killedBy":"<who>"}
```

The reply's `id` is the request's id decoded and re-encoded. It is not the bytes
the client sent. The daemon accepts any JSON value and returns it canonicalized. A
number round-trips through a float64
(`1.0` → `1`, `1e2` → `100`, `12345678901234567890` → `12345678901234567000`). An
object comes back with its keys sorted (`{"b":1,"a":2}` → `{"a":2,"b":1}`).
Integers, strings, arrays and `null` are unchanged. A client that matches replies
by the id *text* must compare the decoded value instead.

Results are ordered structs, never maps. The field order below is the wire
contract.

### Error codes

| code | meaning |
|---|---|
| `-32700` | parse error, a malformed JSON line (response `id` is `null`) |
| `-32600` | `Invalid JSON-RPC version`, meaning `jsonrpc` is absent or not `"2.0"` |
| `-32601` | `Invalid method format: <m>` (method has no `.`), `Unknown namespace: <ns>` (well-formed but unknown namespace), or `Unknown method: <ns>.<m>` (known namespace, unknown method) |
| `-32602` | invalid params (see per-method messages) |
| `-32603` | internal error, for example `open <path>: no such file or directory`. A recovered handler panic also gives this code, with `recovered panic: <v>` |
| `-32003` | `stdin offset gap: offset ahead of applied bytes`, from `process.stdin` with an `offset` past the applied high-water (added in `7c2f88d`) |
| `-32002` | `stdin backpressure: queue full`, from `process.stdin` when the per-process async stdin queue is already full (16 MiB). The write is rejected, not blocked. |
| `-32001` | `Unauthorized: invalid or missing auth token` |

### Error-string catalogue

Every method-level error string, verbatim, in one place. The per-method sections
below give the trigger and the result shape. Codes are `-32602` unless noted.

| namespace / context | error string | code / notes |
|---|---|---|
| protocol | `Invalid JSON-RPC version` | -32600 |
| protocol | `Invalid method format: <m>` / `Unknown namespace: <ns>` / `Unknown method: <ns>.<m>` | -32601 |
| protocol | `Invalid params` | -32602 (absent/mistyped `params`) |
| protocol | `Unauthorized: invalid or missing auth token` | -32001 |
| protocol | `recovered panic: <v>` | -32603 (claustrum-only, see below) |
| files.stat / files.read | `stat <path>: <reason>` | -32603 (any stat failure other than ENOENT) |
| files.read | `files.read: path is a directory` | |
| files.read | `files.read: file exceeds maxBytes` | |
| files.read | `files.read: not a regular file` | D4 opt-in only |
| files.list | `open …: no such file or directory` | -32603 (missing dir) |
| files.list | `open <p>: not a directory` | -32603. The path is not a directory. Since `7d193f89` the open itself fails. Before `7d193f89` the message was `readdirent …` |
| files.list | `open <p>: permission denied` | -32603 (an unreadable directory) |
| files.validate | `Path does not exist` | in `error` field, `valid:false` |
| files.extract_tar | `archivePath and destDir are required` | |
| files.extract_tar | `destDir must be an absolute, non-root path: …` | in `error` field |
| files.extract_tar | `destDir must not be or contain the home directory: …` | D2, in `error` field |
| files.extract_tar | `gzip: …` | in `error` field (bad gzip) |
| files.extract_tar | `unsafe path in archive: <entry>` | in `error` field (zip slip) |
| files.extract_tar | `unsupported tar entry type <c>: <entry>` | in `error` field |
| files.extract_tar | `extraction size limit exceeded` | D3 opt-in, in `error` field |
| files.extract_tar | `clean destDir: …` / `mkdir destDir: …` / `write .synced: …` | in `error` field |
| files.extract_tar | `create <entry>: open <target>: is a directory` | in `error` field |
| files.extract_tar | `mkdir parent <entry>: <os error>` | in `error` field (prefix is contract) |
| git.status | `baseRepo is required` | -32602. `baseRepo` is now required, since `7d193f89` |
| git.status / git.list_branches | `<go error>`, for example `exit status 128` | -32603 (git failed, stdout parse) |
| git.status / git.list_branches | `signal: killed` | -32603, D5 opt-in only |
| git.worktree_create | `branchName is required` | |
| git.worktree_create | `not a git repository` | in `error`, `errorCode:"not_a_repo"` |
| git.worktree_create | `refusing to create worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; … / already exists, …}` | in `error`, `errorCode:"unsafe_path"` (`7d193f89` containment). The spelling refusal is Windows-only and comes before containment |
| git.worktree_create | `refusing to create worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, `errorCode:"symlinked_component"`, for a symlinked ancestor component under the repo (`7d193f89`) |
| git.worktree_create | `failed to create parent directory: "" does not name a directory` | in `error`, `errorCode:"mkdir_failed"` (empty `worktreePath`) |
| git.worktree_create | `git worktree add failed: <combined output>` | in `error`, `errorCode:"worktree_add_failed"`. The message is git's combined output on one line, with internal newlines joined by a space, capped at 512 bytes. The pre-created leaf directory is rolled back on failure. A pre-existing branch is not deleted (`4534d86`) |
| git.worktree_create | `git worktree add timed out after <n>ms (deadline expired {before the checkout started / during the checkout): <git error> / after the checkout finished})` | in `error`, `errorCode:"timeout"`, from the caller-supplied `timeoutMs` (`4534d86`). An absent `timeoutMs`, or 0, arms no deadline |
| git.worktree_remove | `refusing to remove worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; …}` | in `error`, with no `errorCode`. This is `7d193f89` containment. The spelling refusal is Windows-only and comes before containment |
| git.worktree_remove | `refusing to remove worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, with no `errorCode`. It gates the delete fallback off a planted link (`7d193f89`) |
| git.worktree_remove | `refusing to remove worktree: <p> is locked (git worktree lock); unlock it to remove it` | in `error`, with no `errorCode`. `7d193f89` refuses a LOCKED worktree (`success:false`) and leaves it in place. The message is fixed whatever the lock reason is. Before `7d193f89` the reference deleted it through the fallback and answered `success:true`. |
| git.worktree_remove | `failed to remove worktree: "" does not name a directory` | in `error` (empty `worktreePath`) |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (its registrations could not be examined); retry` | in `error`, with no `errorCode`. The configuration of `baseRepo` cannot be read, or, without `worktreeRoot`, `baseRepo` holds no repository. Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: statat .claude: permission denied` / `failed to remove worktree: open <baseRepo>: <error>` | in `error`, with no `errorCode`, without `worktreeRoot` only. `baseRepo` cannot be searched (mode 0600), or cannot be opened (mode 0000, or a regular file). Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: <git output>; manual cleanup also failed: <err>` | in `error` (only if manual cleanup also fails) |
| git.worktree_remove | `worktreePath must not be or contain the home directory: …` | D2, in `error`. It sits behind `7d193f89` containment on the default branch, where it fires only if a repo is an ancestor of home. It is the active home guard on the `external_root` branch |
| git.worktree_remove | `git worktree remove timed out after <dur>; no cleanup was attempted, and git may have partially removed the worktree` | D5 opt-in, in `error` |
| process.spawn | `Process ID is required` / `Command is required` | |
| process.stdin | `Invalid base64 data` / `Process not found` / `Process not running` | order: decode → not-found → offset verdict (`-32003`/duplicate) → not-running (fresh write only) |
| process.stdin | `stdin offset gap: offset ahead of applied bytes` | -32003 |
| process.stdin | `stdin backpressure: queue full` | -32002 (queue full, ~16 MiB) |
| process.killAndWait / process.reattach | `Process ID is required` / `Invalid params` | |

`-install` reports failures inside the `__INSTALL_RESULT__` facts line as
`cliError` strings, not through an exit code. They are catalogued in the
`-install` section below.

### Handler panic recovery

The per-request goroutine wraps dispatch in `recover()`. It therefore catches a
panic in any handler, and the daemon does not crash. The reply is
`{"error":{"code":-32603,"message":"recovered panic: <v>"}}`, and the daemon logs
`[Server] recovered panic: method=<m> id=<id>: <v>`. One method is the exception.
A recovered `server.shutdown` panic writes no frame at all. Its only reply is
`{"ok":true}`.
See `server.*` below.

This frame is claustrum's own. It is not a statement about the wire. No input is
known to reach a handler panic. Extensive fuzzing found none, and each of
claustrum's own panic sites is an unreachable stdlib guard or an
already-bounds-guarded slice. `-32603` is the JSON-RPC 2.0 *Internal error* code.
The message prefix, log line, and id rendering are claustrum's own conventions.
They are documented so that an operator who sees the frame knows what it means.
They are not a compatibility guarantee.

### Validation precedence

The daemon tests a request in the order parse, then auth, then version, then
method, then params:

- The daemon tests auth *before* the `jsonrpc` version. A request can fail both
  tests, with no `auth` and with a missing or wrong `jsonrpc`. That request
  reports `-32001 Unauthorized`, not the version error.
- **`server.shutdown` is the exception.** The daemon skips auth for it entirely. A
  shutdown frame that is missing both `auth` and `jsonrpc` therefore gets
  `-32600 Invalid JSON-RPC version`, because the version gate still applies, and
  the daemon stays up.

### Params presence and typing

Every `files.*` / `git.*` / `process.*` method requires a `params` object.
`server.*` methods take no `params`. The daemon ignores a mistyped `params` on a
`server.*` method, and the call succeeds.

- Absent `params` gives `-32602 Invalid params`. The daemon tests this *after*
  method existence, so an unknown method is `-32601` in every case.
- The daemon accepts an empty `{}` and then runs the method's own validation.
- Mistyped `params` gives `-32602 Invalid params`. This covers a wrong field type
  (`"maxBytes":"4"`, `"path":123`) and a non-object value (`"params":"x"` or
  `[…]`). The daemon does not coerce the value, and it does not ignore the decode
  error.
- The daemon ignores unknown extra fields. It diverges from the reference in *how
  strictly* it does so (D9). claustrum binds `params` into one struct per
  namespace (`pathParams`, `gitParams`). A field that is valid for the *namespace*
  but unused by *this* method therefore still takes part in the decode. A
  type-mismatched value there gives `-32602`, for example
  `files.stat {"maxBytes":"{"}` and `git.status {"baseRepo":[1,2]}`. The reference
  answers both requests with defaults, as measured. Both daemons ignore a
  genuinely unknown key, which is a key in neither struct. This is accepted
  divergence D9. See [`DIVERGENCES.md`](DIVERGENCES.md).

## Path handling

### A path must be valid UTF-8 to be addressable at all

Before any expansion or method logic, the JSON decoder replaces bytes that are not
valid UTF-8 with `U+FFFD`. A file whose name contains such bytes therefore cannot
be named in any request. The daemon answers about a path that does not exist. It
returns `exists:false`, or a `chdir` or `stat` error that quotes the substituted
name. This is parity, not a divergence. Both daemons inherit it from the JSON
decoder. See
[ARCHITECTURE.md → Inherited wire bytes](ARCHITECTURE.md#inherited-wire-bytes).

### Tilde expansion in path params

claustrum expands a leading tilde in every path-bearing param before the method
runs: `files.*` `path`, `extract_tar`'s `archivePath` / `destDir`, `git.*` `path` /
`baseRepo` / `worktreePath`, and `process.spawn`'s `cwd`. Branch names are refs,
not paths, so claustrum never expands them. claustrum replaces a leading `~` with
the daemon user's home directory, and then cleans the remainder lexically. Bare
`~` is the exception. It returns home verbatim, uncleaned.

| sent | reference replies | absolute-form control |
|------|-------------------|-----------------------|
| `~` | `<home>`, verbatim, not cleaned | n/a |
| `~/` | `<home>` (trailing separator stripped) | unchanged |
| `~/f.txt` | `<home>/f.txt` | unchanged |
| `~//f.txt` | `<home>/f.txt` (doubled separator collapsed) | `<home>//f.txt` |
| `~/a/./b` | `<home>/a/b` (`.` resolved) | `<home>/a/./b` |
| `~/a/x/../b` | `<home>/a/b` (`..` resolved lexically) | `<home>/a/x/../b` |
| `~user/f`, `/tmp/~/f`, `$HOME/f` | unchanged, not expanded | n/a |

Two consequences:

- Bare `~` is the exception. A `HOME` of `/home/me/` echoes back with its trailing
  slash. `~/` under the same `HOME` does not.
- The cleaning is lexical, and it applies to the tilde form only. With
  `~/link -> b/c`, the reference reads `<home>/x.txt` for `~/link/../x.txt`. The
  absolute spelling walks the symlink and reads `<home>/b/x.txt`. Same request,
  different file.

Windows behaves the same way in Windows separator terms. Home comes from
`USERPROFILE` there, not from `HOME`.

| sent | reference replies |
|------|-------------------|
| `~\a7` | `~\a7`, not expanded, because `~\` is not a tilde form |
| `~/a1` | `<home>\a1`, with `/` rewritten to `\` |
| `~/a4\x\..\w` | `<home>\a4\w`, because `\` is a separator for `..` |
| `~/a5/` | `<home>\a5` |
| `~//a6` | `<home>\a6` |
| `~` | `<home>` verbatim. A home of `C:\h\` keeps its trailing `\` |

The expanded spelling is wire-visible on eight frames, so it is contract.
`git.worktree_create` reflects `worktreePath` into `result.path` and into git's
error text. The expanded string also appears in the error text of `files.stat`,
`files.read`, `files.list`, `files.validate`, `files.extract_tar` and
`process.spawn`. Two places do *not* carry the spelling. `files.list` entry paths
are re-joined, and `git.info`'s `root` comes from git's own output. On a
trailing-separator spelling the difference is a change of *verdict*. POSIX
`stat("f.txt/")` gives `ENOTDIR`. Once the daemon removes the separator, a
`-32603` error frame becomes a success frame. Ids 15, 16, 18 and 19 of
`testdata/socket_tilde_expansion.golden.json` pin this. Id 17 sends `~//` to
`files.list` and is documentary only.

### Stat failures other than "does not exist"

`files.stat`, `files.read` and `files.validate` distinguish a path that is absent
from a path the daemon cannot examine:

- A genuine `ENOENT` is the "does not exist" answer in each method's own shape.
  The three shapes are `exists:false`, `content:"" exists:false`, and
  `valid:false` with `error:"Path does not exist"`.
- The daemon reports any other stat failure with the underlying message.
  `files.stat` and `files.read` return `-32603 stat <path>: <reason>`.
  `files.validate` keeps its result shape and puts that text in its `error` field
  instead. Reachable reasons include `not a directory` (a path component is a
  regular file), `file name too long`, and `invalid argument` (a NUL byte in the
  path).

## Methods (19)

`server.capabilities` self-describes the set. Order as returned:

```
server.ping  server.capabilities  server.shutdown
files.list   files.validate  files.stat  files.read  files.extract_tar
git.info     git.status      git.list_branches  git.worktree_create  git.worktree_remove
process.spawn  process.stdin  process.kill  process.killAndWait  process.reattach
plugins.prune
```

Reference `7c2f88d` added `process.killAndWait` between `process.kill` and
`process.reattach`, for 19 methods. `7d193f89` then removed `server.version` and
brought the set to 18. Calling `server.version` now answers
`-32601 "Unknown method: server.version"`. The `-version` CLI flag is a separate
surface and still prints the daemon's version. `19f30c46` appended
`plugins.prune` last and brought the set back to 19. `plugins.prune` is the only
member of the `plugins.*` namespace.

### server.*

| method | params | result |
|---|---|---|
| `server.ping` | none | `{"pong":true}` |
| `server.capabilities` | none | `{"version":"<id>","methods":[…19…],"instanceId":"<32-hex>","startedAt":<unix-ms>,"features":["process.stdin.offset","git.status.baseRepo","git.worktree_create.timeoutMs","git.worktree_create.existingBranch","process.spawn.shellAgentSocket","git.worktree.external_root","server.instance_id"]}`. `plugins.prune` is the 19th method, appended last on every OS. `git.worktree.external_root` is omitted on Windows. `git.worktree_create.timeoutMs`, `git.worktree_create.existingBranch`, `process.spawn.shellAgentSocket`, `instanceId` and `startedAt` are present on every OS |
| `server.shutdown` | none | `{"ok":true}`, when the reply gets out. The handler waits until the teardown starts to close connections, and then returns the reply. The frame arrives only when its write wins the race with the close. See below |

- `server.version` was removed in `7d193f89`. It now answers
  `-32601 "Unknown method: server.version"` like any other unknown method.
- `instanceId` and `startedAt` were added by `4534d86`. They sit between `methods`
  and `features`. `instanceId` is a 32-hex string, and `startedAt` is the daemon's
  boot time in unix milliseconds. Both are present on every OS. claustrum
  generates `instanceId` from 16 crypto/rand bytes at startup and stamps
  `startedAt` then. It echoes both on the capabilities reply for parity.
- The `features` array was added by `7c2f88d`. It follows `instanceId` and
  `startedAt`, and it advertises optional extensions. `process.stdin.offset`, the
  resumable and idempotent stdin contract, landed first. `7d193f89` added
  `git.status.baseRepo` and `git.worktree.external_root`. `4534d86` inserted
  `git.worktree_create.timeoutMs` before `git.worktree.external_root` and appended
  `server.instance_id`, which is always last on every OS. `19f30c46` inserted
  `git.worktree_create.existingBranch` after `git.worktree_create.timeoutMs`,
  because `worktree_create` can attach an already-existing branch. On unix
  `git.worktree.external_root` is present. On Windows it is omitted. The reference
  gates the external-worktree capability off on Windows, measured against
  `7d193f89`, and drops the feature from its Windows capabilities frame, so
  claustrum matches. `f6010b97` inserted `process.spawn.shellAgentSocket` after
  `git.worktree_create.existingBranch`, because `process.spawn` can hand a child
  the login shell's SSH agent socket. `git.worktree_create.timeoutMs`,
  `git.worktree_create.existingBranch` and `process.spawn.shellAgentSocket` are
  present on every OS.
- `server.shutdown` is not authenticated. See [Authentication](#authentication).
- On the reference, `server.shutdown` usually closes the connection with no
  reply. Its `{"ok":true}` frame arrived in 24 of 800 single-connection runs
  with an earlier ping. It arrived in 0 of 200 runs with no earlier request on
  the connection. With a second idle connection open it arrived in 67 of 200 runs.
  These counts are measured against `f6010b97` and `90fca6e6` on a Linux VM. In
  claustrum, the handler signals the teardown and waits until the teardown starts
  to close connections. Then it returns the reply for writing, so the reply write
  races the close. The teardown closes the listener first, then every connection,
  before its other cleanup.
  When the frame arrives, it is `{"jsonrpc":"2.0","id":<id>,"result":{"ok":true}}`
  followed by EOF. An idle connection gets a clean EOF with no bytes. macOS is not
  measured yet.
- Log order on `server.shutdown`. The daemon logs
  `[ServerHandler] server.shutdown received over RPC`, then
  `[Server] shutdown requested`, before the teardown closes any connection.
  Each connection's `[Server] Connection closed: <addr>` line, when it is
  logged, comes after its close. Measured with `strace` against `f6010b97` and `90fca6e6` on a Linux
  VM, the reference wrote the two lines before its connection close in 36 of
  36 traced runs. Its `Connection closed` line came after the close in 32 of
  them. Each of the other 4 runs also wrote the shutdown reply.
  The reference then logs
  `[Server] cleanup: closed <n> connection(s), killed <n> child process group(s)`.
  claustrum does not log that line. This is a measured log difference. On a Linux
  VM with one connection, the `f6010b97` log ended with that line in 50 of 50
  runs. The claustrum log had no `cleanup:` line in 50 of 50 runs.
- Reply delivery is a measured timing difference, not parity. The claustrum
  reply arrives at a different rate. Each count below is from 50 runs with a ping before the shutdown. The
  runs were measured in one session against `f6010b97`, on one Linux VM and one
  Windows VM. On one connection, the claustrum reply arrived in 0 of 50 runs on
  Linux and in 0 of 50 on Windows. The reference reply arrived in 5 of 50 on Linux
  and in 21 of 50 on Windows. With a second idle connection open, the claustrum
  reply arrived in 2 of 50 runs on Linux, where the reference reply arrived in 20
  of 50. On Windows the counts were 39 of 50 for claustrum and 36 of 50 for the
  reference. All of these rates depend on timing.

### files.* (param: `path`)

#### files.stat
`{path}` → `{"exists","isDir","size","mode":"-rw-r--r--"}`
- Missing path → `{exists:false,isDir:false,size:0,mode:""}`.

#### files.list
`{path}` → `{"entries":[{"name","path","isDir"},…]}` (name-sorted)
- The daemon omits hidden entries. It skips any name that begins with `.`, such as
  `.git` and `.env`. This matches the reference.
- The daemon resolves `isDir` with `Stat`, so it follows symlinks. A symlink to a
  directory is `isDir:true`, and a dangling symlink is `isDir:false`.
- Missing dir → `-32603 open …: no such file or directory`.

#### files.read
`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`
- `content` is raw text, not base64.
- Missing file → `{content:"",exists:false}`, which is not an error.
- A directory → `-32602 files.read: path is a directory`.
- Size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`.
- An absent, `0`, or negative `maxBytes` sets the cap to `262144` (256 KiB). The
  cap is not "unlimited". A file of 262144 bytes reads, and a file of 262145 bytes
  errors. The daemon honors a positive `maxBytes` verbatim, above or below the
  default. The cap uses the stat size. On linux that size is `0` for every
  non-regular kind, so the cap never bounds a FIFO, socket or device on either
  binary.
- Non-regular files have an opt-in guard, D4. It is off by default, which is
  parity. The reference reads `/dev/null` as `{"content":"","exists":true}` and
  blocks on a writerless FIFO, and it refuses neither. Set
  `-files-read-regular-only`, or the `files-read-regular-only` configuration key.
  Every non-regular path then answers `-32602 files.read: not a regular file`,
  which is a frame the reference never produces. The predicate is
  `Mode().IsRegular()`. It is whole and not narrowable, because `/dev/null` and
  `/dev/zero` are indistinguishable by mode. The full measurement and the reason
  are in [`DIVERGENCES.md`](DIVERGENCES.md) → D4.

  | path | reference and claustrum at the default | with `-files-read-regular-only` |
  |---|---|---|
  | **CONTROL** a regular file | `{"content":"…","exists":true}` | *(unchanged, the guard does not apply)* |
  | **CONTROL** a regular file over `maxBytes` | `-32602 files.read: file exceeds maxBytes` | *(unchanged)* |
  | a FIFO, writer paired | `{"content":"<bytes written>","exists":true}` | `-32602 files.read: not a regular file` |
  | a FIFO, no writer | no frame until a writer opens | `-32602 files.read: not a regular file` |
  | `/dev/null` | `{"content":"","exists":true}` | `-32602 files.read: not a regular file` |
  | a bound `AF_UNIX` socket | `-32603 open <p>: no such device or address` on linux. *darwin/amd64 says `operation not supported on socket`, a per-OS stdlib difference that is identical between binaries on each OS* | `-32602 files.read: not a regular file` |
  | an unreadable character device (`/dev/console`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |
  | an unreadable block device (`/dev/nvme0n1`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |

  The two device rows assume a non-root daemon, because they are permission
  failures. The opted-in column is measured for the FIFO and `/dev/null` rows.
  For the socket row and the two device rows it is entailed by a false
  `Mode().IsRegular()`, and was not run separately.
  The default gives up two things. A writerless FIFO parks a request goroutine and
  a descriptor until a writer arrives. An unbounded device read (`/dev/zero`) grows
  the daemon until the kernel OOM-kills it. Both are the reference's own behaviour,
  and both are measured. The forensics are condensed out of the committed docs.

#### files.validate
`{path}` → `{"valid":bool,"isDir":bool[,"error"]}`
- Missing path → `{valid:false,isDir:false,error:"Path does not exist"}`.

#### files.extract_tar
`{archivePath,destDir}` → extracts a gzip tar → `{"success":true,"fileCount":<n>}`

The method has four deliberate side effects. None of them is visible in the frame:
1. The daemon wipes `destDir` with `os.RemoveAll` and then recreates it before it
   unpacks. Extraction is idempotent and destructive.
2. Entries get owner-only fixed modes: `0600` for files, `0700` for directories.
   An executable `0755` entry still lands `0600`.
3. On success the daemon writes an empty `.synced` marker at the `destDir` root.
   It does not count that marker in `fileCount`.
4. The daemon consumes `archivePath`. Once it opens the archive, it removes the
   file on *every* outcome: success, bad gzip, or unsafe path.

Errors. Unless a line says otherwise, each error goes in the `error` field with
`fileCount:0`, which has no `omitempty`:
- Missing params → `-32602 archivePath and destDir are required`.
- A `destDir` that is not absolute, or that is a root →
  `destDir must be an absolute, non-root path: …`. The daemon rejects this before
  it opens the archive, so it does not consume the archive. "Root" is the
  platform's own notion. It is `/` on Unix, and a drive root `C:\` or a UNC share
  root `\\server\share\` on Windows. The root test and the `filepath.IsAbs` test
  share one branch and one message. Whether the reference refuses a root `destDir`
  at all is not measured. Our own consequence justifies the guard: a recursive
  delete of the volume. It is not a claim about the reference, so it is neither
  parity nor a divergence entry.
- A `destDir` that is, or contains, the home directory →
  `destDir must not be or contain the home directory: …`. This is intentional
  divergence D2, because the reference wipes `$HOME` on `"destDir":"~"`. The test
  is *containment*. The daemon refuses home and any ancestor of home. It accepts
  anything under home, such as `~/.claude/…`. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- Bad gzip → `gzip: …`.
- Zip slip → `unsafe path in archive: <entry>`. The daemon allows a `../` that
  resolves back inside `destDir`.
- An entry that is neither regular nor a directory, such as a symlink, a hardlink,
  a device or a fifo → `unsupported tar entry type <c>: <entry>`. `<c>` is the tar
  typeflag char, `2` for a symlink and `1` for a hardlink.
- Total uncompressed bytes over the opt-in cap → `extraction size limit exceeded`.
  This is not reachable by default, because the cap is `0`, which means off, and
  that matches the reference. This is intentional divergence D3. See the flags
  table under `-serve` and [`DIVERGENCES.md`](DIVERGENCES.md) → D3.
- A failure to clean, to mkdir, or to write the marker → `clean destDir: …` /
  `mkdir destDir: …` / `write .synced: …`.
- Target is an existing directory → `create <entry>: open <target>: is a
  directory`.
- The daemon cannot create the parent → `mkdir parent <entry>: <os error>`. One
  way to reach this is an earlier entry that wrote a file where this entry needs a
  directory. Only the `mkdir parent <entry>: ` prefix is contract. The tail is the
  OS's. Both `create <entry>: ` and `mkdir parent <entry>: ` name the archive
  entry, not the resolved target.

### git.* (param: `path` = repo dir; worktree ops use `baseRepo`)

#### git.info
`{path}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""}`

- The daemon reads `branch` with `symbolic-ref`, so it works on an unborn HEAD. An
  empty repo gives the init branch name, for example `master`. A detached HEAD
  gives `branch:"detached:<short-sha>"`.
- `root` is the absolute repo top-level (`git rev-parse --show-toplevel`). It stays
  the same even when `path` is a subdirectory (added by reference `7cbfa471`).
- `7c2f88d` added `repoSlug` and `defaultBranch`. Both are always present,
  including on the non-repo body. Each is an empty string when the daemon cannot
  determine it.
- `repoSlug` is `owner/repo` from `remote.origin.url`. The daemon populates it only
  for a canonical `github.com` remote. These rules are measured across 42 URL
  shapes:
    - The scheme must be `https`, `http`, `ssh`, `git`, or absent. An absent scheme
      is the scp-like `[user@]host:owner/repo`. `git+ssh://` and `file://` give
      `""`.
    - The host must equal `github.com` case-insensitively. `www.github.com`, the
      trailing-dot `github.com.`, a port (`github.com:443`), GitLab, Bitbucket and
      self-hosted GHE all give `""`. The daemon strips userinfo.
    - The path must be exactly two non-empty segments after one optional trailing
      `/` and one optional `.git`.
    - The owner is alphanumerics with *interior* hyphens only. `ac-me` and `ac--me`
      pass. `-acme`, `acme-`, `acme_corp` and `acme.co` do not.
    - The repo is alphanumerics plus `.`, `_`, `-`. It must not start with `-`, it
      must not be `.` or `..`, and it must not end in a lowercase `.wiki`. That
      last test is case-sensitive and suffix-only, so `GIZMO.WIKI` and a repo named
      `wiki` are accepted.
- `defaultBranch` is what `refs/remotes/origin/HEAD` points to. It is empty when
  `refs/remotes/origin/HEAD` is unset.

#### git.status
`{path,baseRepo}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}`

- `7d193f89` rebuilt this method around session worktrees. `baseRepo` is now
  required. An absent one answers `-32602 baseRepo is required`. Status is
  reported only when `path` is a linked worktree whose main repository is
  `baseRepo`. Everything else answers the bare `{"isRepo":false,"clean":false}`,
  which is the full shape, unlike `git.info`. Everything else means a plain path,
  a plain subdirectory, a nested repository, or the repository root itself. It also
  means a worktree of a *different* repository, and the right worktree named
  against the wrong `baseRepo`.
- `changes` is the stdout of
  `git status --porcelain --untracked-files=all --ignore-submodules=all`, and
  nothing else. Stderr warnings never appear. Lines are verbatim minus the line
  ending. The two-character XY column is positional, so the leading space of an
  unstaged-only change is data. `"M  a.txt"` is staged and `" M b.txt"` is
  unstaged. `--untracked-files=all` is wire-visible. An untracked file inside an
  untracked directory is listed individually (`"?? sub/u.txt"`), not as the
  directory (`"?? sub/"`).
- The invocation prepends `--attr-source=<empty-tree>`, git's canonical empty tree,
  so the repository's in-repo `.gitattributes` is ignored. This matches `4534d86`.
  It is wire-visible on a repo with a `.gitattributes` filter. Without it a clean
  filter runs during status, and it can flip a file between clean and dirty and
  execute the filter's command. It is a no-op on a repo with no attribute rules.
  It is omitted when the runtime git predates `--attr-source` (git 2.40).
- The status runs in an isolated temp gitdir, matching `4534d86`. That gitdir is a
  fresh `GIT_DIR` holding the worktree's own `HEAD` and `index`, with
  `GIT_COMMON_DIR` at the shared repo and `--work-tree` at the worktree, under
  `GIT_OPTIONAL_LOCKS=0`. It therefore does not refresh or rewrite the caller's
  index, and it does not take `index.lock`. On Linux and macOS this is
  byte-identical to the reference and to a direct status.
- Windows divergence D16. On Windows the reference's own `git.status` of a linked
  worktree errors `-32603 "exit status 128"`, as measured, while claustrum returns
  the status. The reference's Windows failure mechanism is not yet pinned. An
  earlier hardcoded-`/tmp` hypothesis is contradicted, because the reference
  respects `$TMPDIR`. This is a reachable, always-on Windows divergence, and
  claustrum is more correct. See [DIVERGENCES.md](DIVERGENCES.md) D16.
- Every line is verbatim, the first one included. The daemon splits on the trailing
  newline only, so entry 0 keeps its leading space. `[" M a1"," M a2"]` returns
  `[" M a1"," M a2"]`. 5db5e4a lost entry 0's leading space. 7d193f89 and 4534d86 do not.
- A failing git → `-32603` that carries the Go error string (`exit status 128`, not
  git's `fatal:` text). With opt-in D5 the same `-32603` can carry `signal: killed`.

#### git.list_branches
`{path}` → `{"isRepo":true,"branches":[…sorted…]}`
- Non-repo → `{"isRepo":false,"branches":[]}`.
- The daemon reads stdout only. A broken-ref `for-each-ref` warning must not become
  a branch.
- A failing `for-each-ref` → `-32603 exit status 128`. With opt-in D5 it can carry
  `signal: killed` (see D5 below).

#### git.worktree_create
`{baseRepo,branchName,worktreePath[,sourceBranch][,existingBranch][,worktreeRoot][,timeoutMs]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}`
- The repo is `baseRepo`, not `path`. When `baseRepo` is absent, the daemon uses
  its cwd repo.
- Missing `branchName` → `-32602 branchName is required`. It is required even when
  `existingBranch` is given.
- `branch` was added by `19f30c46`. It is the branch the worktree checks out. It
  follows `sourceBranch` on the wire and is present on every success. It holds the
  created `branchName`, or the attached `existingBranch`. It is absent on failure.
- `existingBranch` was added by `19f30c46`, as the
  `git.worktree_create.existingBranch` capability. It attaches the worktree to an
  already-existing local branch instead of creating one. When
  `show-ref --verify refs/heads/<existingBranch>` resolves, the add uses that
  branch as the commit-ish (`worktree add --no-checkout <path> <existingBranch>`,
  with no `-b`), and `branch` is `<existingBranch>`. When `existingBranch` is empty
  or names no branch, the `-b <branchName>` new-branch path runs and `branch` is
  `<branchName>`. A miss falls back silently rather than erroring. This is measured
  against `19f30c46`.
- The resolved repo is not git → `{success:false,error:"not a git
  repository",errorCode:"not_a_repo"}`. The daemon tests this before the add.
- By default, with no `worktreeRoot`, `7d193f89` confines the worktree to inside
  the repository. After the repo
  test, `worktreePath` must be absolute, carry no `..` component, sit strictly
  under `baseRepo`, and not already exist. Each failure is
  `{success:false,error:"refusing to create worktree: …",errorCode:"unsafe_path"}`:
  `"<p> is a relative path; …"`, `"<p> contains a \"..\" component; …"`, `"<p> has a
  component Windows reads as a different name (trailing dot or space, or a colon); …"`
  (Windows only, before the containment check), `"<p> is not
  inside the repository <repo>; session worktrees are only created and removed under
  <repository>/.claude/worktrees"`, and `"<p> already exists, and a new worktree is
  only ever created in a fresh directory"`. The recommended location is
  `<repo>/.claude/worktrees/<id>`, but the enforced rule is only containment in the
  repo. An empty `worktreePath` is `{success:false,error:"failed to create parent
  directory: \"\" does not name a directory",errorCode:"mkdir_failed"}`. The daemon
  creates the parent directory before the add, so a nested path succeeds on a fresh
  repo.
- `worktreeRoot` is the `external_root` capability. When the client supplies
  `worktreeRoot`, the worktree is placed OUTSIDE the repository, at
  `<worktreeRoot>/<directory>/<name>`, exactly two levels under the root. On
  Windows this capability is gated off. Any `worktreeRoot` is refused before any
  location test with `"refusing to {create,remove} worktree: <root> cannot be used:
  a custom worktree location is not supported on Windows hosts yet"`. The
  `errorCode` is `"unsafe_path"` on create, and there is none on remove. On unix
  the in-repo containment above is replaced by these tests, each with
  `errorCode:"unsafe_path"`. `worktreeRoot` and `worktreePath` must be absolute and
  `..`-free, and `worktreePath` must sit exactly two levels under the root
  (`"<p> is not <worktree location>/<directory>/<name> beneath <root>"`). The root
  must be owned by the daemon's user
  (`"<root> is owned by uid <o>, not by you (uid <u>); …"`). The root must not be
  writable by its group or by every user on the host
  (`"<root> is writable by <who> (mode <perm>); … chmod go-w"`). The `<directory>`
  level must not be a symlink. Unless it is already marked, it must also start out
  empty (`"<dir> already exists, is not marked as a worktree directory, and holds
  other files (for example \"<name>\"); … must start out empty …"`). On success the
  daemon writes a 285-byte `.claude-managed-worktrees` marker at the `<directory>`
  level. Independently, a `baseRepo` that itself sits under a managed-worktrees
  marker is refused `{success:false,error:"baseRepo is inside a managed worktrees
  directory …",errorCode:"nested_base_repo"}`.
- Other failure → `{success:false,error:"git worktree add failed: …",errorCode:"worktree_add_failed"}`.
  The tail is git's combined output, because the add writes its fatal to stderr
  and leaves stdout empty. Since `4534d86` it is reported on a single line, with
  git's stderr lines joined by a space and capped at 512 bytes. Since `4534d86` the
  pre-created leaf directory is also rolled back. One example is
  `"git worktree add failed: Preparing
  worktree (new branch 'dup') fatal: a branch named 'dup' already exists"`.
- `timeoutMs` is caller-supplied and was added by `4534d86`. It bounds the add and
  the checkout with a per-request deadline in milliseconds. An absent `timeoutMs`,
  or `0`, arms no deadline, so the reply is byte-identical to the default. A fired
  deadline answers
  `{success:false,error:"git worktree add timed out after <n>ms (…)",errorCode:"timeout"}`.
  The parenthetical takes three forms. It is
  `deadline expired before the checkout started` when the add is killed, as
  measured. It is `deadline expired during the checkout): <git error>` when the
  checkout, a `read-tree`, is killed, as measured. The tail there is git's own
  error, for example `signal: killed`. It is
  `deadline expired after the checkout finished` in one further case. The checkout
  git exits 0, and then a descendant it left, such as a smudge or hook filter,
  holds the daemon's combined output pipe past a fixed ~5s post-exit drain cap.
  That cap is measured against `4534d86` and is independent of `timeoutMs`. At that
  cap the daemon reaps the descendant, then gates the reply on `timeoutMs`. When
  `timeoutMs` exceeds the drain, the worktree is kept and the reply is
  `{success:true}`. When it does not, the worktree is rolled back, with the branch
  deleted and the worktree removed. The reply is then the `timeout` frame carrying
  this parenthetical. The same string
  also covers the near-unhittable window where the deadline expires just after a
  checkout that left no lingering descendant. This is caller-activated. It is
  distinct from the operator-global `-git-timeout` divergence (D5), and it applies
  to create only, not to `git.worktree_remove`.
- `sourceBranch` omitted → the source defaults to the repo's current branch, and
  the daemon echoes it back. On an unborn HEAD the source resolves empty, the add
  infers an orphan branch and succeeds, and the result omits `sourceBranch`.

Worktree population works as follows. `git worktree add` checks out tracked files
only, so the daemon then seeds the new worktree. The copies are best-effort, and a
failure never fails the request:
- `.worktreeinclude` sits at the repo root and uses `.gitignore` syntax. It is an
  include filter over the git-ignored set. The daemon copies an untracked file only
  when the manifest names it and git's standard rules ignore it. That is the
  intersection of `git ls-files --others --ignored --exclude-from=.worktreeinclude`
  and `git ls-files --others --ignored --exclude-standard`. A manifest match that
  git does not ignore is not copied. Without the manifest the daemon copies no
  untracked manifest file.
- `.claude/` is copied separately, with no manifest entry. A second pass runs
  `git ls-files --others --ignored --exclude-standard -z -- .claude/` and copies
  what it lists, minus the exclusions in the bullets below. A `.claude/` the repo
  git-ignores is therefore seeded into the new worktree. A `.claude/` that is
  merely untracked is not, because that view cannot see it. `.claude/worktrees/` is
  always skipped, because that is where session worktrees live. The listing is
  limited to the repo-root `.claude/`, so a nested one is reached only by the
  manifest pass. This is measured against `19f30c46` and `90fca6e6` alike.
- Claude runtime state is skipped by both passes since `90fca6e6`. The names
  are `scheduled_tasks.json`, `scheduled_tasks.lock`, `routines/.state`,
  `worktrees`, `checkpoints`, `mailbox`, `agent-registry.json`, `first-run` and
  `assistant-daemon-state.json`. They match as whole path components, directly
  under `.claude/`, case-insensitively. `.claude/Checkpoints/` is therefore
  dropped, while `.claude/mailboxes/` and `.claude/nested/mailbox/` are both
  copied. All nine names and both boundary cases are measured against `19f30c46`
  and `90fca6e6`. `19f30c46` copies eight of the nine. It drops `worktrees` as
  well, so both builds drop `worktrees`.
- The daemon skips symlinks. A filename that `git ls-files` C-quotes, with a tab, a
  quote, a backslash or a non-ASCII byte, IS copied. Both passes use `-z` and split
  on NUL. An earlier version of this document said the opposite and called it a
  reference limitation reproduced for parity. It was neither.
- The copies do not preserve the source mode. The daemon creates them
  0666-subject-to-umask, so an executable arrives non-executable and a `0400`
  source is widened. This matches the reference. Treat the manifest as a way to
  name configuration, not secrets or scripts.
- Destination containment on both passes is claustrum's own. Every copy resolves
  its destination component by component inside the new worktree. The copy is
  dropped when an intermediate component is a symlink, so a link checked out into
  the worktree cannot carry a copy outside it. Whether the reference refuses the
  same is unmeasured. No probe behind this section has a symlinked-intermediate
  fixture. Treat it as claustrum hardening, not parity. A `..` component cannot
  occur, because `git ls-files` never prints one.
- An opted-in `-git-timeout` (D5) that kills a `git ls-files` loses that pass. The
  reply is still `{"success":true}`, so the loss is silent and wire-invisible. Each
  pass has its own deadline, so the manifest copy can succeed while the `.claude/`
  copy is lost, or the other way round. The `.claude/` pass has no manifest
  precondition, so it runs on every create, unlike the manifest pass. The loss
  itself still needs a listing slower than the deadline. This is off by default.

#### git.worktree_remove
`{baseRepo,worktreePath[,branchName][,worktreeRoot]}` → `{"success":true}` (lenient)

- By default, with no `worktreeRoot`, `7d193f89` confines the removal to inside the
  repository. Before git runs,
  `worktreePath` must be absolute, carry no `..` component, and sit strictly under
  `baseRepo`. Otherwise the reply is `{"success":false,"error":"refusing to remove
  worktree: <p> …"}`, with no `errorCode`, and with the same three reasons as
  `worktree_create`. An empty `worktreePath` is `{"success":false,"error":"failed to
  remove worktree: \"\" does not name a directory"}`. Only a path that passes these
  tests reaches git and the recursive-delete fallback below. The fallback therefore
  targets a path inside the repository. When `worktreeRoot` is supplied, it targets
  one two levels under that root, under the same external containment as
  `worktree_create`. An external remove also makes sure that `<p>` is a genuine
  registered worktree of `baseRepo` before it deletes it. `<p>/.git` must be a
  regular `gitdir:` pointer file naming `<baseRepo>/.git/worktrees/<name>` whose own
  record points back at `<p>`. Any other path is refused and LEFT IN PLACE, with
  `{"success":false,"error":"refusing to remove worktree: <p> is not a worktree of
  <repo> (<reason>), so it is left in place; remove it by hand if it is a
  leftover"}`. `<reason>` is one of `<p> has no .git file`, `<p>/.git is not a
  regular file`, `<p>/.git does not name a git dir`, `<p> carries a .git file that
  does not name this repository's own worktree admin directory`, or `<p> carries a
  .git file naming an admin directory whose own record is of a different worktree`.
  When `baseRepo`'s worktrees directory cannot be read, the daemon cannot decide,
  and the reply is transient: `{"success":false,"error":"failed to remove worktree:
  could not verify that <p> is a worktree of <repo> (<detail>); retry"}`. A stale
  registration whose admin dir is gone is still removed, and so is a `<p>` that is
  already gone (`success:true`). None of these paths reaches the recursive delete
  of an unrelated directory.
- The daemon runs `git worktree remove --force`. `7d193f89` refuses a LOCKED
  worktree here. git fails with `cannot remove a locked working tree`, and the
  reply is `{"success":false,"error":"refusing to remove worktree: <p> is locked
  (git worktree lock); unlock it to remove it"}`, and the directory is left in
  place. Any OTHER non-zero git exit takes a different path, for example an
  ordinary directory. The daemon then removes `worktreePath` itself, recursively,
  and it still answers `{"success":true}`. On a non-locked failure this method is
  a recursive delete of the caller-supplied `worktreePath`. Treat `worktreePath` as a path you
  ask the daemon to remove, not as a filter. Both are reference behavior, matched
  deliberately. The lock refusal is a `7d193f89` change, because before `7d193f89`
  the reference deleted the locked worktree too, through the fallback. The reply
  carries `{"success":false,"error":"failed to remove worktree: <git output>;
  manual cleanup also failed: <err>"}` only when the manual cleanup *also* fails.
- Without `worktreeRoot`, a `baseRepo` that is an existing directory in which git
  finds no repository is refused before `git worktree remove` runs, and nothing is
  deleted: `{"success":false,"error":"failed
  to remove worktree: could not check whether <p> is locked (its registrations could
  not be examined); retry"}`, with no `errorCode`. Examples are a plain directory, a
  repository whose `.git` lacks `objects/`, and a daemon `GIT_DIR` that names nothing
  usable. `f6010b97` refuses these, measured on Linux, macOS and Windows VMs, and
  `90fca6e6` answers the same. The same
  text answers a repository whose configuration cannot be read.
- Without `worktreeRoot`, a `baseRepo` that the daemon can open but not search
  (mode 0600) answers `{"success":false,"error":"failed to remove worktree: statat
  .claude: permission denied"}`. One that it cannot open at all (mode 0000) answers
  `failed to remove worktree: open <baseRepo>: permission denied`. Nothing is
  deleted in either case. Measured against `f6010b97` on Linux and macOS VMs.
- A request that names a non-existent branch still answers a bare
  `{"success":true}`. That is what "lenient" means here.
- A home-directory `worktreePath` is refused. The `7d193f89` containment now does
  it, as parity. A `~`-expanded home path is not strictly under `baseRepo`, so it is
  refused with the reference's `"…is not inside the repository…"` wording before git
  or the fallback. The claustrum-only D2 frame (`"worktreePath must not be or contain
  the home directory: …"`) is now behind that containment on this method's default
  branch. It fires only in the exotic case of a repository that is itself an
  ancestor of home. On the `worktreeRoot` / `external_root` branch the in-repo
  containment does not apply, so there D2 is the active home guard. D2
  remains the primary guard for `files.extract_tar`, which gained no containment. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- A relative `worktreePath` is refused upfront (`"…is a relative path…"`), so it
  never reaches git or the fallback. Before `7d193f89` the daemon resolved a relative
  path twice. git resolved it with `-C <baseRepo>`, and the manual cleanup resolved
  it against the daemon's working directory. The fallback was then able to delete a
  directory git never looked at. Containment closes that. Send an absolute path
  under the repository.
- The registration is pruned. `7d193f89` removes `$GIT_DIR/worktrees/<name>`
  along with the directory, so `git worktree list` no longer shows it and a later
  create at the same path succeeds. Before `7d193f89` neither binary pruned on the
  fallback path, so a re-create failed `already registered`. claustrum now reads the
  worktree's `.git` pointer before removal and drops the admin directory too.
- `gitTimeout` (D5) does NOT authorise the deletion, and this whole timeout arm
  is off by default. When armed it answers
  `{"success":false,"error":"git worktree remove timed out after <dur>; no cleanup
  was attempted, and git may have partially removed the worktree"}` and removes
  nothing. A hit on the earlier config or repository check answers the lock-check
  refusal instead. See [`DIVERGENCES.md`](DIVERGENCES.md) → D5.

### process.* (the agent/MCP-hosting core)

The client supplies its own `id`, which is any string. The daemon delivers output
as id-less stream notifications, and it buffers them for a later replay.

#### process.spawn
`{id,command[,args][,cwd][,env][,disableShellAgentSocket][,wantPid]}` → `{"success":true}`, then stream frames
- `args`: string[]. `env`: `{KEY:VAL}`, merged over the daemon environment.
- Missing `id` → `-32602 Process ID is required`. Missing `command` →
  `-32602 Command is required`.
- A request that reuses a still-live `id` succeeds and replaces the registry entry,
  like the reference. claustrum also kills the now-orphaned previous process
  tree. It drops the subscribers first, so no stray frame arrives under the
  reused id. This is OS-level only and changes no wire byte.
- Session superseding is `4534d86` parity. A `process.spawn` whose `args` name a
  stream-json CLI session terminates any OTHER running process of the SAME session
  id. Such `args` carry an `--input-format=stream-json` or
  `--output-format=stream-json` arg, or a bare `stream-json` arg. They also carry a
  valid `--session-id` or `--resume` token, with the resume fallback suppressed by
  `--fork-session`. The superseded process is killed through the
  SIGTERM-then-SIGKILL path, and its exit frame reaches its client. The
  `killedBy:"client"` marker on that frame ships in a separate slice. A spawn with
  no session key, or with a different session id, supersedes nothing. The eviction,
  the `client` kill reason, and the session-key rules above are measured against
  the reference. claustrum also serializes concurrent spawns of one session.
- The SSH agent hand-off is `f6010b97` parity on linux and darwin, advertised as
  `process.spawn.shellAgentSocket`, and measured on both. The daemon builds the
  child env from its own env, the login-shell PATH and the caller's `env`. If that
  env has no `SSH_AUTH_SOCK` entry, the child gets `SSH_AUTH_SOCK=<socket>` after
  the caller's entries. The socket is what the user's login shell exports.
  - An entry that is already present wins, even with an empty value. A caller can
    therefore skip the hand-off for one spawn with `"SSH_AUTH_SOCK":""`.
  - `"disableShellAgentSocket":true` also skips it. A non-bool value answers
    `-32602 Invalid params`, and `null` counts as `false`.
  - The daemon runs `<shell> -l -i -c …` on the first spawn that needs a socket,
    not at startup. The shell choice is the one the PATH extraction uses: `$SHELL`
    when it is executable, then `/bin/zsh`, `/bin/bash` and `/bin/sh`. The run has
    a 4 s deadline, and a shell that the kill cannot end is given up on at 5 s. The
    spawn that runs the shell therefore answers about 4 s later when the shell
    does not exit, and about 5 s later when the kill cannot end it.
  - A socket that does not accept a connection within 250 ms is not handed on. The
    daemon caches the answer and runs the login shell again only 10 minutes after
    the last run. After 3 failed runs in a row it stops asking. A connection
    attempt that has not returned after 500 ms ends the hand-off for the life of
    the daemon.
  - A spawn never fails because of this step. At the default log level, each
    login-shell run and each give-up writes a `[shellenv]` line to the daemon log.
    A spawn served from the cache, or skipped, writes none.
  - On Windows the capability and the param exist, but spawn runs no login shell
    and adds no `SSH_AUTH_SOCK`. Measured with a Git bash `$SHELL` whose profile
    exports a live socket: neither daemon starts it.
- `wantPid` is a claustrum-only opt-in, CT-1. With `"wantPid":true` the reply gains
  two fields after `success`: `{"success":true,"pid":<int>,"startTime":<number>}`.
  `pid` is the child's OS pid. `startTime` is the daemon's wall clock in epoch
  seconds, captured at spawn. Spawn and reattach return the identical value for the
  same process. It is an opaque token for PID-reuse and orphan detection. Compare a
  persisted daemon value against a later daemon value for the same id. Do not
  equality-compare it against an OS-read process start time
  (`psutil create_time`), because the two derivations differ. When `wantPid` is
  absent or `false`, the daemon omits both fields through `omitempty`, and the
  frame is byte-identical to `{"success":true}`. An older daemon ignores the
  unknown param through its tolerant decode. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → CT-1.
- The exec-child trampoline is `19f30c46` parity on linux and darwin. When the
  daemon's socket is `run/<clientId>/rpc.sock` shaped, a spawned child is launched
  through a self-re-exec trampoline (`<self> --exec-child <path> <argv…>`). That
  trampoline adds two environment markers that give the child a stable,
  pid-reuse-safe identity. The first is `CLAUDE_SSH_RUN_DIR=<run dir>`, set by the
  daemon. The second is `CLAUDE_SSH_CHILD=<pid>:<startTicks>`, set by the
  trampoline from the child's own pid and its start-time. On linux the start-time
  is the `/proc` clock ticks. On darwin it is the `ps` start-time. The five
  Go-runtime vars (`GODEBUG`, `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT`, `GOTRACEBACK`)
  are stashed under `CLAUDE_SSH_HELD_<name>` across the re-exec. They are restored
  before the target runs, so they do not perturb the transient trampoline. A
  command that
  does not resolve to a runnable file is not trampolined, so its spawn error frame
  is unchanged (`fork/exec …` or `exec: … not found in $PATH`). A bare or
  non-run-shaped socket spawns directly, with no trampoline and no markers. This is
  off-wire. It adds no JSON-RPC frame and it changes none. It was verified against
  `19f30c46` on a VM. The marker set and format match, and the values are
  per-process. The held Go vars round-trip. A missing target, a
  non-executable-format target, and a relative-under-`cwd` target each return the
  identical `-32603 fork/exec …` frame, or are trampolined exactly as the
  reference does. Darwin links the same subsystem, with the start-time from `ps`
  instead of `/proc`. This was validated against `19f30c46` on a macOS VM. On
  windows the reference daemon reports it is not the run-dir lock holder, so it
  stamps neither marker. A windows VM showed that a run-shaped-socket child has the
  same environment as a bare-socket child.
- The orphan-child registry record is `19f30c46` parity on linux and darwin. Under
  that same `run/<clientId>/` socket, each spawned child with a pid of 2 or more
  and a readable start-time is recorded to `<runDir>/children/<pid>.json`. The
  daemon writes it atomically, through a temp file renamed into place. A later
  daemon reads these to reap children a since-exited daemon left behind. The record
  is an ordered JSON object:
  `{"pid":<int>,"node":"<boot-id>/pid:[<inode>]","host":"machine-id:<hex>","instance":"<daemon instance id>","daemonPid":<int>,"daemonStart":"<ticks>","argv0":"<child argv0>","start":"<ticks>","at":<epoch-ms>}`.
  The field ORDER and the string-vs-number typing are the on-disk contract,
  measured byte-for-byte against `19f30c46`. `daemonStart` and `start` are STRINGS,
  holding clock ticks on linux and a `ps` timestamp on darwin. `pid`, `daemonPid`
  and `at` are numbers. This record is off-wire, because it adds no JSON-RPC
  frame. On linux and darwin the daemon reaps these records at `-serve` startup.
  See [ARCHITECTURE.md](ARCHITECTURE.md) → orphan reap. On darwin the node is the
  boot-session UUID and the host is the hostname, and the start-times come from
  `ps` rather than `/proc`. On windows the reference daemon reports it is not the
  run-dir lock holder, so it records no children and reaps none. A windows VM
  showed that a spawned child leaves the children dir empty, and that planted
  records survive startup.

#### process.stdin
`{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}`
- `data` is base64. The daemon writes it to the child's stdin.
- The tests run in a fixed order: decode, then exists, then offset, then running.
    - Invalid base64 → `-32602 Invalid base64 data`. The daemon returns this
      *before* it looks up the process, so an unknown id with a bad payload still
      reports the decode error.
    - Unknown id → `-32602 Process not found`.
    - The offset idempotency verdict is evaluated next, even for an exited
      process. An offset gap returns `-32003`, and a wholly-duplicate write
      returns `{"success":true,…,"duplicate":true}`, whatever the running state is.
    - A known process that exited or was already reaped → `-32602 Process not
      running`. This applies only when the write carries fresh bytes. Since
      `90fca6e6` the reap counts, not just the exit frame, so this also covers the
      drain window. See the exit-drain note under Stream notifications.
- `offset` and `applied` are the resumable-stdin contract. `7c2f88d` added them,
  and they are advertised as `process.stdin.offset`. The reply always carries
  `applied`, which is the cumulative count of stdin bytes accepted for delivery,
  the high-water mark. `offset` is the byte position the caller believes this
  `data` starts at. `offset` makes stdin idempotent across reconnects:
    - An absent `offset`, or `offset == applied`, makes the daemon append, and
      `applied` grows by `len(data)`.
    - `offset > applied` → `-32003 stdin offset gap: offset ahead of applied bytes`.
      If accepted, that gap drops input. Resend from `applied`. The daemon
      enqueues nothing.
    - `offset + len(data) <= applied`, which is wholly applied, is a no-op. The
      reply adds `"duplicate":true`, `applied` does not change, and nothing reaches
      the child.
    - A partial overlap (`offset < applied < offset+len`) makes the daemon write
      only the fresh tail `data[applied-offset:]`, and `applied` advances to
      `offset+len(data)`. The daemon does not flag this as a duplicate.
  `applied` counts base64-decoded bytes, and it is never `omitempty`, because the
  daemon emits it at 0. The daemon drops `duplicate` when it is false. A legacy
  client that never sends `offset` still works, because it always appends.
- Backpressure gives `-32002`. The per-process async stdin queue is bounded at
  16 MiB. The queue can be non-empty while a producer outruns a slow or non-reading
  child. When this write then pushes the queue past the cap, `process.stdin`
  returns `-32002 stdin backpressure: queue full` and enqueues nothing. `applied`
  does not change, so resend once the child drains. The request is rejected, never
  blocked. This is parity with `4534d86`, because the reference emits this frame at
  the same boundary, as measured by the probe `scratch/probe/stdincap`.
  A lone write larger than the whole cap on an empty queue is exempt. claustrum
  enqueues it rather than rejecting it. This is an internal edge, not a parity
  claim. A `data` field that large exceeds the 1 MiB request-line cap and closes
  the connection first. No wire client can therefore reach it on either daemon.

#### process.kill
`{id[,signal]}` → `{"success":true}`
- Best-effort and fire-and-forget. It does not wait for the child to exit.
  `process.killAndWait` does wait.
- On Unix, how wide the signal reaches depends on the signal:
    - `KILL` goes to the whole process group, as a negative pid, so the entire
      child tree dies.
    - Every other signal (`TERM`, `INT`, `HUP`, and the default) goes to the direct
      child only, and a backgrounded grandchild keeps running. A graceful
      `process.kill` does not kill the tree. Use `signal:"KILL"`, or use
      `killAndWait` with `escalate:true`.
  The split does not apply on Windows. There claustrum terminates the Job Object,
  which takes the tree either way.
- claustrum diverges here. It skips the signal when the child has already exited,
  because the OS can recycle a reaped pgid. This is OS-level only, and the reply is
  identical.

#### process.killAndWait
`{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}`

Added by `7c2f88d`. It blocks until the process is gone, up to the grace, and
reports the outcome as a *result*. An unknown id is not an error:
- Missing `id` → `-32602 Process ID is required`. Absent `params` → `-32602 Invalid
  params`.
- Unknown id → `{"found":false,"died":false}`.
- Already exited → `{"found":true,"died":true,"alreadyExited":true}`. The daemon
  sends no signal.
- Inside the exit drain this method is the exception. Since `90fca6e6`,
  `process.reattach` and `process.stdin` answer as if the process is not running
  there. In claustrum, `killAndWait` still reads the flag that flips with the
  exit frame. A call inside the drain therefore answers `alreadyExited:false` and
  waits for the frame rather than reporting an already-exited process. No signal
  is delivered either, because the daemon refuses to signal a reaped process.
  Keeping this method unchanged is claustrum's choice. The reference answer inside
  the drain is not probe-measured.
- Live process → the daemon sends the graceful `signal`, `SIGTERM` by default, and
  then waits up to the grace:
    - `timeoutMs` sets the grace. A non-positive or absent value gives the
      3000 ms default. The daemon honors a positive value verbatim up to a
      30000 ms ceiling, and it clamps a larger value. `timeoutMs:45000` against a
      signal-ignoring child therefore answers after about 30 s. The `30000` ceiling
      is a black-box bracket `(29500, 30500]`. It is the only round value in that
      bracket, not a measured-exact figure.
    - `escalate` is `true` by default. If the process is still alive after the
      grace, `true` escalates to a process-group `SIGKILL`, waits up to 7 s
      for the reap, and adds `"escalated":true`. This is measured. `timeoutMs:500`
      against an unreapable child makes the reference reply at 7.51 s. The daemon
      sends the SIGKILL even when the graceful signal already killed the child. A
      grandchild that holds the stdout pipe can keep the drain pending past the
      grace. `false` leaves the process running and reports
      `{"found":true,"died":false}`, with no `escalated` and no SIGKILL, which
      spares the tree.
- A process that dies within the grace → `{"found":true,"died":true}`, with no
  `escalated`.

#### process.reattach
`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`
- A missing or empty `id` → `-32602 Process ID is required`. This is the same frame
  `spawn` and `killAndWait` document. Both `"id":""` and an absent `id` were
  probed.
- The daemon replays buffered frames with a seq above `fromSeq`, exclusive, to this
  connection. It transfers the frame stream to that connection, and then returns
  the result.
- The transfer is exclusive. A reattach does not add a second listener. Any
  connection attached before stops receiving frames for that process. This is what
  makes a resume safe.
- Since `90fca6e6` the transfer also CLOSES the connection it replaced. The
  daemon closes it once and logs a reason naming the process. Before `90fca6e6`
  the old connection stayed open. A client then held a connection that never
  carried another frame, and it had no sign that the session moved. This is
  measured. On
  `19f30c46` the old connection still answers `server.ping` after another
  connection reattaches, and on `90fca6e6` the next write to it fails. A reattach
  on the connection that is already attached closes nothing.
- Since `90fca6e6`, `running` is false for a reaped process. Inside the bounded
  exit drain the daemon already waited on the process but did not yet emit the
  exit frame. A reattach in that window answers `running:false`. See the
  exit-drain note under Stream notifications, and the same rule under
  `process.stdin`.
- The cut is by `seq`, not by wall-clock. The transfer point is the reported
  `lastSeq`. The old connection never receives a frame above it. It can still
  receive one `<= lastSeq`, from a write already in flight when the
  transfer took the process off it. Since `90fca6e6` that window is bounded by the
  supersede's close rather than lasting until the client hangs up. claustrum
  runs the close before it writes the reply. Whether a frame can still land on the
  old connection after the client reads the reply is unmeasured. No frame reaches
  the old connection and is also absent from the new connection's replay. That is
  what `fromSeq` is for.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- The daemon retains an exited process for a bounded time and then drops it,
  together with its replay buffer. An id last seen longer ago therefore answers
  exactly like an unknown one, and `process.kill` on it still reports
  `{"success":true}`. The daemon never drops a running process. On the wire, the
  reference retention brackets only to `(45 s, 960 s]`. claustrum retains for 15
  minutes. It sweeps on a 60-second timer and inline on every `process.spawn`.
  Those two values are claustrum's own and are not probe-measured. Read `found:false` after a long gap as "finished and forgotten",
  not as "never existed".
- `stdinApplied` was added by `7c2f88d`. It is the process's cumulative
  applied-stdin byte count, as described under `process.stdin`. It is always
  present after `lastSeq`. A reconnecting client resumes stdin from this offset. It
  is an acknowledgement, not a delivery receipt. `process.stdin` returns before the
  child reads, so the daemon counts bytes accepted just before exit even though the
  writer never delivered them. A client that must know that data arrived makes sure
  of that in-band.
- `wantPid` is opt-in, CT-1. With `"wantPid":true`, and with the process found, the
  reply appends `"pid":<int>,"startTime":<number>` after `stdinApplied`. It
  reports the same pid and startTime the spawn reported. A client can therefore
  make sure that it reattached to the same process and not to a pid-reuse. The
  daemon omits both fields otherwise.

### plugins.* (added `19f30c46`)

#### plugins.prune
`{[keep],[minAgeDays]}` → `{"root":"<per-install root>","pruned":[…],"prunedLegacy":[…],"staleArchives":0,"kept":0,"live":<n>,"young":<n>,"legacy":"absent|swept|skipped: …","minAgeDays":<clamped>}`

Prunes cached CLI plugin directories the daemon keeps under its socket's
run-directory layout. The daemon socket is `<X>/run/<clientId>/rpc.sock`. The
per-install root is `<X>/plugins/<clientId>`, and its results go in `pruned`. The
shared legacy root is `<X>/plugins`, and its results go in `prunedLegacy`.

- A plugin directory is named by its content hash, 16 lowercase hex. Its
  last-used time is the newer of the directory's own mtime and its `.synced`
  marker's mtime. The `manifest.json` mtime does not count. A directory is kept
  when it is live, and it is kept when it is young. Live means its hash appears in
  a running child's argv, because the agent CLI is launched with
  `--plugin-dir <…>/<hash>`. That gives the `live` count. Young means last-used
  within `minAgeDays`, which gives the `young` count. Otherwise it is removed and
  its hash is appended to `pruned` or `prunedLegacy`, each sorted.
- `minAgeDays` is the retention window, clamped to `[7, 3650]`. An absent value
  reads as 0 and clamps up to 7. The clamped value is echoed.
- `keep` is a `[]string` accepted on the params. `kept` and `staleArchives` are
  always-present counters. Neither `keep`, a caller list, nor `staleArchives`,
  stale non-directory files, was observed to fire against `19f30c46`. A valid old
  plugin whose hash was in `keep` was still pruned, and no plain file was swept
  whatever its extension. Both counters therefore stay 0 here. They
  are reproduced as fields for parity. Their non-zero triggers are not yet pinned.
- `legacy` reports the shared `plugins/` sweep, and the sibling test runs
  first. If a sibling daemon on the host is present, or cannot be ruled out gone,
  `legacy` is `"skipped: another install's daemon is running (<detail>); its sessions
  may use the shared legacy dirs"`, even when the shared directory does not exist.
  `<detail>` describes what blocked the sweep. It names the first present sibling
  in sorted `run/<clientId>` order, or the run directory itself on a failed
  listing. It takes one of four forms, where `<rel>` is
  `run/<clientId>/rpc.sock`:
  - `"<rel> answers"`: the sibling socket accepted a 300 ms unix dial.
  - `"cannot rule out <rel>: <err>"`: the dial failed for a reason other than
    connection-refused, not-a-socket, or not-found.
  - `"cannot inspect <sock>: <err>"`: a stat of the socket path failed.
  - `"cannot read <run-dir>: <err>"`: a listing of the run directory failed.

  With no sibling present, `legacy` is `"absent"` when the shared directory is
  missing, or `"swept"` once it is processed. The per-install sweep (`pruned`)
  always runs. The daemon skips only the legacy sweep, because a present sibling's
  sessions can reference the shared legacy plugins.
- If the socket is not `run/<clientId>/rpc.sock`, the daemon has no per-install
  root. `root` is then `""`, `minAgeDays` is 0, `legacy` is
  `"skipped: <reason>"`, and a `skipped` field carries the reason.
- Bad params answer `-32602 "Invalid params: <encoding/json detail>"`. That frame
  carries the detail, unlike the other methods' bare `Invalid params`. A
  `plugins.<other>` method answers `-32601 "Unknown method: plugins.<x>"`.
  `plugins.prune` requires auth like every method except `server.shutdown`.

### Stream notifications

```jsonc
{"type":"stream","processId":"<id>","stream":"stdout","seq":1,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"stderr","seq":2,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":0}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":-1,"signal":"SIGTERM","killedBy":"client"}
```

- `seq` is per-process. It starts at 1 and is monotonic across
  stdout/stderr/exit.
- `data` is base64 for stdout/stderr. The `exit` frame carries `exitCode` and no
  `data`. A signal-terminated child reports `exitCode: -1`, not `128+signo`.
- `signal` and `killedBy` were added by `4534d86`. They appear on the `exit` frame
  only, both after `exitCode`, and both are omitempty, so a normal exit stays
  byte-identical. `signal` is the SIG-prefixed name of the terminating signal, such
  as `SIGTERM` or `SIGKILL`, read from the wait status. It is omitted on a normal
  exit, and it is omitted always on Windows, which has no signal on the wait path.
  That was measured on a Windows VM, where the reference omits `signal` and still
  emits `killedBy`. `killedBy` names who asked the daemon to kill the process. It
  is `client` for `process.kill` and `process.killAndWait`, and `shutdown` for the
  shutdown and `killAll` sweep. `killedBy` is emitted on every OS. The `client`
  values and the `SIGTERM` and `SIGKILL` names are live-measured. The other mapped
  signal names derive from the same wait-status path and are not individually
  measured against the reference. The `shutdown` value is not probe-measured,
  because the shutdown exit frame races connection teardown
  and is not client-observable.
- The `exit` frame waits at most 5 seconds after the process exits for
  stdout/stderr to reach EOF. The daemon then closes the read ends and emits the
  frame anyway. This matters when the command leaves a grandchild that holds the
  same pipe, as `npm run dev &` does. The daemon does not forward output that the
  grandchild writes after the cap, because that write fails `EPIPE`. The
  `process.reattach` `running` flag does not wait for that frame. Since `90fca6e6`,
  a reattach inside the drain window reports `running: false`. In claustrum it
  flips at the reap itself. On the reference the flip is measured one second into
  the drain. The probe therefore bounds the flip
  at one second rather than at the reap.
  `process.stdin` refuses inside the same window with `-32602 Process not
  running`, for a write that carries fresh bytes. An offset gap still
  answers `-32003` and a wholly duplicate write still answers
  `{"success":true,…,"duplicate":true}`. Before `90fca6e6`, both paths reported
  the process as still running inside the drain, measured on `19f30c46`. The
  refusal also stops the drain window from inflating `applied` and `stdinApplied`, which used to count bytes the
  closed pipe discarded. The acknowledgement caveat under `stdinApplied` still
  applies for a write accepted while the process was genuinely live.
- Each stdout/stderr frame carries at most one 32 KiB read. Larger output splits
  across frames. Concatenate `data` in `seq` order to reassemble it. The exact
  frame *boundaries* depend on pipe scheduling and are not stable. Only the
  reassembled bytes are stable.
- The replay buffer has a bound of 16 MiB per process. The daemon counts the
  serialized frame including its trailing newline, which is the bytes a subscriber
  receives, not the base64 `data` alone. An exit frame therefore costs its envelope
  although it carries no `data`. The daemon drops frames oldest-first, whole frames
  at a time, once adding a new frame pushes the buffer total over the cap. It always retains at least one
  frame, even a frame larger than the cap. `reattach{fromSeq:0}` therefore replays
  everything still retained, and not necessarily everything ever emitted.
  `firstSeq` is the floor. Compare it against the last `seq` you saw to detect a
  gap.
- A process survives the disconnect of the connection that spawned it. Another
  connection picks it up with `reattach`. This is the multi-attach and reconnect
  mechanism.

## Daemon lifecycle (flags)

One binary, six modes: `-serve`, `-bridge`, `-stop`, `-version`, `-install` and
`-probe-cli`.

### Flags and config keys

Every opt-in divergence flag defaults to its zero value, which is OFF, and which
is byte-identical to the reference. Each flag has a matching `claustrum.conf` key.
Claude Desktop owns the `-serve` and `-install` argv. That is a driver claim. See
[ARCHITECTURE.md → Driver claims and their
provenance](ARCHITECTURE.md#driver-claims-and-their-provenance). The configuration
key is therefore the reachable knob. The precedence is: explicit CLI flag, then
configuration, then default. A disabled bound bypasses the guard entirely. It is
never "a huge limit".

| flag | config key | default | effect when set | mode |
|---|---|---|---|---|
| `-token-file <p>` | none | none | token source (read once, unlinked) | -serve |
| `-token-fd <n>` | none | `-1` | token from an open fd (claustrum-only) | -serve |
| `-metrics-addr <a>` | `metrics-addr` | `""` | Prometheus `/metrics` (claustrum-only, CT-3) | -serve |
| `-wire-log <p>` | `wire-log` | `""` | append every JSON-RPC frame to `<p>` as JSONL (claustrum-only, CT-3) | -serve |
| `-wire-log-max-string <n>` | `wire-log-max-string` | `512` | bytes kept per string value. `0` keeps whole payloads | -serve |
| `-keep-children` | `keep-children` | off | survive restart, POSIX-only (CT-2) | -serve |
| `-listen-pipe` | `listen-pipe` | off | named-pipe transport, Windows-only (CT-5) | -serve |
| `-max-extract-bytes <n>` | `max-extract-bytes` | `0` | cap `files.extract_tar` bytes (D3) | -serve |
| `-git-timeout <dur>` | `git-timeout` | `0` | deadline on git invocations (D5) | -serve |
| `-files-read-regular-only` | `files-read-regular-only` | off | refuse non-regular `files.read` (D4) | -serve |
| `-max-cli-bytes <n>` | `max-cli-bytes` | `0` | cap CLI decompress + download (D10) | -install |
| `-cli-probe-timeout <dur>` | `cli-probe-timeout` | `0` | `<cli> --version` deadline (D11) | -install |
| `-cli-download-timeout <dur>` | `cli-download-timeout` | `0` | download deadline (D12) | -install |
| `-libc-probe-timeout <dur>` | `libc-probe-timeout` | `0` | `ldd --version` deadline, linux only (D14) | -install |
| `-cli-keep <n>` | none | `3` | versions to retain on prune | -install |
| none (config only) | `version-override` | none | `-version` stdout rebrand (CT-3) | -version |

Config-value parsing: bool keys accept `true/1/yes/on` and `false/0/no/off`. The
parser ignores a negative or unparseable numeric value, so a typo can never
silently enable a cap. An unrecognised bool value leaves the key unset, and the
flag value, or the default, stands.

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] \
          [-keep-children] [-listen-pipe] [-wire-log <p> [-wire-log-max-string <n>]] \
          [-max-extract-bytes <n>] [-git-timeout <dur>] [-files-read-regular-only]
```

The binary self-daemonizes, which means it reparents to init and detaches. It then
extracts the login-shell PATH on Unix, and then runs the RPC server. On success it
prints `Claustrum remote server listening on <socket>` to stdout.

When `$SHELL` is an executable file, login-shell PATH extraction on Unix runs
`$SHELL -l -i -c …`. Otherwise it runs the first usable of `/bin/zsh`,
`/bin/bash` and `/bin/sh`. zsh comes first, which matches the reference. The value
reaches
`process.spawn` children as their `PATH` only. It never reaches the daemon's own
environment, so it never changes how the daemon resolves a `command`. The
extraction has a cap of 4 s. On a timeout the daemon discards whatever the shell
printed, even a valid PATH, and children fall back to the inherited PATH.

A token source is required. The detached child tests for it, not the launcher:
- Both flags missing → the launcher daemonizes anyway, the child refuses to start,
  and the launcher reports its accept timeout after about 10 s:
  `claustrum: timeout waiting for daemon to accept on <socket>`, exit `1`. The
  specific reason (`claustrum: daemonized child requires --token-file or
  --token-fd`) reaches only the child's detached stderr. This is deliberate
  parity, because the reference exits 1 at about 10 s the same way. A zero-byte
  `-token-file` behaves identically.
- The daemon reads the token as a line. It strips one trailing `\n` or `\r\n`, and
  preserves other surrounding whitespace verbatim.
- A bad `-token-file` → `claustrum: read --token-file: <err>`, exit `1`.
- `-token-fd <n>` is claustrum-only. It reads from an already-open fd, where `0` is
  stdin, so this handoff never touches disk. The launcher forwards it to the
  detached child over an inherited pipe.

The daemonize sentinel is internal and claustrum-namespaced. The re-exec marker is
`CLAUSTRUM_DAEMON_CHILD`, not the reference's `CLAUDE_SSH_DAEMON_CHILD`. The
reference name cannot serve here. A host that runs *inside* a real claude-ssh
session exports `CLAUDE_SSH_DAEMON_CHILD=1` ambiently. If claustrum used that name,
the launcher mistakes itself for the already-daemonized child. claustrum keeps the observable
parity separately. `daemonizeWithToken` still sets `CLAUDE_SSH_DAEMON_CHILD=1` in
the daemon's environ, so that variable propagates into `process.spawn` children.
`TestSpawnInheritsDaemonChildMarker` pins that. claustrum unsets the internal
marker before it spawns.

Claustrum-only extras follow. They are off the wire, and the canonical detail is in
[`DIVERGENCES.md`](DIVERGENCES.md):
- `-metrics-addr <a>` is CT-3. It serves Prometheus counters at
  `http://<a>/metrics`, covering connections, spawns and exits, reattaches, and
  stream and stdin bytes. It is off by default, with no listener. It counts only,
  and it has no auth, so bind it to loopback. The daemon logs a bind failure
  (`[Server] metrics: …`), which is non-fatal.
- `-wire-log <p>` is CT-3. It appends every JSON-RPC frame, in both directions, to
  `<p>` as JSONL. It is a diagnostic side channel that observes already-marshaled
  bytes, so a daemon that logs emits frames byte-identical to one that does not. It
  is off by default, with no file and no work. `-wire-log-max-string <n>` bounds
  each string value. The default is 512, and `0` keeps whole payloads, which is
  needed to reconstruct a session from stream frames. Credentials are redacted by
  key, which covers the `auth` member and token-like env keys. A secret a client
  embeds inside a payload string is not caught, so redaction is best-effort, not a
  guarantee. A capture holds whatever the client sent, such as `files.write`,
  `process.stdin` and the spawn env. It is therefore forced to `0600` on every
  open, appends included, and it belongs somewhere private. Each record carries the
  frame as a decoded `body`, which is structured and truncated per value. That is a
  normalized view, so field order is not significant there. At
  `-wire-log-max-string=0` the record carries the frame as `raw` instead, verbatim,
  preserving the field order and number formatting that *is* the wire contract. An
  unopenable path is fatal, not silent.
- `-keep-children` is CT-2 and POSIX-only. It is off by default, so a graceful
  shutdown kills the whole child tree. When set, it leaves spawned children running
  across a restart, and logs `[Server] -keep-children: leaving <n> running child
  process(es) alive across shutdown`. The new daemon does not re-adopt them, and
  the survivors lose their stdio. Their stdin reaches EOF, and a stdout or stderr
  write gets SIGPIPE, or EPIPE for a child that ignores SIGPIPE, such as Node. It
  therefore suits only children that tolerate dead stdio. Windows ignores it and
  logs a warning (`[Server] -keep-children is not supported on Windows …`), because
  the Job Object terminates children in every case.
- `-listen-pipe` is CT-5 and Windows-only. See [Named-pipe
  transport](#named-pipe-transport-windows-opt-in). The daemon logs a setup failure
  (`[Server] named-pipe transport: …`), which is non-fatal. The socket still serves.

The opt-in divergences on this mode are `-max-extract-bytes` (D3),
`-git-timeout` (D5) and `-files-read-regular-only` (D4). Off is parity. Their wire
frames appear in the method sections above, under `files.extract_tar`, `git.status`,
`git.list_branches`, `git.worktree_remove` and `files.read`. See the flags table and
[`DIVERGENCES.md`](DIVERGENCES.md).

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

A simple relay. It is the thing an SSH session attaches to. It adds no auth. The
client that speaks through it supplies `"auth"` itself. It is strict, so a dial
failure is a hard error: `claustrum: dial server: <err>` on stderr, exit `1`.

### -stop — ask a running daemon to shut down

```text
claustrum -stop -socket <p>          # no token needed, and none is read
```

`-stop` sends `server.shutdown` with no `auth` member, because that method is
not authenticated. See [Authentication](#authentication). The request is
`{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}` and a newline. `-stop` then
reads once, with a 2 s deadline, and discards what it reads. It never relays a
frame to stdout. A live daemon's reply does not always arrive. On one connection
the claustrum reply arrived in 0 of 50 runs on Linux and on Windows, where the
`f6010b97` reply arrived in 5 of 50 on Linux and in 21 of 50 on Windows. This is
a measured timing difference, not parity. See `server.*` above for the
counts with a second idle connection. `-stop` prints `stopped` whether the
reply arrives or not.

`-stop` prints one word on stdout, then a newline, and exits `0` in every state
in the table below. The words and the rules below are measured against
`f6010b97` and `90fca6e6` on a Linux VM, except the rules marked as claustrum's
own. After `SIGKILL`, `-stop` polls the lock for 1.5 s. The word `killed` needs
the lock to become free in that wait. The macOS results are below.

| word | state |
|---|---|
| `stopped` | The connect succeeded. A reply, garbage, an EOF, a reset and the read deadline all give this word. |
| `none` | The connect failed, and `daemon.lock` is free or missing. claustrum also prints `none` when the lock cannot be opened or locked. |
| `terminated` | The connect failed, and a live serve daemon of this binary for this socket holds `daemon.lock`. `-stop` sent `SIGTERM`, and the lock became free. |
| `killed` | As `terminated`, but the lock was still held 4 s after `SIGTERM`. `-stop` sent `SIGKILL`. |
| `survivor` | The connect failed, and a live process that is not that daemon holds `daemon.lock`. `-stop` does not signal it. The word is also `survivor` if the lock is still held 1.5 s after `SIGKILL`. `-stop` does not wait longer. claustrum also prints `survivor` if the holder check fails again before `SIGKILL`. It prints it too when a signal cannot reach a holder that is still there. |

The lock check uses the holder test of the serve eviction. See [Run-dir
lock](#run-dir-lock-daemonlock). The exact rule of the Linux reference was not
isolated. Windows has no run-dir lock, so a failed connect there always prints
`none`.

On macOS the words and effects match the references in 22 of 24 measured states.
The two other states have a live lock holder that is not a serve process. The
macOS reference signals any live holder whose lock record says role `serve` on
this host's node. It does not check the holder's command line. claustrum keeps
its holder check on macOS, so it does not signal such a holder and prints
`survivor`. This is part of [D15](DIVERGENCES.md#d15). It was measured on a macOS
VM against `f6010b97` and `90fca6e6`.

The lock path writes these lines to stderr. claustrum adds its own level tag, as
it does for the serve eviction lines. The message text of these five lines is the
reference text:

```text
<ts> INFO  [daemon] stop: run dir is held by a live daemon, pid <N> (instance "<hex32>"); sending SIGTERM
<ts> INFO  [daemon] stop: previous daemon pid <N> (instance "<hex32>") exited after SIGTERM
<ts> WARN  [daemon] stop: WARNING previous daemon pid <N> (instance "<hex32>") ignored SIGTERM for 4s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)
<ts> WARN  [daemon] stop: WARNING <dir>/daemon.lock is held by a live process we will not signal (pid <N> is not a <basename> --serve process for <sock>); leaving it alone
<ts> WARN  [daemon] stop: WARNING previous daemon pid <N> (instance "<hex32>") survived SIGKILL (uninterruptible?); proceeding without run-dir ownership
```

This line is claustrum's own. It comes from the second holder check:

```text
<ts> WARN  [daemon] stop: pid <N> is no longer our serve holder before SIGKILL (<reason>); leaving it alone
```

After a successful connect, `-stop` removes nothing. A live daemon removes its own
socket and `daemon.token` when it stops. After a failed connect, `-stop` removes
the socket path and the `daemon.token` beside it, stale or not. It does this after
the lock check. If the word is `survivor`, `-stop` removes neither file. This is
measured for a holder that `-stop` does not signal and for a lock that outlives
`SIGKILL`. The other `survivor` cases follow the same rule. `-stop` never creates
or removes `daemon.lock`, and it writes no record into it. The references also
never create or remove it, and write no record into it. The socket unlink removes
a stale socket, a regular file and an empty directory. It keeps a non-empty
directory, and a path in a directory that the user cannot write. The references do
the same.

If the word is not `survivor`, a live foreign listener that refuses the connect
with `EACCES` loses its socket path. The listener stays alive, but a client that
dials by path cannot reach it afterwards. The unlink does not check what the path is. A variant that removes
only a socket keeps a regular file and an empty directory at the path. It does
not keep the socket of such a listener. That variant is a candidate not taken.
It is recorded under [Candidates considered but not
taken](DIVERGENCES.md#candidates-considered-but-not-taken).

`-version`, `-install` and `-probe-cli` win over `-stop`. `-bridge` and `-serve`
win too. With `-stop -bridge` claustrum runs the bridge, and with `-stop -serve`
it runs serve.

> Upgrading a live daemon needs care. A daemon still running from a build that
> predates the shutdown-auth-exemption change *does* require auth on
> `server.shutdown`. It answers `-32001` and keeps running. `-stop` prints
> `stopped` and exits `0` either way, so the caller sees success while the old
> daemon survives. Stop the old daemon before you upgrade, or kill it by PID once.

### -version

```text
claustrum -version                   # → claustrum <id> (built <time>)
```

`version-override` through `claustrum.conf` is an intentional divergence. It is
claustrum-only, CT-3. `claustrum.conf` is an optional `key = value` file. claustrum
reads it from the directory that holds the binary, and it gates the opt-in
divergences above. An absent or malformed file gives stock behaviour. The file can
set `version-override` to a bare commit SHA. That SHA is a 40-hex git SHA-1, the
string the desktop client pins. claustrum also accepts 64-hex, and anything else is
a no-op. The output then becomes:

```text
claustrum -version                   # → claude-ssh <sha> (via Claustrum <id>, built <time>)
```

This exists so that the desktop client treats an already-deployed claustrum as
up-to-date. That client decides whether to re-upload from a `<bin> --version`
output that matches `/claude-ssh\s+(\S+)/`. The override is CLI stdout only, not a
JSON-RPC frame, so it does not touch the wire contract. `server.capabilities`
still reports claustrum's own `<id>`. `server.version` was removed in `7d193f89`.
See
[`DIVERGENCES.md`](DIVERGENCES.md) → CT-3.

### -install — ensure the agent CLI

```text
claustrum -install -cli-dir <d> -cli-version <v> \
          [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>] \
          [-max-cli-bytes <n>] [-cli-probe-timeout <dur>] [-cli-download-timeout <dur>] \
          [-libc-probe-timeout <dur>]
```

`-install` downloads, verifies, extracts and prunes, and then prints one
`__INSTALL_RESULT__<json>` facts line (schema in
[ARCHITECTURE.md](ARCHITECTURE.md)). `-install` always exits `0`. It reports a
failure inside the facts as `cliError`, not through the exit code. `-install`
reaches the network only with `-cli-url`.

The `cliError` catalogue follows:

| `cliError` | trigger |
|---|---|
| `installed cli at <path> is not runnable` | post-extraction `--version` probe failed (or timed out, D11) |
| `cli <v> missing and no --cli-url or --cli-zst provided` | cache miss (or a cache-hit probe timeout, D11) with no source flag |
| `checksum mismatch: expected=<x>, actual=<y>` | `-cli-checksum` verify failed. This applies to `-cli-url` always, and to `-cli-zst` only when a checksum is supplied (D1) |
| `opening input: <err>` | `-cli-zst` read error |
| `decompressing: <err>` | bad zstd blob, for example `invalid input: magic number mismatch` |
| `decompressing: decompressed CLI exceeds <n> bytes` | D10 cap, opt-in |
| `download failed: response exceeds <n> bytes` | D10 cap on the download body, opt-in |
| `download failed: <transport err>` | `io.Copy` transport error, for example `read tcp …: connection reset by peer` |
| `download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)` | D12 download deadline, opt-in |
| `download stalled: no data for 60s after <got>/<total> bytes` | read-idle abort, meaning no bytes for 60 s on the `-cli-url` body (always-on, `4534d86` parity, VM-measured) |
| `mkdir cli dir: <err>` | cli-dir uncreatable |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli version "…" collides with the install temp sweep` | D7 hardening |
| `cli version "…" collides with the install download blob` | version starting `.blob-` |
| `clearing stale dir at <path>: <err>` | an occupied `cliPath` directory that claustrum cannot remove |
| `staging file vanished before install: <err>` | a concurrent sweep took the staging file |

Download progress and `fetch` stats came with `4534d86`, on the `-cli-url` path:
- While downloading, `-install` prints `__INSTALL_PROGRESS__<json>` lines to stdout
  on a ticker of about 1 s: `{"phase":"download","bytes":<n>[,"total":<m>]}`. A
  leading `bytes:0` line is always emitted. `total` carries the Content-Length and
  is dropped when the server sends none, as with a chunked body. The cadence is
  time-driven, so byte counts jump irregularly and there is no guaranteed final
  `bytes==total` line. A consumer treats these as progress, not as a byte-exact
  sequence.
- The `__INSTALL_RESULT__` facts line gains a `fetch` object LAST, after `cliError`:
  `{"bytes":<n>,"ms":<n>,"longestPauseMs":<n>}`. Those are bytes read, download
  duration, and the largest gap between reads. It appears whenever a `-cli-url`
  download was attempted, even a 0-byte 404. It is dropped on the `-cli-zst` path
  and on the cache-hit path.
- The `-cli-url` body has an always-on 60 s read-idle abort, reset on every byte,
  that fails the install with the `download stalled: …` cliError above. This is
  parity, not a divergence, because the reference does it. It is a read-idle bound,
  not a total deadline, so a slow-but-progressing download completes. That total
  cap is the opt-in D12, which is off by default.

Checksum and verify ordering:
- claustrum verifies `-cli-checksum` on the `-cli-url` path unconditionally. An
  empty checksum still fails.
- Verify happens BEFORE decompress. This is intentional divergence D13. It is
  always-on and unresolved. The reference decompresses first, and claustrum
  checksums first. A blob that is both undecompressable and wrong-checksummed
  diverges on the string. A short artifact yields `checksum mismatch` where the
  reference says `decompressing: unexpected EOF`. A genuine interrupted transfer
  never reaches the checksum on claustrum at all. claustrum answers
  `download failed: <transport>` there, where the reference answers
  `decompressing: <transport>`. Both binaries fail the install either way. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D13.
- The `-cli-zst` checksum is intentional conditional divergence D1. The reference
  never checksum-verifies the local SFTP-upload blob. claustrum verifies it only
  when a `-cli-checksum` is supplied, with the same `checksum mismatch` error, and
  it leaves the source blob intact. An absent or empty checksum stays trusting, so
  honest callers are byte-identical. See [`DIVERGENCES.md`](DIVERGENCES.md) → D1.

The opt-in wall-clock bounds are all three off by default, so a stock claustrum
applies none of them, on linux or anywhere. At the shipped defaults no
claustrum-chosen `-install` bound applies. Only the stdlib transport clocks
(`net.Dialer{Timeout:30s}`, `TLSHandshakeTimeout:10s`) apply, and only on
`-cli-url`. Off is parity, because the reference showed no deadline at the
durations probed. See [`DIVERGENCES.md`](DIVERGENCES.md):
- `-cli-download-timeout <dur>` is D12. `0` gives `http.Client{Timeout:0}`, which
  is no bound. When armed, it bounds the whole exchange. An honest download that is
  merely too slow therefore trips `download failed: context deadline exceeded (…)`
  as surely as a black hole does.
- `-cli-probe-timeout <dur>` is D11. `0` puts no deadline on the `<cli> --version`
  runnability probe, on every platform. When armed, a CLI slower than the deadline
  diverges. After extraction it diverges as `installed cli at <path> is not
  runnable`, and claustrum deletes the staged binary. On the cache-hit test it
  diverges as a silent reinstall. With `-cli-url` and a timely replacement it
  diverges as no `cliError` at all. It is a threshold, not a hang detector, so an
  honest-but-slow CLI trips it too. The cached binary survives every failure before
  the rename.
- `-libc-probe-timeout <dur>` is D14, and it is linux only. `0` puts no deadline on
  `ldd --version`. Off linux the probe never runs. On linux it can fire on any
  host. Since build 3ef9370 the libc probe runs `ldd` on every call, and
  the loader glob is only the empty-output fallback. Do not confuse it with
  `-cli-probe-timeout`. The two names differ only in
  their `cli` and `libc` prefix, they have the same type, and main's `-install` arm
  resolves them in consecutive statements. `TestInstallArmWiresEachFlagToItsOwnGlobal`
  pins that. `libc` build selection is a driver claim. See
  [ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance).

D10 is the opt-in size cap. `-max-cli-bytes <n>`, or the `max-cli-bytes`
configuration key, governs both the decompressed CLI and the download body. `0` is
off, which is parity, because the reference took a 600 MiB payload to the
runnability test. claustrum streams the blob and never buffers it. It keeps a path,
not a `[]byte`, so the staging retry can re-read it. "Cap off" therefore does not
mean unbounded memory. See [`DIVERGENCES.md`](DIVERGENCES.md) → D10.

`-cli-version` hardening is claustrum-only:
- D6 requires a single path component. The clearing step is an `os.RemoveAll`
  on `filepath.Join(cliDir, cliVersion)`. A version that escapes the cli-dir
  therefore deletes unrelated data. Measured, the reference destroys the target on
  `../victim`. `link/1.0.0` through an intermediate symlink also escapes. claustrum answers
  `cli version "…" must be a single path component` and touches nothing. It
  refuses `.`, `..`, `/` and `\` on every OS. claustrum uses a single-component
  test and not lexical containment, because containment accepts `link/1.0.0`, and
  `EvalSymlinks` adds a TOCTOU window. A final component that is itself a
  symlink stays legal, because `os.RemoveAll` unlinks it and does not follow it. The
  real client passes bare versions. `1.0.86`, `2.0.0-beta.1`, a commit sha,
  `latest` and `1.0.86+build.5` are all measured as accepted.
- D7 forbids a collision with the orphan sweep. The sweep claims `.fetch-*` and
  `*.zst`, and it runs after *every* attempted install. Without that rule, `-cli-version .fetch-x` or
  `1.0.zst` installs, and the sweep then deletes it moments later.
  Both binaries finish with an empty cli-dir and no `cliError`, and report a
  success that installed nothing. claustrum now answers `cli version "…" collides
  with the install temp sweep`. The sweep predicate and this test share one
  definition.

Staging and cleanup:
- claustrum stages the CLI at `<cli-dir>/.fetch-<random>`, mode `0600`, and
  renames it into place. It never stages at `<cliPath>.tmp`. This is one code path
  for `-cli-url` and `-cli-zst` alike. The orphan sweep matches `.fetch-*`, so it
  reclaims the litter of an interrupted install.
- When the cli-dir exists, a `-cli-url` download lands at
  `<cli-dir>/.blob-<random>`. On a first install it lands at
  `$TMPDIR/claustrum-fetch-<random>`,
  because `fetchToFile` in `install.go` runs before `ensureCLI` creates the
  directory. The `.blob-` prefix is deliberately different, so that the sweep and
  the `-cli-keep` prune do not claim an in-flight blob. That prune counts every
  non-directory as a version. That is also why claustrum refuses a `-cli-version`
  that starts with `.blob-`. The install removes the blob on every path. Only a
  SIGKILLed download leaves it behind. No frame changes either way.
- claustrum consumes the `-cli-zst` blob once decompression succeeds, and not
  only on a fully successful install. An extracted CLI that fails the runnability
  test still costs the blob. claustrum leaves a blob that is not valid zstd alone.
- claustrum clears an occupied `cliPath`, and that is not fatal. `rename(2)`
  refuses to replace a non-empty directory, so claustrum removes it first. It
  removes it only when `cliPath` is a directory. A regular file, which an
  installed CLI always is, is replaced atomically. If claustrum cannot remove it →
  `clearing stale dir at <path>: <err>`. If the staging file vanished →
  `staging file vanished before install: <err>`, and `cliPath` stays untouched. The
  end states match the reference for every destination shape: absent, a regular
  file, and a non-empty directory.
- The orphan sweep removes `.fetch-*` and `*.zst` entries with one `os.Remove`
  per entry. It therefore clears files and *empty* directories, and leaves a
  non-empty `.fetch-dir/`. Unrelated files survive. The sweep runs whenever an
  install was attempted, and the `-cli-keep` prune runs only on success. claustrum
  stages its extract in this same `.fetch-*` namespace and holds it across the
  probe, so a concurrent install can reclaim another install's staging file.
  claustrum handles that with a single retry of the stage-verify-rename step,
  and it does not narrow the sweep.
- claustrum runs `ldd` on every libc probe since build 3ef9370, and its output
  decides the answer. A "musl" banner reports `musl`, and any other output reports
  `glibc`. The `/lib/ld-musl-*.so.*` marker is consulted only when `ldd` produced
  no output.

### -probe-cli — classify a CLI binary

`-probe-cli <path>` runs the bounded `<path> --version` runnability probe and exits
`0`. It is how Claude Desktop classifies a CLI binary out of band, without a full
`-install` (reference build `19f30c46`). Its stdout is:

- If the CLI runs and exits 0 within the bound, stdout stays empty.
- If the deadline had to kill it, stdout is `__CLI_HUNG__\n`.
- If the binary is missing or does not run, stdout is `__CLI_BAD__\n`. Not running
  means it either fails to start or exits non-zero.

The bound is a fixed 30 s, always applied. It is not the opt-in
`-cli-probe-timeout` (D11), which bounds only the `-install` runnability probe and is
off by default. The mode unsets `CLAUDE_RPC_TOKEN` so the
probed child never inherits it. The probe runs in its own process group. On Unix the
fixed deadline group-kills the whole subtree, so a `--version` that forks a descendant
cannot outlive the probe. On Windows the direct child is killed and `WaitDelay` bounds
the mode, so a stray descendant is left to exit on its own. The mode installs no SIGINT
handler. A Ctrl-C therefore terminates the claustrum process itself, with exit 130 and
empty stdout. The probed CLI runs in its own process group, so a terminal Ctrl-C is not
delivered to it. The mode ignores
SIGPIPE, as `-install` does, so a stdout pipe the caller closes mid-write fails the
write with EPIPE instead of terminating the process.

### Behavior shared by every mode

- The default socket is `~/.claude/remote/rpc.sock`. When `-socket` is omitted, all
  modes fall back to it. If the parent directory is missing, `-serve` creates it
  with mode `0700`, so a bare `-serve` on a fresh machine works. `-bridge`
  and `-stop` do not create it. When no daemon has run, `-bridge` fails with
  `connect: no such file or directory`, and `-stop` prints `none`.
- With no mode given, claustrum prints
  `claustrum: one of --version/--install/--probe-cli/--serve/--bridge/--stop is required`
  on stderr and exits `2`, with no usage dump. An *unknown flag* gets the stdlib
  `flag` error plus the usage, and exit `2`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the `-install` facts schema and the
deployment lifecycle. See [`DIVERGENCES.md`](DIVERGENCES.md) for the full
divergence catalog and rules. See [EXAMPLES.md](EXAMPLES.md) for runnable
snippets.
