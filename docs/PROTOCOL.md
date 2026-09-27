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
- Two error replies skip the concurrent dispatch: the parse error (`-32700`)
  and the auth error (`-32001`). The daemon writes them on its read path,
  before it reads the next line. Every other reply goes through the
  concurrent dispatch. Measured against `f6010b97` and `90fca6e6` on a Linux
  VM, the reference did the same:
  - The parse error covers any line that does not decode into a request. A
    partial object, a line of only spaces, `[1]`, `123`, `"x"` and a batch
    array all get it. The frame is
    `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`,
    76 bytes with its newline.
  - The auth error covers a request with a missing or wrong `auth`. The
    lines `null`, `{}` and `{"jsonrpc":"2.0"}` get it too. The frame echoes
    the request `id`, re-encoded as in [Message shapes](#message-shapes). A
    `null` or missing `id` gives `"id":null`. An unauthenticated
    `server.shutdown` is not an auth error (see
    [Authentication](#authentication)).
  - The client can half-close its write side right after the line. These two
    frames still reach the wire before the close, with or without a final
    newline. A reply from the concurrent dispatch races the close.
  - A request whose handler blocks does not delay a later auth error on the
    same connection.
  - The daemon logs `[Server] Parse error: <json error>` or
    `[Server] Unauthorized request: method=…, id=…` before it writes the error
    reply. Measured against `f6010b97` and `90fca6e6` on a Linux VM, the
    reference logged the same parse-error text. When its reply write failed,
    the failure line came after the parse-error line. In a Go type-mismatch
    error, the type name differs from the reference.
  - At a client EOF the daemon logs `[Server] Connection closed: <addr>`, then
    closes the socket. Measured with strace on a Linux VM, `f6010b97`,
    `90fca6e6` and claustrum all log this line before the close, in 20 of 20
    runs each.
  - The daemon does not wait for a request in flight when the client
    half-closes. The probe sent a `files.read` of a pipe that returned data
    after 100 ms, 500 ms or 2 s. Measured against `f6010b97` and `90fca6e6`
    on a Windows VM, the reply was lost in 60 of 60 runs per build. A
    partial last line did not change that. claustrum lost the reply in 60 of
    60 runs too.
- Input that ends without a final newline. The daemon reads the last bytes
  before EOF as one more line, and the rules above apply. A tail of only `\r`
  is a blank line and gets no reply.
  - If an earlier valid line came first, the parse error for the tail and the
    reply to the earlier line race. The probe sent a `server.ping` line, a
    partial line, then a half-close, in 50 runs per build. Measured on a Linux
    VM, `f6010b97` and `90fca6e6` each sent the `server.ping` reply in 1 of 50
    runs. claustrum sent it in 4 of 50 runs. All three builds closed about
    2 ms after the half-close. On Linux, claustrum matches the reference.
  - On Windows this case has a measured timing difference, not parity.
    Measured on a Windows VM, `f6010b97` and `90fca6e6` each sent the
    `server.ping` reply in 50 of 50 runs, in either order. They closed about
    12 ms after the half-close. claustrum sent the reply in 21 of 50 runs and
    closed after about 2.4 ms. Every run on every build got the parse-error
    frame. The cause of the later close of the reference is not known.
  - At the idle close, the daemon also reads the last bytes as a line. It
    dispatches a valid request. It writes the error for a bad tail on the read
    path, but the connection is already closed, so that write fails. The wire
    carried 0 bytes on each measured build.
- Either end of an `AF_UNIX` connection on Windows can miss the other end's
  close. Measured on a Windows VM, a reader got the bytes before the EOF and
  then blocked. A new read returned the EOF. A plain blocking `WSARecv` loop
  missed it too. One claustrum `-bridge` run hung in this way until the probe
  killed it. One `90fca6e6` `-bridge` run also hung until the kill.

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
connect, unless it prints `survivor`. See the `-stop` section below.

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
| git.worktree_create | `git worktree add failed: <text>` | in `error`, `errorCode:"worktree_add_failed"`. `<text>` is git's stderr, made by the text rule in the method section. On `f6010b97` and on claustrum the text can start with git's graft-file deprecation `hint:` lines, because both set `GIT_GRAFT_FILE`. The 512-byte cap can then cut the rest. The rollback runs no git call and removes the leaf only if it is empty. A pre-existing branch is not deleted (`4534d86`). A failed add answers this frame even when the caller `timeoutMs` expired during the add (measured against `f6010b97` and `90fca6e6` on a macOS VM) |
| git.worktree_create | `git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)` | in `error`, `errorCode:"worktree_add_failed"`, when the attach add for `existingBranch` fails and the fallback `-b <branchName>` add fails too. The text rule makes each text on its own, each with its own 512-byte cap. Measured against `f6010b97` and `90fca6e6` on a macOS VM |
| git.worktree_create | `git worktree add failed (checkout): <text>` | in `error`, `errorCode:"worktree_add_failed"`, when the `read-tree` checkout fails. Same text rule. Where git prints graft-file deprecation `hint:` lines, the text starts with them on `f6010b97` and on claustrum. `90fca6e6` prints no hint. Apart from the hint, the frame matches `90fca6e6` byte for byte. The new directory and the branch the call created are removed. In attach mode the attached branch is kept (measured against `f6010b97`) |
| git.worktree_create | `git worktree add timed out after <n>ms (deadline expired {before the checkout started / during the checkout): <text> / after the checkout finished})` | in `error`, `errorCode:"timeout"`, from the caller-supplied `timeoutMs` (`4534d86`). An absent `timeoutMs`, or 0, arms no deadline. `<text>` comes from the stderr of the killed git, by the same text rule |
| git.worktree_create | `<frame>; and the undo could not finish for <leaf>: {the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll <entry>: <OS error>) / the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat <leaf base name>: <OS error>)}` | appended to the checkout-failure frame and to each `timeout` frame when a step of the rollback fails. The `errorCode` stays as it was. Measured against `f6010b97` and `90fca6e6` on a Windows VM |
| git.worktree_remove | `refusing to remove worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; …}` | in `error`, with no `errorCode`. This is `7d193f89` containment. The spelling refusal is Windows-only and comes before containment |
| git.worktree_remove | `refusing to remove worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, with no `errorCode`. It gates the delete fallback off a planted link (`7d193f89`) |
| git.worktree_remove | `refusing to remove worktree: <p> is locked (git worktree lock); unlock it to remove it` | in `error`, with no `errorCode`. `7d193f89` refuses a LOCKED worktree (`success:false`) and leaves it in place. The message is fixed whatever the lock reason is. Before `7d193f89` the reference deleted it through the fallback and answered `success:true`. |
| git.worktree_remove | `failed to remove worktree: "" does not name a directory` | in `error` (empty `worktreePath`) |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (its registrations could not be examined); retry` | in `error`, with no `errorCode`. Without `worktreeRoot`: the configuration of `baseRepo` cannot be read, `baseRepo` holds no repository, or the trust check refuses its git directory. With `worktreeRoot`: the worktree registry exists but cannot be read. Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: cannot determine the repository's work tree: <reason>` | in `error`, with no `errorCode`, with `worktreeRoot` only. `<reason>` is a trust refusal text, `exit status 128`, or the hooks refusal. Nothing is deleted. See the method section (`f6010b97`, Linux VM) |
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

#### Git-directory trust check

`f6010b97` added this check. Every `git.*` method runs it before git runs. The
check looks at the directory the method runs git in. That is `path` for
`git.info` and `git.list_branches`. It is `baseRepo` for `git.status`,
`git.worktree_create` and `git.worktree_remove`. Unless a rule names its OS, it is
measured side by side against `f6010b97` on a Linux VM, and on macOS and Windows VMs
where the case can exist. The check has three
outcomes. "Trusted" lets git run. "No repository" answers as if the directory held
no repository. "Refused" answers with one of the texts M1 to M5, and git does not
run.

The daemon finds the git directory as follows, when its own environment sets
neither `GIT_DIR` nor `GIT_COMMON_DIR`:

- The walk starts at the request directory with symlinks resolved, and goes up. A
  refusal names the resolved path, never the alias.
- A `.git` directory that passes the git-directory test is the git directory.
- A `.git` directory that fails the test ends the walk. If it holds a `commondir`
  entry of any type, it is the git directory, and M1 refuses it. Otherwise the
  answer is "no repository". Git itself walks on to an outer repository here, so
  this answer differs from git's own.
- A `.git` regular file starts with the exact bytes `gitdir:` and holds at most
  1 MiB. The rest is trimmed of white space. A relative target is taken relative to
  the directory that holds `.git`. Any other `.git` file means "no repository".
  `GITDIR:` in upper case is one example.
- A `.git` of another type, such as a FIFO, is skipped, and the walk goes on.
- With no `.git`, the directory itself is the git directory if it passes the test.
  That is a bare repository. Nothing found up to the root means "no repository".
- A request directory that does not exist is left to git.

The git-directory test has three parts. `objects` and `refs` exist, of any type.
`HEAD` passes. A regular `HEAD` passes when its first 255 bytes start with `ref:`, then
any spaces, tabs, CRs or LFs, then `refs/`. It also passes when it starts with 40
hex digits in either case. A symlink `HEAD` passes when its target text starts with
`refs/`. Any other `HEAD` fails at once. A FIFO `HEAD` is one example, and git
itself blocks on it.

The daemon then judges the git directory G:

- G is a linked-worktree entry when its parent is named `worktrees`, its own name
  is not `.git`, and its grandparent passes the git-directory test. The entries of
  a main repository, a bare repository and a submodule all count.
- G is not an entry and holds no `commondir`: trusted. This is a normal main, bare
  or submodule git directory.
- G is not an entry and holds a `commondir` of any type: M1. Git writes that file
  only in an entry. A `commondir` that names G itself is refused too. An entry whose
  grandparent fails the test is not an entry, so its honest `commondir` gets M1.
- G is an entry without `commondir`: M3.
- G is an entry whose `commondir` is not a regular file of at most 1048576 bytes:
  M4. A symlink is followed only when it is relative and stays inside the entry.
  Exactly 1 MiB passes.
- Otherwise the content is trimmed of spaces, tabs, CRs and LFs. It is taken
  relative to G when it is not absolute, and cleaned lexically. It has to equal the
  grandparent as a string. No symlink in it is resolved, so naming the repository
  through an alias fails. On Linux and macOS a backslash is an ordinary character.
  On Windows the comparison ignores letter case and slash direction. A `\\?\` prefix
  or a path without a drive letter still fails there. Any other value gets M2. That
  covers another repository, a missing path, an empty file and a NUL byte.
- A trusted git directory runs git with `GIT_COMMON_DIR` pinned to the repository
  git directory. For an entry that is the grandparent. For any other git directory
  it is that directory itself. White space around `../..` therefore passes the
  check, although git itself rejects that file. The frames that follow come from
  git, so they depend on the git version. The pin also keeps git from walking past a
  trusted nested `.git`. A nested `.git` whose `objects` is a regular file therefore
  answers "not a repository" from git on Linux and macOS. Git for Windows accepts
  that layout and answers for the nested repository.
- An entry directory that is gone means "no repository". On Linux and macOS an
  entry replaced by a regular file means the same. On Windows it gets M3.
- A `.git` file that names a plain directory, another repository's git directory
  or the main git directory is not refused. Git answers for itself there.
- On Windows the daemon does not expand 8.3 short names in G. A `.git` file or a
  daemon `GIT_DIR` can name an entry as `…\GIT~1\WORKTR~1\<name>`. That G is not
  an entry, because its parent is not named `worktrees`. Its `commondir` therefore
  gets M1, which names the short path. Measured on a Windows 11 VM. The daemon
  resolves G only when a component of it is a symlink or a junction.

The daemon's own environment changes the check as follows:

- A `GIT_DIR` names the git directory to judge, for every request directory, even
  a plain one. A relative value is taken relative to the request directory. A
  `GIT_DIR` that names a gone entry gets M5. On Linux and macOS a `GIT_DIR` that
  names a `.git` file means "no repository". On Windows that `.git` file is trusted
  and pinned to itself. Git then fails on the entry the file names, and `git.info`
  and `git.list_branches` answer `-32603 config-defined hooks could not be pinned
  off; git not run: listing the configuration in force: exit status 128: fatal: not a
  git repository: <entry>`. Any other `GIT_DIR` that does not exist is left to git.
  Git then runs with `GIT_COMMON_DIR` pinned to that `GIT_DIR`, unless the daemon's
  environment sets `GIT_COMMON_DIR`. A relative value is
  first joined to the request directory. On Windows `/dev/null` names nothing, so it
  becomes `<request dir>\dev\null`. Measured against `f6010b97` on Linux and
  Windows VMs.
- A `GIT_COMMON_DIR` turns the check off for `git.info` and `git.list_branches`.
  The other three methods still run it. `git.worktree_create` then prefixes the
  refusal with `git worktree add failed: cannot locate the repository's git
  directory: `.
- `GIT_CONFIG`, `GIT_CONFIG_PARAMETERS` and `GIT_CEILING_DIRECTORIES` do not change
  the check.

M1 to M4 start with `the repository's git directory could not be trusted; git not
run: commondir not as git writes it: `. Each `%q` is Go quoting of a path cut to
its first 300 runes. A non-ASCII path is cut by runes, not bytes. An invalid UTF-8
byte stays a byte and prints as `\xff`. The rest of the text is never cut.

| id | text after the prefix | operands |
|---|---|---|
| M1 | `%q exists where git itself never writes one (git keeps that file only in a linked worktree's entry under .git/worktrees/); remove it if you did not create it, and treat its appearance as tampering` | `<G>/commondir` |
| M2 | `%q does not name the entry's own repository, %q; restore the file to read ../.. or remove that worktree entry and add the worktree again` | `<entry>/commondir`, `<repo git dir>` |
| M3 | `worktree entry %q has none (git always writes one there, containing ../..); the entry is damaged or was not made by git — recreate the worktree with git worktree add, or remove the entry` | `<entry>` |
| M4 | `%q is not a small plain file (<reason>); remove it, or remove that worktree entry and add the worktree again so git rewrites it` | `<entry>/commondir` |

The M4 reasons seen include `commondir is not a regular file`,
`commondir is larger than 1048576 bytes`,
`openat commondir: path escapes from parent`,
`openat commondir: no such file or directory` or
`openat commondir: permission denied`. On Windows the OS text differs, for
example `openat commondir: The system cannot find the file specified.`.

M5 has no common prefix. It is `config-defined hooks could not be pinned off; git
not run: the worktree entry this folder's .git names no longer exists`.

| method | refused | no repository |
|---|---|---|
| `git.info` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"repoSlug":"","defaultBranch":""}` |
| `git.list_branches` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"branches":[]}` |
| `git.status` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"clean":false}` |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}` | `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}` |
| `git.worktree_remove` | without `worktreeRoot`, the lock-check refusal. With it, the work-tree refusal and `<text>` | without `worktreeRoot`, the lock-check refusal. With it, the work-tree refusal and `exit status 128` |

The lock-check refusal is
`{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`.
It has no `errorCode`, and the path is neither quoted nor cut. The work-tree
refusal is `{"success":false,"error":"failed to remove worktree: cannot determine the
repository's work tree: <reason>"}`, also with no `errorCode`.

When git cannot list the configuration in force, the method answers the hooks
refusal: `config-defined hooks could not be pinned off; git not run: listing the
configuration in force: <exec error>: <git text>`. claustrum makes `<git text>`
with the text rule of `git.worktree_create`. The rule takes git's raw stderr, keeps
its first 512 bytes, drops invalid UTF-8, turns each non-printable rune into one
space, and then trims spaces. A stub git on a Windows VM measured this frame against
`f6010b97`. The payloads were multi-line, CRLF, over 512 bytes, invalid UTF-8 and
control bytes. 40 newlines and 40 spaces before a long text showed that the cap
comes before the trim. The leading white space counts toward the 512 bytes. A tab
becomes one space on `90fca6e6` too (Linux VM). With the `GIT_COMMON_DIR` pin, git
names a corrupt config by its absolute path. `f6010b97` does the same in the
`git.info`, `git.list_branches`, `git.status`, create and remove frames, measured on
Linux and macOS VMs. `90fca6e6` names `.git/config`.

#### Hardened git calls

The git methods run most git steps as hardened calls. A hardened call carries a fixed
set of `-c` options and a fixed set of environment variables. This shape is off the
wire. The reply frames do not depend on it. A logging git wrapper measured each point
below against `f6010b97`. Each point names the VMs that measured it.

- Working directory. Each hardened call runs with the repository directory as its
  working directory, and it passes no `-C`. The checkout of `git.worktree_create`
  runs in the new worktree. The `git status` call runs in the worktree. Both name
  their directories with `--git-dir` and `--work-tree`. Linux, macOS and Windows VMs.
- Profiles. A call uses the light profile or the heavy profile. Each profile has its
  own `-c` options and its own variables. The light variables are, in this order,
  `GIT_ALLOW_PROTOCOL=https:ssh`, `GIT_TERMINAL_PROMPT=0`, `GIT_NO_REPLACE_OBJECTS=1`
  and `GIT_GRAFT_FILE=<null>`. The heavy variables are, in this order,
  `GIT_NO_LAZY_FETCH=1`, `GIT_ALLOW_PROTOCOL=denied_by_claude_ssh`, an empty
  `GIT_ASKPASS` and `GIT_TERMINAL_PROMPT=0`. Then comes `GIT_COMMON_DIR` when the
  trust check pins it. Then come the two hook pins, from `GIT_CONFIG_COUNT=2`.
  Linux, macOS and Windows VMs.
- Heavy calls. `git status` and `rev-parse --absolute-git-dir` use the heavy
  profile. Every other hardened call uses the light profile. Of the claustrum calls,
  only `git status` adds `GIT_OPTIONAL_LOCKS=0`. The checkout adds `GIT_INDEX_FILE`.
  Linux, macOS and Windows VMs.
- Null device. `<null>` is `/dev/null` on Linux and macOS, and `NUL` on Windows.
  The same value goes into `-c core.excludesFile` when the user has no global
  excludes file. The `-c core.hooksPath` and `-c core.attributesFile` values stay
  `/dev/null` on Windows. Windows VM. claustrum only: its `git status` call passes
  `-c core.excludesFile=/dev/null` on Windows too. That is divergence D16.
- Configuration listing. Before each hardened call except the first (see the next
  point), the daemon runs
  `git config -z --list --name-only` in the same directory. The listing carries the
  profile variables of the call after it, and `GIT_COMMON_DIR`. It carries no hook
  pins. The listing before the checkout and the listing before the `git status`
  call start with `--git-dir=<dir>`. Linux, macOS and Windows VMs.
- Hooks refusal check. The first listing of a method is the hooks refusal check. It
  is not an extra call. If it fails, the method answers the hooks refusal. `git.info`,
  `git.list_branches` and `git.worktree_create` run it with the light profile.
  `git.status` and `git.worktree_remove` run it with the heavy profile. Linux, macOS
  and Windows VMs. With `worktreeRoot`, `git.worktree_remove` runs a light check.
  Later a heavy listing and `rev-parse --absolute-git-dir` follow. Linux VM.
- Excludes read. Before its first listing, the daemon reads the user's
  `core.excludesFile` once, with `git config --includes --path core.excludesFile`.
  This call runs in the temporary directory. `GIT_DIR=<null>` is the only variable
  that it adds. Linux, macOS and Windows VMs.
- The probe `git --attr-source=<empty tree> version` adds no variable. Linux, macOS
  and Windows VMs.
- The `git status` call starts with `--attr-source=<empty tree>`. The heavy `-c`
  options, `--git-dir` and `--work-tree` follow it. Linux and macOS VMs. It and its
  listing pin `GIT_COMMON_DIR` to the clean path of the common git directory. On
  Windows the reference pins it with backslashes (Windows VM). claustrum cleans the
  path to match, and its Windows unit test checks the pin.
- Variable order. A `GIT_*` variable of the daemon's own environment keeps its
  place, before the variables that the daemon adds. If the daemon adds a variable
  that its environment already has, the added value and place win. Linux and macOS
  VMs. On Windows, the Go runtime of claustrum sorts the environment block by name.
  `f6010b97` does not sort it (Windows VM). Go's os/exec sorts it, and claustrum
  does not work around that.
- Temporary names. The temporary git dir of `git status` starts with
  `claustrum-git-dir-`. The temporary index directory of the checkout starts with
  `claustrum-gitidx-`. Each prefix has the length of the `f6010b97` prefix, 18 and 17
  bytes. Linux, macOS and Windows VMs.
- `git.worktree_remove` without `worktreeRoot`. If the hooks refusal check or the
  heavy `rev-parse --absolute-git-dir` after it fails, the check runs once more. That
  is a second listing, and a second `rev-parse` when that listing passes. Then the
  method answers the lock-check refusal. Linux and macOS VMs.
- `git.worktree_remove` with `worktreeRoot`. A light `rev-parse --show-toplevel`
  follows the light check, with no listing of its own. Before the daemon decides
  whether `worktreePath` is a registered worktree, it runs a light
  `worktree list --porcelain -z` and a heavy `rev-parse --absolute-git-dir`. Each
  of them has its listing. claustrum does not use the answers of these calls.
  Linux VM.
- Some calls of `f6010b97` have no claustrum counterpart. Examples are a
  `rev-parse --show-toplevel` in `git.worktree_create` and the plumbing calls of its
  `git status`. claustrum's `git.status` runs a light `rev-parse` in `path`, with its
  listing, that `f6010b97` does not run. The replies are the same.

#### git.info
`{path}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""}`

- Since `f6010b97` the git-directory trust check runs first, on `path`. A refused
  git directory answers `-32603` with the refusal text. "No repository" answers the
  non-repo body. A `GIT_COMMON_DIR` in the daemon's environment turns the check off
  for this method. See [Git-directory trust check](#git-directory-trust-check).
- The daemon reads `branch` with `branch --show-current`, so it works on an unborn
  HEAD. An empty repo gives the init branch name, for example `master`. A detached
  HEAD gives `branch:"detached:<short-sha>"`.
- When `branch --show-current` fails, the result has no `branch` member at all.
  git's error text never appears there. An example is a HEAD that git fails to
  resolve (`ref: refs/heads/main` or 40 hex digits, each followed by junk). Measured
  side by side against `90fca6e6` and `f6010b97` on a Linux VM.
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
- Since `f6010b97` the git-directory trust check runs on `baseRepo`, not on
  `path`. A refused git directory answers `-32603` with the refusal text, even when
  `path` is an honest linked worktree. "No repository" answers
  `{"isRepo":false,"clean":false}`. Damage inside the linked worktree's own entry
  does not trigger the check. A `GIT_COMMON_DIR` in the daemon's environment does
  not turn the check off here. See
  [Git-directory trust check](#git-directory-trust-check).
- Status honours replace objects (`refs/replace`), as the reference does. A
  replaced `HEAD` commit therefore shows in `changes`.
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
  the status. With Git for Windows 2.55.0 the cause is the
  `-c core.excludesFile=NUL` of the reference's status call. The reference passes
  `NUL` when the user has no global excludes file. `git status` exits 128 on that
  value. Claustrum passes `/dev/null` there instead. This is a reachable, always-on Windows divergence, and
  claustrum is more correct. See [DIVERGENCES.md](DIVERGENCES.md) D16.
- Every line is verbatim, the first one included. The daemon splits on the trailing
  newline only, so entry 0 keeps its leading space. `[" M a1"," M a2"]` returns
  `[" M a1"," M a2"]`. 5db5e4a lost entry 0's leading space. 7d193f89 and 4534d86 do not.
- A failing git → `-32603` that carries the Go error string (`exit status 128`, not
  git's `fatal:` text). With opt-in D5 the same `-32603` can carry `signal: killed`.

#### git.list_branches
`{path}` → `{"isRepo":true,"branches":[…sorted…]}`
- Non-repo → `{"isRepo":false,"branches":[]}`.
- Since `f6010b97` the git-directory trust check runs on `path`. A refused git
  directory answers `-32603` with the refusal text. "No repository" answers
  `{"isRepo":false,"branches":[]}`. A `GIT_COMMON_DIR` in the daemon's environment
  turns the check off for this method. See
  [Git-directory trust check](#git-directory-trust-check).
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
- If the attach add fails, the daemon runs
  `worktree add --no-track --no-checkout -b <branchName> <path> [<sha>]` and goes
  on. The fallback add gets the start commit that `sourceBranch` resolved to, as
  the new-branch path does, and the checkout then reads that commit. With no start
  commit, the add gets no start point, and the checkout reads
  `refs/heads/<branchName>`. `branch` is `<branchName>`. A rollback after this
  fallback deletes `<branchName>`, because the call created it. The natural trigger
  is an `existingBranch` that is checked out in `baseRepo`. With
  `existingBranch:"main"` and no `sourceBranch`, the reply is
  `{"success":true,"path":"<p>","sourceBranch":"main","branch":"<branchName>"}`.
  If the fallback add also fails, the reply is
  `{success:false,error:"git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)",errorCode:"worktree_add_failed"}`.
  Measured against `f6010b97` and `90fca6e6` on a macOS VM. There, `90fca6e6`
  passes the ref name as the start point and `f6010b97` the full id. The new
  branch lands on the same commit, and claustrum passes the full id.
- No fallback runs when the failed attach add deleted the leaf or put a new
  directory in its place. The daemon tests the leaf's identity for that. The reply
  is then `git worktree add failed: <attach text>`, and the rollback of a failed
  add runs. Measured against `f6010b97` and `90fca6e6` on macOS and Windows VMs.
  On Linux and Windows VMs both references held the leaf and its parent open
  during the create. On ext4 their replacement leaf got a new inode in 12 of 12
  runs. Claustrum holds both open until it answers, so a replacement cannot reuse
  the inode of the leaf. On Windows both claustrum handles share delete. Under the
  handles of `f6010b97`, the leaf can be renamed. A rename of the parent fails with
  "Access is denied." while the leaf is inside it. Claustrum takes the leaf's
  identity from its handle.
- Since `f6010b97` the git-directory trust check runs on `baseRepo`, after the
  managed-worktrees test and before anything is created. A refused git directory
  answers `{success:false,error:<text>,errorCode:"worktree_add_failed"}`. "No
  repository" answers `not_a_repo` as below. Either way the daemon creates no
  worktree directory, no entry and no branch. When the daemon's environment
  carries `GIT_COMMON_DIR`, the text is
  `git worktree add failed: cannot locate the repository's git directory: <text>`.
  See [Git-directory trust check](#git-directory-trust-check).
- The resolved repo is not git → `{success:false,error:"not a git
  repository",errorCode:"not_a_repo"}`. The daemon tests this before the add.
- This method ignores replace objects and grafts. The checkout holds the real blob
  and the real commit, and ancestry is the real ancestry, even when `refs/replace`
  or `info/grafts` name others. This is measured against `f6010b97`. claustrum
  runs every git step of this method with `GIT_NO_REPLACE_OBJECTS=1` and
  `GIT_GRAFT_FILE=<null>`, except `rev-parse --absolute-git-dir`. See
  [Hardened git calls](#hardened-git-calls).
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
  repo. A `worktreePath` with a trailing slash, `//` or `/./` succeeds too. `path`
  and each undo text below quote `worktreePath` exactly as sent. Measured against
  `f6010b97` and `90fca6e6` on Linux and macOS VMs.
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
  other files (for example \"<name>\"); … must start out empty …"`). These two
  tests take `<directory>` from the cleaned `worktreePath`, so for `R/proj/w1/`
  the refusal names `R/proj`. With `worktreeRoot`, the "already exists" refusal
  also quotes the cleaned path, for example `R/cp/w1` for `R/cp/w1/`. Measured
  against `f6010b97` and `90fca6e6` on Linux and macOS VMs. On success the
  daemon writes a 285-byte `.claude-managed-worktrees` marker at the `<directory>`
  level. Independently, a `baseRepo` that itself sits under a managed-worktrees
  marker is refused `{success:false,error:"baseRepo is inside a managed worktrees
  directory …",errorCode:"nested_base_repo"}`.
- Other failure → `{success:false,error:"git worktree add failed: <text>",errorCode:"worktree_add_failed"}`.
  `<text>` is git's stderr, made by the text rule below. On `f6010b97` and on
  claustrum the text can start with git's graft-file deprecation `hint:` lines.
  Both set `GIT_GRAFT_FILE`, and git prints the hint when it reads that file.
  `90fca6e6` prints no hint. One `90fca6e6` example is
  `"git worktree add failed: Preparing
  worktree (new branch 'dup') fatal: a branch named 'dup' already exists"`. A
  failed add answers this frame even when the caller `timeoutMs` expired during the
  add.
- After a failed add, the daemon runs no git call. It removes the leaf only if the
  leaf is an empty directory. Files that the failed add left in the leaf stay, and
  so do a registration and a branch that it made. A retry at the same path then
  answers `unsafe_path` "already exists". Measured against `f6010b97` and
  `90fca6e6` on macOS and Windows VMs, with a stub git that failed after it wrote
  into the leaf. The common failures, such as a branch that already exists, leave
  the leaf empty, so a retry with a fresh branch succeeds. If the failed add
  replaced the leaf's parent and made a new empty leaf in it, the new leaf stays.
  The daemon tests the identity of the parent that it holds open for that. Both
  references kept the new leaf after a failed attach add, in 20 of 20 runs on
  Linux and macOS VMs.
- The text rule makes every git text in the failure frames of this method. That
  covers the add failure, each part of the attach-fallback frame, the checkout
  failure and the checkout that the deadline killed. Measured against `f6010b97`
  and `90fca6e6` on a macOS VM:
  1. Take stderr only. stdout is not quoted.
  2. Keep the first 512 bytes.
  3. Drop every byte that is not valid UTF-8, anywhere in the text. The cap comes
     first, so the bytes of a rune that the cap cut go too.
  4. Replace each rune that is not printable with one space, with no collapsing.
     So `\r\n` gives two spaces. The measured set includes `\t`, NUL, `\x7f`,
     U+0085, U+00A0, U+200B and U+2028. claustrum uses Go's `unicode.IsPrint`,
     which fits every measured payload. That is an inference, not a proof.
  5. Trim the spaces at both ends.
  6. If the result is empty, use the exec error. That is `exit status 128` for a
     git that failed with that status, and `signal: killed` for a killed checkout.
     On Windows the killed checkout gives the kill's own error, `exit status 1`.
- `timeoutMs` is caller-supplied and was added by `4534d86`. It is a per-request
  deadline in milliseconds over the add, the checkout and the copy step. An absent
  `timeoutMs`, or `0`, arms no deadline, so the reply is byte-identical to the
  default. A fired deadline answers
  `{success:false,error:"git worktree add timed out after <n>ms (…)",errorCode:"timeout"}`.
  The rules below were measured against `f6010b97` and `90fca6e6` on a macOS VM,
  except where a rule names another build.
  - The deadline does not kill `git worktree add`. The daemon waits for the add to
    exit. If the add failed, the reply is the add-failure frame above, not
    `timeout`. If the add succeeded and the deadline expired, the parenthetical is
    `deadline expired before the checkout started`. The reply thus waits for the
    add. In attach mode the daemon runs the fallback add first, as described
    above, and then tests the deadline.
  - The deadline kills the checkout, a `read-tree`. The parenthetical is then
    `deadline expired during the checkout): <text>`. The text rule above makes
    `<text>` from the stderr of the killed git. With `f6010b97` and with
    claustrum, git can print graft-file `hint:` lines on stderr before the kill,
    and `<text>` then holds them. `90fca6e6` prints no hint.
  - The deadline does not kill the copy step that seeds the new worktree. The
    daemon lets the step finish and then tests the deadline. If it expired, the
    parenthetical is `deadline expired after the checkout finished`. The reply
    thus waits for the copy step.
  - The checkout git can exit 0 while a descendant it left, such as a smudge or
    hook filter, holds one of the daemon's output pipes. The daemon caps that
    drain at a fixed ~5s from git's exit, independent of `timeoutMs`. That cap is
    measured against `4534d86`. At the cap the daemon reaps the descendant. The
    checkout then counts as finished, so the copy step runs and the deadline test
    after it decides. When `timeoutMs` exceeds the drain, the reply is
    `{success:true}`. When it does not, the reply is the `timeout` frame with
    `deadline expired after the checkout finished`.
  - These timeouts roll back as a failed checkout does. See the failed-checkout
    and undo rules below. No rollback runs `git worktree remove`, so the
    `.git/worktrees/` directory stays. A retry at the same path then succeeds, with
    the same `branchName` or a new one.
  - This is caller-activated. It is distinct from the operator-global
    `-git-timeout` divergence (D5), and it applies to create only, not to
    `git.worktree_remove`.
- A non-empty `sourceBranch` picks the start commit of the new branch. The rules
  below were measured side by side against `f6010b97` on Linux, Windows and
  macOS. Here `s` is the value as sent.
  - The daemon resolves two candidates. L is `refs/heads/<s>^{commit}`. R is
    `refs/remotes/origin/<s>^{commit}`. Each is plain string concatenation, so a
    revision suffix such as `feat~1` works, and a slash name such as `team/feat`
    works. A symbolic ref is followed, so `s = "HEAD"` reads
    `refs/remotes/origin/HEAD`. An annotated tag object in the origin ref is peeled
    to its commit. An origin ref that holds a missing object, a tree or garbage,
    or a local ref that holds a missing object, counts as absent. `s` is tried
    only under `refs/heads/` and `refs/remotes/origin/`.
  - Only the remote-tracking namespace `origin` is read. The remote's
    configuration does not matter. The daemon fetches nothing, so a stale tracking
    ref is used as it is.
  - On a case-insensitive file system, a loose ref matches `sourceBranch` in any
    letter case, and a packed ref does not (Windows, macOS). On macOS a loose ref
    under `Origin` also counts.
  - Only one candidate resolves → that one.
  - Both resolve, and `git merge-base --is-ancestor <L> <R>` exits 0 (L equals R or
    is behind it) → R.
  - Otherwise the daemon runs `git merge-base <R> <L>`. If it fails (no common
    history, a shallow cut, a missing parent commit) → R.
  - Otherwise the daemon runs `git diff --quiet --no-ext-diff --no-textconv
    --submodule=short <merge base> <L> -- ':(top,icase).claude'
    ':(top,icase).mcp.json'`. Exit 0 → L. Any other exit → R. So a local change to
    the repo-root `.claude` entry or the root `.mcp.json`, in any letter case,
    selects R. So does a diff that errors. The test is the net tree difference from
    the merge base. It is not the commit history, and it is not a comparison with
    R. A nested `sub/.claude`, a `.claude.json` or a `.claudex` directory does not
    count. Uncommitted state in `baseRepo` does not count.
  - The add gets the chosen commit's full id:
    `worktree add --no-track --no-checkout -b <branchName> <path> <sha>`. The
    checkout reads the same id. The new branch's reflog therefore reads
    `branch: Created from <sha>`. The branch gets no upstream configuration.
  - `sourceBranch` is echoed exactly as sent, whichever candidate was used.
  - Neither candidate resolves → the same result as an omitted `sourceBranch`.
  - `existingBranch` is resolved after these steps. When it attaches, the chosen
    commit goes unused, and `sourceBranch` is still echoed.
  - These git steps have no deadline of their own. With `-git-timeout` (D5) opted
    in, a killed step counts as a failed step under the rules above.
- `sourceBranch` omitted or `""` → origin is not read. A non-empty `sourceBranch`
  that resolves to nothing reads both candidates first. In each of these cases the
  add gets no start point, so the new branch starts at HEAD, and its reflog reads
  `branch: Created from HEAD`. The daemon echoes the current branch from
  `rev-parse --abbrev-ref HEAD`. On a detached HEAD the result omits
  `sourceBranch`. That is measured against `f6010b97` with `sourceBranch` omitted
  and with a `sourceBranch` that resolves to nothing. Since `7d193f89`, on an
  unborn HEAD the add fails with `worktree_add_failed`.
- A failed checkout (`read-tree`) fails the request with
  `{success:false,error:"git worktree add failed (checkout): <text>",errorCode:"worktree_add_failed"}`.
  The text rule above makes `<text>`. Where git prints graft-file deprecation
  `hint:` lines, the text starts with them on `f6010b97` and on claustrum.
  `90fca6e6` prints no hint. Apart from the hint, the frame matches `90fca6e6`
  byte for byte. The daemon removes the new directory,
  the branch that the call created and its reflog, and the worktree's registration
  in the main repository's `.git/worktrees/`. The only git call in the rollback is
  `update-ref --no-deref -d refs/heads/<branchName>`. The `.git/worktrees/`
  directory itself stays, so the first linked worktree's failed checkout leaves it
  empty. With a linked worktree as `baseRepo`, that worktree's own entry stays,
  and a retry at the same path fails the same way. In attach mode the attached
  branch is kept. These end states are measured against `f6010b97`.
- The checkout is `read-tree -u --reset --no-recurse-submodules <rev>`, after the
  hardening `-c` options and `-c core.splitIndex=false -c core.commitGraph=false`.
  It runs with the new worktree as its working directory, and it passes no `-C`.
  `--git-dir` names the git dir of `baseRepo`, and `--work-tree` names the new
  worktree. For a linked-worktree `baseRepo`, the git dir is that worktree's own
  admin dir. The daemon gets it with `rev-parse --absolute-git-dir` before the add.
  The index goes to a file in a new temporary directory, named by `GIT_INDEX_FILE`.
  Its config precursor, `--git-dir=<git dir> config -z --list --name-only`, runs in
  the new worktree too. Measured against `f6010b97` and `90fca6e6` on a Windows VM.
  After git exits 0, claustrum moves that index into the worktree's registration
  and removes the temporary directory. The registration of a reference create
  holds an `index` file too. A process that the checkout leaves behind starts in
  the new worktree. On Windows it then blocks the removal of the leaf, and the
  rollback reports it with the undo text below.
- Every rollback after a successful add runs three steps. This covers the checkout
  failure and each `timeout` frame. The steps and texts were measured against
  `f6010b97` and `90fca6e6` on a Windows VM:
  1. Delete the entries at the top of the leaf, one at a time, in the order that
     the directory read returns them. The names are not sorted. Stop at the first
     entry that cannot be deleted. Then append `; and the undo could
     not finish for <leaf>: the worktree directory, its registration, and the
     branch all remain; remove them by hand before retrying (RemoveAll <entry>:
     <OS error>)` to the error, and undo nothing else.
  2. Delete the registration and the branch that the call created.
  3. Remove the leaf directory, which is now empty. If that fails, append `; and
     the undo could not finish for <leaf>: the worktree directory remains
     (re-populated while undoing?); remove it by hand before retrying (removeat
     <leaf base name>: <OS error>)`.

  The `errorCode` does not change. `<leaf>` is `worktreePath` exactly as sent, and
  `<entry>` is a name at the top of the leaf. With a trailing slash on
  `worktreePath`, the `removeat` part still names the base name, such as `w1`. The
  step 1 order was measured against both references on Linux ext4 and macOS APFS
  VMs. The two wordings are fixed, and only `<OS error>` varies. On Windows the measured causes were an open handle, a
  process with its working directory in the leaf, and a running executable. An
  ACL that denies the delete and a file name with a trailing dot were causes too. On Linux and macOS
  claustrum gives the same wordings with the OS error text of Go, for example
  `permission denied`. Both references gave the same text on Linux and macOS VMs.

Worktree population works as follows. `git worktree add` checks out tracked files
only, so the daemon then seeds the new worktree. The copies are best-effort, and a
failure never fails the request. A caller `timeoutMs` that expires before the
copies end still fails it, as `timeoutMs` above describes:
- `.worktreeinclude` sits at the repo root and uses `.gitignore` syntax. It is an
  include filter over the git-ignored set. The daemon copies an untracked file only
  when the manifest names it and git's standard rules ignore it. A manifest match
  that git does not ignore is not copied. The manifest must be a regular file. A
  symlink or a directory copies nothing and runs no git. An empty regular file
  still runs `git version` and the scan, and it copies nothing. git reads a temp
  copy of the manifest bytes. The rules in this bullet and the next three were
  measured against `f6010b97` on a macOS VM, except the rows named below. A
  Linux VM re-checked the version parse, opening rules, counts, batches and
  error arms. A Windows VM measured the Windows batch budget. The prefix rule
  for one glob segment, with its case folding, was measured on Linux, macOS and
  Windows VMs. So was the literal form after a leading `/`. Linux and Windows
  VMs measured the glob after a leading `/`.
  They also measured `build//`, `build//a.txt`, `BUILD//` and a lone `\` that
  ends the first segment. The other `//` and `\` rows were measured on a Linux
  VM only.
- If the manifest is a regular file, the daemon runs `git version`. The call has no `-c`
  option and no `-C`. It runs in the daemon's working directory, with the
  daemon's environment unchanged. The first `git version ` in the output counts,
  even after other text. A digit must follow it. Git 2.32.0 or later gets the
  directory scan. Older git gets the full scan, and so does output that does not
  parse. A non-zero exit also gets the full scan, even with valid output. Major
  and minor compare as numbers. A number too large for an int counts as very
  large. Text after the numbers is ignored, so `2.32.0.windows.1` gets the
  directory scan.
- The full scan runs
  `git ls-files --others --ignored --exclude-from=<manifest copy> -z -- ':(exclude).claude/worktrees'`.
  `git check-ignore --stdin -z` then keeps the paths that git's standard rules
  ignore. If either call fails, nothing is copied. The full scan searches every
  ignored directory.
- The directory scan first lists the ignored entries with
  `git ls-files --others --ignored --exclude-standard --directory`. If the listing
  fails, nothing is copied. Every ignored file in the listing is a candidate. An
  ignored directory is searched only when the manifest opens it. An any-depth
  pattern therefore does not reach a file in a closed directory: `*.txt` does not
  copy `build/a.txt` when git ignores `build/`. The opening rules follow:
  - A literal name of one segment opens each listed directory that has the name
    as any of its segments. `build` opens `build`, `sub/build` and `build/x`. So
    does `**/` and then one literal. Case does not matter.
  - One segment after a leading `/` opens each listed directory whose first
    segment matches it as a glob. `/build/` opens `build`, not `sub/build`.
    `/a?b/` opens `aXb` and `a_b`, not `ab`. The prefix rule does not apply
    here. A `\` escapes the next character, so `/ab\q/` opens nothing. Case
    does not matter.
  - One glob segment without a leading `/` opens by its literal prefix. A glob
    segment holds `*`, `?`, `[` or `\`. The prefix ends before the first of
    these characters. The segment opens each listed directory whose path starts
    with the prefix. The rest of the segment is not used. Case does not matter.
  - So `b*` opens `build`, not `sub/build`. `su*` opens `sub/build`.
    `a?b/` and `a[_]b/` open `ab`, `a b` and `a/c`. `sub\build/` opens `sub`,
    `sub/build` and `subbuild`. `AB\q/` opens `ab`.
  - `**/` and then a literal and more segments matches the rest of the pattern
    from any segment of a listed directory. `**/sub/build/` opens `sub/build`.
  - A pattern of two or more segments opens each listed directory that it matches
    segment by segment, and the listed parents of that directory.
  - In such a pattern, a segment that ends in a lone `\` matches any name. So
    `sub\/x/` opens each listed directory of one segment and each listed
    `<name>/x`. Git reads that line as `sub/x/`. This holds for the first, a
    middle and the last segment. An even run of `\` at the end of a segment is
    a literal `\`, so `zz\\/x/` opens only `zz\/x`. A run of three acts like a
    run of one. Directly after `**/`, such a segment opens nothing.
  - A `//` in a pattern leaves an empty segment. `build//`, `build//a.txt` and
    `BUILD//` open `build`, not `sub/build` or `a/x/build`. `build//` alone
    opens no dot directory. The empty segment matches no name, so `//x` opens
    nothing and `a//b` opens only a listed `a`.
  - One segment without a leading `/` that starts with `*`, `?`, `[` or `\` has
    an empty prefix. It opens no directory. Neither does `**/` and then a glob. A negation and a
    comment open nothing too.
  - Git's own match still reads a `\` as an escape. The manifest goes to git
    unchanged. So `a\ b/` opens `ab`, but git copies from `ab` only the files
    that another manifest line matches. Git does not read `\` as a separator.
  - In a pattern of two or more segments, a `\` does not cut a prefix.
    `sub/b\q/` opens only `sub`.
  - Only the first 256 counted patterns can open a directory. A negation counts.
    Blank lines, comments and patterns over a cap do not count. A line of only
    tabs and spaces is blank. So is a line that is empty after one leading and one
    trailing `/` are removed, such as `//`.
  - A pattern opens nothing if it has more than 1024 bytes after one leading and
    one trailing `/` are removed. It also opens nothing if it has more than 32
    segments.
  - An any-depth pattern also opens the listed dot directories. A pattern of one
    segment is any-depth, unless it starts with `/`. A pattern that starts with
    `**/` is any-depth too. The 256 count does not apply to this. At most 128 dot
    directories open, in listing order. The root `.claude/` takes a place unless
    an explicit pattern matches it. The flag never opens these 14 names, in any case. They
    take no place: `.angular`, `.cache`, `.dart_tool`, `.gradle`, `.next`,
    `.nuxt`, `.parcel-cache`, `.pnpm-store`, `.svelte-kit`, `.terraform`, `.tox`,
    `.turbo`, `.venv` and `.yarn`. An explicit pattern still opens a skipped
    directory, or one past the 128th. A dot directory that an explicit pattern
    matches takes no place. The root `.claude/` and `.claude/worktrees/` never
    open, even when a pattern names them.
  - The candidates go to `git ls-files --exclude-from=<manifest copy>` in batches.
    Files and directories never share a batch. A directory pathspec has no
    trailing `/`. A batch fills in listing order. When the next path does not
    fit, a new batch starts. Each argument after the `-c` options costs its
    length plus 3 bytes. That covers the fixed `ls-files` arguments, the
    `--exclude-from` argument and the paths. One call costs at most 131 072
    bytes on Linux and macOS, and at most 24 576 bytes on Windows. The `.claude/`
    pass below uses the same budget. Each value is a fit to the measured batch
    counts. It is not a value read from the reference. The `-c` options do not
    count in claustrum. Whether `f6010b97` counts them was not measured. On Linux and macOS, every value from 131 070 to
    131 073 fits the directory batches. On Windows, a one-byte bisection pins
    24 576 from both sides. On a Windows VM, a command line of 32 412 characters started, and
    one of 36 012 characters did not. The temp file name starts with a 27-byte
    prefix, the same length as in the measured `f6010b97` argv. A random
    decimal suffix follows it. The measured suffix had 8 to 10 digits in both
    daemons.
    A failed batch is skipped, and the other batches are still copied.
  - Only the paths from the directory batches go to `git check-ignore --stdin -z`.
    It keeps the paths that git's standard rules ignore. The file candidates are
    copied without it. If it fails, the directory paths are dropped, and the file
    candidates are still copied.
  - If the ignored files of the listing total more than 1 MiB, the full scan runs
    instead. Each file counts as its path length plus 3 bytes.
- A nested repository inside an ignored directory is not copied, and no empty
  directory is left for it.
- `.claude/` is copied separately, with no manifest entry. A second pass runs
  `git --literal-pathspecs ls-files --others --ignored --exclude-standard -z --`
  with one pathspec for each child of `.claude/`, such as `.claude/settings.json`.
  It leaves out `worktrees` in any case. If no other child exists, the pass runs
  no git. `f6010b97` names the same children on Linux, macOS and Windows VMs. The
  case rule was measured on Linux only. The pathspecs go into batches in the
  order of the directory read, one git call for each batch. The budget is the
  budget of the directory batches above. The fixed arguments cost 87 bytes. So
  the pathspecs of one call cost at most 130 985 bytes on Linux and macOS, and
  at most 24 489 bytes on Windows. A Linux VM and a Windows VM measured these
  split points to the byte against `f6010b97`. The largest rows had 2 400
  children on Windows and 30 000 on Linux. On
  macOS the `.claude/` batches were not measured. Before each batch, the daemon
  runs `git config -z --list --name-only`, as it does before most hardened
  calls. `f6010b97` also makes that call before each batch. A failed batch is
  skipped, and the other batches still copy. The pass copies what git lists, minus
  the exclusions in the bullets below. A `.claude/` the repo
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
- An opted-in `-git-timeout` (D5) that kills a git call loses what that call
  gives the pass. A killed listing loses the pass, and a killed batch loses that
  batch. A killed `git check-ignore` loses the whole full scan, or the directory
  paths of the directory scan. A killed `git version` selects the full scan.
  The reply is still `{"success":true}`, so the loss is silent and
  wire-invisible. Each git call has its own deadline, so the manifest copy can
  succeed while `.claude/` files are lost, or the other way round. The `.claude/` pass has no manifest
  precondition. It runs git on every create where `.claude/` holds a child other
  than `worktrees`. This is off by default.

#### git.worktree_remove
`{baseRepo,worktreePath[,branchName][,worktreeRoot]}` → `{"success":true}` (lenient)

- Since `f6010b97` the git-directory trust check runs on `baseRepo` only, before
  git runs. Without `worktreeRoot` it runs after the containment tests and the
  `.claude` look below. A refused git directory and "no repository" both answer the
  lock-check refusal:
  `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`.
  After a refusal the daemon deletes nothing. The worktree directory, its entry and
  its branch all stay. A `GIT_COMMON_DIR` in the daemon's environment does not turn
  the check off here. Damage to the entry of the worktree being removed does not
  stop the removal. See [Git-directory trust check](#git-directory-trust-check).
- With `worktreeRoot`, the daemon determines the repository's work tree after the
  containment checks and before the dir-symlink check. A failure answers
  `{"success":false,"error":"failed to remove worktree: cannot determine the
  repository's work tree: <reason>"}`, with no `errorCode`, and nothing is deleted.
  It comes before the "already gone" success, so a missing worktree gets it too.
  The reasons, in order:
  1. Git cannot start in `baseRepo`, because it is a regular file or a directory
     without search permission (mode 0600). `<reason>` is the hooks refusal with the
     start error, for example `…listing the configuration in force: fork/exec
     /usr/bin/git: permission denied` or `…: not a directory`. The path is the git
     that `PATH` resolves.
  2. The trust check refuses the git directory of `baseRepo`. `<reason>` is the
     refusal text M1 to M5. A plain folder as the target gets this text too.
  3. The trust check finds no repository. `<reason>` is `exit status 128`. Examples
     are a plain directory, an empty nested `.git` directory, a `.git` file with no
     `gitdir:` line, and a linked worktree whose entry is gone. A `.git` file whose
     target is gone and is not a worktree entry goes on to the next step instead.
  4. Git cannot list the configuration. `<reason>` is the hooks refusal text. The
     `.git` file with a gone target gets `…exit status 128: fatal: not a git
     repository: <target>` here.
  5. `baseRepo` is itself a git directory, such as a bare repository or a `.git`
     directory. `<reason>` is `exit status 128`. A repository whose config sets
     `core.bare` still passes.
  6. The hardened `git rev-parse --absolute-git-dir` fails in `baseRepo`. `<reason>`
     is its exec error, for example `exit status 128`. A daemon `GIT_DIR` that names
     nothing is one example.

  A `baseRepo` that does not exist skips every reason. One that is not a directory
  gets reason 1. Each reason and the order around this step were measured side by
  side against `f6010b97` on a Linux VM.
- With `worktreeRoot`, after the dir-symlink check, a worktree registry that exists
  but cannot be read (mode 0000) answers the lock-check refusal. This holds for a
  plain folder and for a registered worktree. A worktree that is already gone still
  answers `{"success":true}`. Measured against `f6010b97` on a Linux VM.

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
  regular `gitdir:` pointer file naming `<git dir>/worktrees/<name>` whose own
  record points back at `<p>`. `<git dir>` is the repository git directory that the
  trust check pinned for `baseRepo`. For a main repository that is
  `<baseRepo>/.git`. For a linked worktree it is the main repository's git
  directory. For a subdirectory of a repository and for a submodule it is that
  repository's git directory. With a daemon `GIT_DIR` it is the directory that
  `GIT_DIR` names. `f6010b97` removes a worktree through a linked-worktree
  `baseRepo`, and deletes the worktree, its entry and its branch (Linux VM). It does
  the same when the main repository holds a stray `commondir`. The check judges the
  entry of the linked worktree, and the main git directory is only the pin. Any
  other path is refused and LEFT IN PLACE, with
  `{"success":false,"error":"refusing to remove worktree: <p> is not a worktree of
  <repo> (<reason>), so it is left in place; remove it by hand if it is a
  leftover"}`. `<reason>` is one of `<p> has no .git file`, `<p>/.git is not a
  regular file`, `<p>/.git does not name a git dir`, `<p> carries a .git file that
  does not name this repository's own worktree admin directory`, or `<p> carries a
  .git file naming an admin directory whose own record is of a different worktree`.
  When `baseRepo`'s worktrees directory does not exist, the daemon cannot decide,
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
  create at the same path succeeds. When `baseRepo` is a linked worktree, the entry
  lives under the main repository's git directory, and the prune looks there. Git
  records the worktree's resolved path in the entry. After the fallback delete the
  worktree is gone, so the daemon resolves its path through the nearest existing
  parent. A remove named through a symlinked root, such as macOS `/tmp`, therefore
  still prunes the entry. Measured against `f6010b97` on a macOS VM. The
  same holds on Windows after the
  recursive-delete fallback. There the entry's record of the worktree uses forward
  slashes, and the daemon compares it without regard to slash direction or letter
  case. Measured against `f6010b97` on a Windows 11 VM. Before `7d193f89` neither binary pruned on the
  fallback path, so a re-create failed `already registered`. claustrum now reads the
  worktree's `.git` pointer before removal and drops the admin directory too.
- `gitTimeout` (D5) does NOT authorise the deletion, and this whole timeout arm
  is off by default. When armed it answers
  `{"success":false,"error":"git worktree remove timed out after <dur>; no cleanup
  was attempted, and git may have partially removed the worktree"}` and removes
  nothing. A hit on the earlier config or repository check answers the lock-check
  refusal instead. With `worktreeRoot` that hit answers the work-tree refusal. See [`DIVERGENCES.md`](DIVERGENCES.md) → D5.

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

At stdin EOF the bridge half-closes its socket write side, so the daemon reads
EOF. The bridge then keeps copying the socket to stdout. It exits `0` when the
daemon closes the connection, even with stdin still open. It prints every frame
that arrived before the close. Measured against `f6010b97` and `90fca6e6` on
Linux and Windows VMs, the reference bridge relayed the parse-error frame for a
final partial line. It then exited `0`. Measured on a Linux VM, the
reference bridge also exited `0` with stdin still open once the daemon closed.
It printed the frames that arrived before the close. If the half-close fails,
the claustrum bridge closes the socket fully.

On Windows the daemon can miss the half-close of the bridge (see
[Transport](#transport)). The bridge then does not exit.

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
