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

A new daemon replaces the token of a dead daemon only after its bind. On a Linux
VM (row P12, two runs), a dead daemon left its `daemon.token` and one recorded
child that ignored `SIGTERM`. For the 2.2 s of the reap, the file still held the
token of the dead daemon. `89cb6289` and claustrum renamed the new token into
place after the `SIGKILL` of the reap, the `bind` and the `listen`.

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
record into that file. The record is a JSON object. At first it is
`{pid, role, node}`, where `role` is `serve`. After the reap of the start and the
bind, the daemon writes it again as `{pid, role, node, instanceId, startedAt}`.
`node` is omitted when the machine identity is unknown. Reference build `4534d86`
added the lock, and claustrum matches it. It is off the JSON-RPC wire, because it is a file
beside the socket. On graceful shutdown the daemon truncates the record and drops
the lock, but it leaves the file in place. `daemon.token` is unlinked instead. A
Linux VM measured the same end state against `89cb6289` in the stop rows:
`daemon.token` and `rpc.sock` removed, `daemon.lock` left empty. The `instanceId`
of the record is the instance of the listening line, and the `instanceId` that
`server.capabilities` answers. On a Linux VM, the listening line, the lock and
`server.capabilities` of `89cb6289` held one value in 36 of 36 daemons.

Linux and macOS VMs measured the two forms of the record against `89cb6289`. In
row D1 a dead daemon left one recorded child that ignores `SIGTERM`. The lock of
the new daemon held `{"pid":<n>,"role":"serve","node":"<node>"}` for about 2 s.
Then it held the full record. With nothing to reap (row D2), the first read on
macOS showed the full record. On Linux one read of row D2b showed the short form.
The `startedAt` of the record is the `startedAt` that `server.capabilities`
answers. Row D3 measured that on `89cb6289` in one Linux read and in 3 of 3 macOS
reads.

A prior live daemon can still hold the lock. The newcomer then evicts it before it
takes over, with `SIGTERM` and then `SIGKILL` after a grace period. On Linux the
`SIGTERM` goes out as `pidfd_open` and then `pidfd_send_signal`. A Linux VM
measured those two calls with `strace` against `89cb6289` (row E3). The
`SIGKILL` is `pidfd_send_signal` with the same descriptor number (rows H7 and
N3). macOS uses `kill`. The call of the macOS reference is not measured. The identity
reads of the holder come between the open and the send. On a Linux VM (row P6, by
`strace`, 2 of 2 runs), `89cb6289` made these calls in this order: `pidfd_open`,
the read of `/proc/<pid>/ns/pid`, the read of `/proc/<pid>/cmdline`, then
`pidfd_send_signal`. claustrum is built to that order.

After the `SIGTERM` the newcomer waits for the lock, not for the pid. On a Linux
VM (row H7, by `strace`), the holder was stopped with `SIGSTOP`. `89cb6289` tried
`flock(LOCK_EX|LOCK_NB)` on `daemon.lock` every 50 ms, and the call answered
`EAGAIN` 43 times. It sent the `SIGKILL` 2.02 s after the `SIGTERM`, and it got
the lock 50 ms later. It sent no `kill(pid, 0)` in that row, and none in rows E3,
P5, P6, N3 and N4. claustrum is built to it: it tries the lock every 50 ms for 2 s,
sends the `SIGKILL`, and tries for 1 s more. That 1 s is claustrum's own value
and is not measured. Before the `SIGKILL` claustrum tests the identity of the
holder again. A restart
therefore replaces its predecessor deterministically. The eviction only fires
against a holder that is verifiably one of our own `-serve` daemons for this
socket, on this machine. See the guards in `daemon_runlock_unix.go`. Claiming is
best-effort. Any failure logs a warning, and the daemon serves without run-dir
ownership rather than aborting.

If the record of the holder has no instance, the eviction lines name its pid
alone. On a Linux VM and on a macOS VM, `89cb6289` logged these two lines for a
holder with the short record (row P5, 5 of 5 runs on each system):

```
serve: run dir is held by a live daemon, pid <n>; sending SIGTERM
serve: previous daemon pid <n> exited after SIGTERM
```

For a holder with the full record, both lines read
`pid <n> (instance "<hex>")` (row E3). claustrum is built to both rows.

A holder that still has the lock after the 2 s gets `SIGKILL`. On a Linux VM
(row H7), `89cb6289` then logged these two lines:

```
serve: WARNING previous daemon pid <n> (instance "<hex>") ignored SIGTERM for 2s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)
serve: previous owner of <dir>: killed
```

For a holder with the short record, the first line reads `pid <n>` with no
instance (row N3). claustrum is built to both rows.

A signal can reach a daemon during its start. On a Linux VM (row P5, 5 of 5
runs), a second daemon started 0.5 s after a first one. The first daemon still
waited in the reap of its start, for a recorded child that ignored `SIGTERM`. The
second daemon sent `SIGTERM` to the first one. The first daemon of `89cb6289` did
not end there. Its log held these lines in this order:

```
[process.Registry] ending orphaned process group <pgid> recorded by daemon instance "<inst>" (pid <dpid>): SIGTERM
[daemon] received terminated; shutting down (children will be killed)
[process.Registry] group <pgid> outlived SIGTERM for 2s: SIGKILL
[daemon] serve: predecessor's children — 1 orphaned group(s): 0 ended on SIGTERM, 1 on SIGKILL, 0 survived; 0 stale record(s) dropped, 0 kept
[Server] shutdown requested
Claude remote server listening on <socket> (pid <n>, instance <32-hex>)
[daemon] host cleaning off: executable "<binary>" is not a deployed daemon under <root>/srv
[Server] cleanup: closed 0 connection(s), killed 0 child process group(s)
```

The first daemon bound, listened and wrote its token, then removed the socket
and the token and exited 0. The recorded child got one `SIGTERM` and one
`SIGKILL`, both from the first daemon. The second daemon signalled no child, and
it served. On a macOS VM (row P5, 5 runs), the first daemon of `89cb6289` went
on in the same way. Its log held the received line, the outlived line, the
summary line, the `shutdown requested` line, its listening line and the cleanup
line, in that order.
claustrum is built to that row. It installs its handler for `SIGTERM` and `SIGINT`
before the claim of the lock, and it handles one signal.

More Linux rows measured a signal or a second start during a start. In each row
the recorded child of a dead daemon ignored `SIGTERM`. Build `ac5cadb` of
claustrum sent the same signals and gave the same end state as `89cb6289` in
each row but one, the 0.05 s gap:

- A `SIGTERM` or a `SIGINT` 1 s into the reap (rows H8a and H8b). The daemon
  logged `received terminated; shutting down (children will be killed)`, or
  `received interrupt` with the same tail. It sent the `SIGKILL` of the reap at
  2.1 s, bound and exited 0. The run folder then held `children`, an empty
  `daemon.lock` and the logs.
- Two `SIGTERM` 0.3 s apart (row H8c). The daemon logged one `received` line and
  did the same.
- A `SIGTERM` to a daemon that waits in its eviction of an older holder (row N4).
  The daemon still sent the holder `SIGKILL` after the 2 s, took the lock, sent
  the child of that holder `SIGTERM`, bound and exited 0. Its launcher exited 1
  after 12.08 s with `daemon did not take over <socket> (predecessor still owns it)`.
- A `-stop` 0.5 s into the reap (row N5). The connect of `-stop` was refused. It
  sent the daemon one `SIGTERM` and tried the lock every 50 ms. The daemon
  finished the reap, bound and exited 0. `-stop` then got the lock, printed
  `terminated` and exited 0, 1.7 s after its start. The launcher of the daemon
  exited 1 after 12.09 s with the timeout line.
- A second daemon 0.2 s after the first (row N3, 3 runs). The second daemon sent
  `SIGTERM` only. The first one finished, bound and exited 0. The second one
  then bound and held the lock with its full record.
- A second daemon 0.05 s after the first (row N3, 3 runs). The first daemon was
  still in its start after the 2 s, so the second daemon sent it `SIGKILL`.
  `89cb6289` then got the lock, bound and wrote its own full record. It sent the
  recorded child no signal. Build `ac5cadb` of
  claustrum read the killed daemon as alive and served without the lock. claustrum
  now waits on the lock as `89cb6289` does, and it is built to this row.

A process that is no daemon can hold the lock, with no owner record in the file.
The daemon then signals nothing and logs three lines. On a Linux VM (row ST02),
`f6010b97` and `89cb6289` logged the same three texts:

```
[daemon] serve: WARNING <dir>/daemon.lock is held by a live process we will not signal (its holder is not a daemon (role "")); leaving it alone
[daemon] serve: previous owner of <dir>: survivor
[daemon] serve: not the run dir's lock holder; leaving any predecessor's children alone and recording none
```

claustrum logs the third line at every start that ends without the lock. The other
cases without the lock are not measured. A record with another role text is not
measured. On Linux and macOS claustrum still records and reaps children in that
state. What the reference does in that state is not measured.

The lock, the owner record, and the eviction run on Linux and macOS only. The
machine identity (`node`) is the boot id joined to the pid-namespace inode on
Linux, and `sysctl kern.bootsessionuuid` on macOS. Windows ships no run-dir
lock. Mutual exclusion on Windows stays the socket remove-then-rebind handoff.
On Windows the "not the run dir's lock holder" line above is the first log line of
every start. A Windows VM (row K11) measured that against `f6010b97` and
`89cb6289`.
On macOS claustrum makes sure of the holder through `sysctl KERN_PROCARGS2`,
where the reference skips that test. This holds for the
eviction and for `-stop`. See [DIVERGENCES.md](DIVERGENCES.md) D15.

### Host cleaner (off-wire, Linux and macOS)

A periodic sweep ends orphaned Claude Code process groups, retires abandoned
daemons and tidies stale run dirs. It
reaches no JSON-RPC frame, so nothing here is a wire contract. It is recorded
because one of its decisions is an always-on divergence that a client can feel as
a lost session.

The sweep ends no stranded daemon. A stranded daemon is a daemon whose socket path
no longer leads to it. The cleaner sends no signal to such a daemon and none to its
children. On a Linux VM, `f6010b97` and `89cb6289` sent none in every staged shape
(rows ST01, ST02, ST04 to ST10, HC16 to HC19, HC23 to HC26). The stranded daemon
ends by itself. See [Orphan-exit self-probe](#orphan-exit-self-probe). On a macOS
VM, `f6010b97` and `89cb6289` sent none either (rows ST01, ST09, ST10, HC16,
HC18). In row ST01 on that VM, claustrum sent none either.

The cleaner retires an abandoned daemon with `SIGTERM`. On Linux the signal goes
through `pidfd_send_signal`, as on the reference (rows HC10, HC12 to HC15, by
`strace`). `89cb6289` and claustrum open the descriptor with `pidfd_open`, then read
`/proc/<pid>/stat`, then send the signal. A Linux VM saw that order in 26 of 26
retires for each binary (rows HC10, HC12a, HC13, HC14). On a kernel without that
call claustrum uses `kill`. That fallback is
not measured. macOS uses `kill`. The call of the macOS reference is not measured.
On a macOS VM, the cleaners of `89cb6289` and claustrum ended the same daemons and
orphan groups and removed the same folders (rows K9, HC01, HC10, HC14c, HC20,
HC22d).

The cleaner is on in one case only. The cleaned socket path is absolute, and its
last three components read `run/<id>/rpc.sock`. The folder above `run` is the
root. The resolved daemon binary is `<root>/srv/<id>/<name>` under that root, or
under the symlink-resolved form of that root. In every other case the cleaner is
off and runs no pass. The daemon then logs one of two lines, right after the
listening line:

```
[daemon] host cleaning off: socket path "<socket>" is not <root>/run/<id>/rpc.sock
[daemon] host cleaning off: executable "<binary>" is not a deployed daemon under <root>/srv
```

The socket line names the socket as given. The executable line names the resolved
binary and the `srv` of the root as given. If the daemon cannot read its own
executable path, the binary test does not run. That case is not measured.

A pass acts on one folder. Its root list holds the root as the socket path gives
it. When the resolved form of that root differs, the list also holds that form,
which is the same folder under its real path. The root of the binary is never
added. A pass lists each run folder once, and its lines name the run folders in
the spelling of the socket as given. If the resolution of the root fails, the list
holds only the root as given. That case is not measured.

A Linux VM measured the decisions of this gate against `89cb6289`, in start rows
and in rows with staged fixtures. claustrum gave the same decision in each row:

| start row | `89cb6289` and claustrum |
|---|---|
| `<R>/run/c1/rpc.sock`, binary `<R>/srv/d1/<name>` | on, a pass 15 s after the start |
| `<R>/run/./k1/rpc.sock`, `<R>//run/k1/rpc.sock` (rows KS-i-j, KS-i-k) | on |
| `<R>/b/s.sock`, `<R>/b/rpc.sock`, `<R>/run/rpc.sock`, `<R>/run/c1/x.sock`, `<R>/run/c1/d/rpc.sock`, `<R>/RUN/c1/rpc.sock` | off, socket line |
| `<R>/lnk/c1/rpc.sock`, where `lnk` is a symlink to `run` | off, socket line |
| relative `run/c1/rpc.sock` (rows START-i-h, START-p-h, KREL) | off, socket line |
| `<R>/run/c1/rpc.sock`, binary outside `<R>/srv` (the plain layout) | off, executable line |
| binary `<R>/srv/<name>`, or `<R>/srv/d1/sub/<name>` | off, executable line |
| binary `<R>/srv/d1/<name>` that is a symlink to a file outside | off, executable line with the resolved path |
| binary under the `srv` of another root (rows START-x-e, KXB, KS-x-e) | off, executable line |
| another file name in `<R>/srv/d1` | on |
| binary started through a symlink to `<R>`, socket through the real path (row KS-u-e) | on |
| socket through a symlink `<R>l` to `<R>`, binary through either path (rows KS-m-e, KS-q-e) | on, the lines name `<R>l/run/<id>` |
| binary and socket through a symlink one level above the root | on |

The socket rows gave the socket line in the install layout and in the plain
layout. In rows KREL and KXB `89cb6289` sent no signal and removed no folder. In
rows KS-m-e and KS-q-e it ran one pass with staged fixtures. `f6010b97` logged the
same two texts in its rows (EV01, K3, ST08 and every plain-layout row). A macOS VM
measured nine gate shapes against `89cb6289` (rows GSa to GSg3). claustrum made the
same decision in each one, with the same log lines. In row GSc the daemon binary
runs through a symlinked root and the socket has the real path: both run the
cleaner.

claustrum links no cleaner on Windows. The Windows references logged no cleaner
line and changed nothing in 120 s (row WN06, with no positive control).

Before it signals a daemon whose run dir went idle past the threshold, or whose run
dir name carries `.removing-` from an earlier removal, the cleaner
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
polls every 20 ms, up to a bound of 12 seconds. To make sure that the daemon is
ready, it dials the socket and closes the connection again. A freshly started
daemon's log therefore holds a `New connection from: @` /
`Connection closed: @` pair from the launcher's own probe, after the listening
line.

It waits for the path to exist, not for a successful dial. One case differs on
Linux and macOS: the stale socket file of a killed daemon. That case is below. It
also does not give up early when the child dies. Both behaviours are measured
against `5db5e4a`:

| start | what the launcher sees | outcome |
|---|---|---|
| normal | path appears, and the dial succeeds | exit `0` |
| socket path occupied by a directory | path exists immediately | exit `0` (~0.01 s, reference 0.08 s) |
| child can never bind (uncreatable parent dir) | path never appears | exit `1` at the launcher bound (`5db5e4a` 10.06 s) |

The launcher bound of claustrum is 12 s. On a Linux VM (row P7), a daemon child of
`89cb6289` ended before its bind, and the launcher exited `1` after 12.06 s. The
row holds three forms: a stale socket file, no socket file, and an empty run
folder. A macOS VM measured 12.08 to 12.16 s in the same three forms (row P7). A
Windows VM measured 12.058 s with a stale socket file and with none. The bound is
shared code, and claustrum took 12.01 to 12.13 s in those rows. The bound of
`5db5e4a` in the table was about 10 s, and claustrum used 10 s until that row.

A Windows VM measured the occupied path against `89cb6289` (rows OCC and OCCf, two
runs each). An empty folder or a regular file was at the socket path. On both
sides the launcher exited `0` at once, the entry was removed, and the daemon
served on a socket at that path. A folder with content is not measured.

On Linux and macOS the daemon binds the socket only after the reap of its start.
The order is: claim `daemon.lock`, reap the recorded children of a dead daemon,
remove the old socket file, bind, complete the owner record. On a Linux VM the
calls of `89cb6289` came in this order (row D1, by `strace`): the open of
`daemon.lock`, the signals of the reap, the unlink of the old socket file, `bind`
and `listen`. The `strace` rows hold no write to the lock. The place of the last
step comes from the `startedAt` of the full record. In 8 of 8 Linux runs of rows
D1 and D2 it was within 1.5 ms of the `listen` call. While the reap waits
for a group to end, nothing listens on the path. In row D1 a recorded child
ignored `SIGTERM`, so the wait took about 2 s. A client that connected in that
wait got `ECONNREFUSED` when the socket file of the dead daemon was there (row
D1a). It got `ENOENT` when no socket file was there (row D1b). Linux and macOS VMs
measured both against `89cb6289`. The first connection worked 2.2 to 2.4 s after
the start. With nothing to reap, the first connection
worked 0.1 to 0.24 s after the start (row D2). See
[process.spawn](#processspawn) for the reap itself.

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
predecessor and no socket file on disk, the wait is unchanged. The next paragraph
covers a stale socket file. The
inode-difference wait and that distinct message are unix-only. On Windows the
predecessor probe is a no-op that always reports no predecessor. Startup there
always takes the no-predecessor path, and on failure it emits only the ordinary
timeout line, never the "daemon did not take over" message. This is off the
JSON-RPC wire, because it is launcher lifecycle. The departing daemon's matching
half is the inode-ownership unlink under
[Token persistence](#token-persistence-daemontoken).

A killed daemon leaves its socket file on disk. The launcher records
the socket file that is on disk before it starts the child. While the path still
holds that file and no daemon answers a dial, the launcher keeps waiting. It then
dials the new daemon once. On a Windows VM (rows WN03 and WN05), the log of a new
`f6010b97` or `89cb6289` daemon held the connection pair in that case. On a Linux
VM (row XA), the `-serve` command of `f6010b97` and `89cb6289` returned when the
socket accepted, 2.06 s after its start. A dial
that a live older daemon answers ends the wait at once, as before. On Windows a
client that connects in that window is not measured. On Linux and macOS only a
socket file makes the launcher wait. Another kind of file at the path counts as
present, as in the table above. A stale socket file and a daemon that never binds
give the timeout line at the bound and exit `1`. That is form a of row P7 above:
`89cb6289` exited `1` after 12.06 s.

On Windows the launcher starts the serving process outside the job of the
launcher (`CREATE_BREAKAWAY_FROM_JOB`). The SSH server of the Windows VM puts each
session in a job with the limit flags `0x2800`. That job ends its processes when
the session ends, and it allows breakaway. On that VM (rows WJ01 to WJ07), the
serving process of `f6010b97` and `89cb6289` was outside the session job. Row WJ08
shows the same for `89cb6289`. It was alive 120 s after the session closed (row
WJ01). The serving process of claustrum was outside that job in rows WJ01 to WJ08.
How the reference leaves the job is not measured. A job that refuses breakaway
refuses that start. The launcher then starts the serving process inside the job
and prints one line on its stderr. On a Windows VM (row JB, a job with the limit
flags `0x2000`), `89cb6289` and claustrum both served from inside that job. Both
printed this text after a time stamp, and claustrum puts its level tag before it:

```
[daemon] detached spawn failed (fork/exec <binary>: Access is denied.); retrying without breakaway
```

### Idle-connection close

A `-serve` daemon closes any accepted connection that goes 5 minutes with no read
or write activity. This matches `f6010b97`, as measured. The timeout is fixed and
always-on. There is no flag and no configuration key to change it or to disable
it. The daemon stamps the last activity time on every read and write of the
connection. A per-connection watcher polls for silence, at a quarter of the
timeout, bounded to at most 30 s. Once the idle span reaches the timeout, the
watcher closes the socket and logs
`[Server] closing connection idle <d> in both directions: <addr>`. On a Linux VM
(rows HC12a and HC12b), `89cb6289` logged that text with `5m0s` and `@`. The watcher stops as
soon as the connection closes for any other reason, and as soon as the daemon
shuts down. It therefore never outlives its connection. This is off the JSON-RPC wire, because it is connection lifecycle and
sends no frame. It closes only an idle *connection*, never the daemon. A
client-less orphan daemon is retired by the separate orphan-exit self-probe below.

### Daemon log (`remote-server.log`)

The launcher creates `remote-server.log` in the socket's directory with mode
`0600`. The launcher rotates any existing log to
`remote-server.log.old` and creates a fresh one. One Windows case gets no fresh
file: a second daemon on a live socket (see below). This matches `4534d86`, which
keeps the previous session's log as `.old` on every restart. The launcher
redirects the daemonized child's stdout and stderr into that file, so the
launcher's own streams stay empty. On Linux and macOS, when the limit raise
works, the first line tells the open-files limit that `process.spawn` children
get. This line is at INFO level, so `CLAUSTRUM_LOG_LEVEL=warn` drops it. The
ready banner follows, with no timestamp:

```
2026/07/31 00:17:30 INFO  [daemon] child processes will start with an open-files limit of 65536
Claustrum remote server listening on /run/user/1000/claude/rpc.sock (pid 4711, instance 5f1c0a9e2b7d4c3e8a6f0b1d2c3e4f50)
2026/07/31 00:17:30 INFO  [daemon] host cleaning off: socket path "/run/user/1000/claude/rpc.sock" is not <root>/run/<id>/rpc.sock
2026/07/31 00:17:30 INFO  [Server] New connection from: @
```

The value of the first line is the soft limit it set, which is the lower of 65536 and the hard limit. `f6010b97`
writes the same line, with no level tag, before its banner. Linux and macOS VMs
measured this, with the value 65536. On the Windows VM the reference log has no
such line, and claustrum writes none there. A failed raise was not measured.
Claustrum writes no line then.

The banner ends with the pid of the daemon and its instance. The instance is the
`instanceId` of `server.capabilities`. `f6010b97` and `89cb6289` print the same
suffix (Linux, macOS and Windows VMs). The name `Claustrum` is claustrum's own.
The reference prints `Claude` there. The banner beside the lines of
`-listen-pipe` and `-metrics-addr` is not measured.

For a planted symlink in a writable directory, claustrum renames the link, not its
target, to `remote-server.log.old`. It then creates a fresh regular log with
`O_EXCL`. On Linux and macOS the link is never followed, and the victim stays untouched. If claustrum
cannot rename the existing entry, the exclusive create fails too. One such case is a
sticky directory that holds another user's file or symlink. On Linux and macOS
claustrum then declines the
log entirely and falls back to inherited stdio. In both cases, on Linux and macOS,
claustrum never follows the link and never writes into a file another user owns.
On Windows the launcher then opens an existing regular file for append (see
below). That open tests the file type first and has no owner test. A swap between
the test and the open is not measured. This follows from the code. This is
intentional divergence D8, and it is always-on. `4534d86` no longer
plain-truncates a foreign regular log, and claustrum matches the `.old` rotation. But in a root-owned sticky directory the
reference still follows a planted `remote-server.log` symlink. It then writes its
log into the victim, or it refuses to start. claustrum declines instead. The
trigger is not reachable on the deployed path, because the socket directory
(`~/.claude/remote/`) is per-user and not world-writable. That is why D8 is
always-on and not opt-in. See [`DIVERGENCES.md`](DIVERGENCES.md) → D8.

On Windows a second daemon can start on a live socket (rows WN04 and WJ04). The
first daemon holds `remote-server.log` open, so the launcher cannot rotate it. The
launcher then opens the same file for append, and the second daemon logs there.
Each daemon writes to the end of the file. On a Windows VM the file of claustrum
kept the 16 earlier lines of the first daemon. The later lines of both followed in
time order. `89cb6289` logs to the file too, and both launchers return at once. It
truncates the file at that start. The 16 earlier lines of its first daemon were
lost, and the later lines of that daemon sat behind a block of NUL bytes. This is
divergence D21, a maintainer decision of 2026-10-02: claustrum keeps the lines.
See [`DIVERGENCES.md`](DIVERGENCES.md) → D21. A first daemon of an earlier
claustrum build does not write in append mode. It writes at its own offset. That
mix follows from the code and is not measured.

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
is set. It is never a bare exit.

A Linux VM measured the self-exit of a stranded reference daemon (rows ST09 and
ST11, several runs). The log lines of `89cb6289` in one run:

```
[Server] socket path <socket> no longer leads to this daemon; shutting down if that holds for 10m0s with nobody connected
[Server] socket path <socket> did not lead back to this daemon (1/2); re-checking before acting
[Server] orphaned for 11m0s (socket path gone or re-bound, 2 self-probes failed, no connections); shutting down and killing 0 child process(es)
[Server] shutdown requested
[Server] cleanup: closed 0 connection(s), killed 0 child process group(s)
```

The second line came 9 min 59 s after the first, and the third line one minute
later. `f6010b97` logged the second and third line one minute later
(`orphaned for 12m0s`), in its one run. `89cb6289` logged `11m0s` in four rows and
`12m0s` in three. Each daemon exited with code 0, with no signal from another
process, and removed `daemon.token`. At its self-exit `89cb6289` ended both
children with `SIGKILL` (row ST11). On a Linux VM, claustrum logged the same
texts, ended both children and exited with code 0 (rows ST09 and ST11). It logged
`orphaned for 12m0s` in each of its six rows. The cause of the 60 s difference is
not measured, and the interval of the reference is not measured.

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
| `-32004` | `the managed launcher cannot be used: <reason>`, from `process.spawn` when the checks refuse its `launcher` param, and for any `launcher` on Windows (added in `89cb6289`) |
| `-32005` | `the managed launcher <path> could not be started: <reason>`, from `process.spawn` when the launcher passes the checks but does not start (added in `89cb6289`) |
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
| git.status | `<file>: not a regular file` | -32603. claustrum's own text, from the copy into the temporary git folder after the gate passed. `<file>` is `info/sparse-checkout` when that entry is a folder or a FIFO. The reference is not measured there |
| git.status | `<file> is larger than <n> bytes` | -32603. claustrum's own text, for a copied file over its bound, also one that grew after the gate. `<n>` is 1073741824 for `index`, `sharedindex.*`, `info/sparse-checkout` and a reftable file, and 1048576 for `HEAD` and `config.worktree`. The reference is not measured there |
| git.status | `<os error>` of the copy or of the temporary folder, for example `openat index: permission denied` or `stat <dir>: no such file or directory` | -32603. The raw Go error when a file of the entry cannot be copied, as an `index` of mode 000, or when the temporary git folder or a file in it cannot be made. The reference is not measured there |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "<value>" is not a count` | in the frame of each method, when the daemon's own `GIT_CONFIG_COUNT` does not parse. See "The daemon's own git environment" |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair <n> is incomplete` | in the frame of each method, when a pair below the daemon's `GIT_CONFIG_COUNT` is not set. See "The daemon's own git environment" |
| git.* | `git cannot run on this host; git not run: <exec error>: <git text>` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`), when the configuration listing fails and `git version` fails too. See "Git-directory trust check" (`89cb6289`) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`), for a configuration key over 1048576 bytes. `89cb6289` measured `git.info`. See "Hardened git calls" |
| git.* | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` / `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`). It answers more than 1024 hook names, or names over 65536 bytes in sum. `89cb6289` measured `git.info`. See "Hardened git calls" |
| git.* | `the repository's git directory could not be trusted; git not run: commondir not as git writes it: <S1, S2 or S3>` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`), for a stray `commondir`. "Git-directory trust check" gives the three texts (`89cb6289`) |
| git.worktree_create / git.worktree_remove | `baseRepo is inside a managed worktrees directory (beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees marker), or could not be validated as a trust root` | in `error`. Create adds `errorCode:"nested_base_repo"`, and remove has none. It answers a `baseRepo` under a managed worktrees tree, or one that fails claustrum's own trust-root test. No git runs. See the method sections |
| git.worktree_create | `branchName is required` | |
| git.worktree_create | `not a git repository` | in `error`, `errorCode:"not_a_repo"` |
| git.worktree_create | `refusing to create worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; … / already exists, …}` | in `error`, `errorCode:"unsafe_path"` (`7d193f89` containment). The spelling refusal is Windows-only and comes before containment. `<repo>` is `baseRepo` as sent, so an absent `baseRepo` gives the empty string (`f6010b97`, Linux and macOS VMs). With `worktreeRoot`, the first two texts also refuse a relative, absent or `..` `baseRepo`. `<p>` is then `baseRepo` as sent (`f6010b97`, Linux and macOS VMs) |
| git.worktree_create | `refusing to create worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, `errorCode:"symlinked_component"`, for a symlinked ancestor component under the repo (`7d193f89`) |
| git.worktree_create | `failed to create parent directory: "" does not name a directory` | in `error`, `errorCode:"mkdir_failed"` (empty `worktreePath`, without `worktreeRoot`) |
| git.worktree_create | `failed to create parent directory: <repo>\<component> is not a directory` | in `error`, `errorCode:"mkdir_failed"`, Windows, when `.claude` or `.claude\worktrees` is a junction. claustrum also refuses another directory level, or a non-symlink reparse point, the same way (not measured). Nothing is created: no directory, no entry, no branch (`f6010b97`, Windows VM, rows JCR1 and JCR2) |
| git.worktree_create | `refusing to create worktree: <root> is the repository <repo> or inside it; a worktree location must be outside the repository` | in `error`, `errorCode:"unsafe_path"`, with `worktreeRoot`. A root that is `baseRepo` or lies beneath it by whole components is refused. `<B>/Tx` beside `<B>/T` passes. `<root>` is `worktreeRoot` as sent, and `<repo>` is `baseRepo` as sent. claustrum cleans both paths first, as on remove. No create row measured that cleaning. It comes after the repo test and before the root-chain tests. claustrum runs it after the two-level test, as on remove. That order is not measured on create. Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_create | `refusing to create worktree: <root> passes through <dir>, which is writable by <who> without the sticky bit (mode <mode>), so they could replace what is beneath it; choose a location under directories only you (or the system) control, or remove the extra write permission (chmod go-w)` / `refusing to create worktree: <root> passes through <dir>, which is owned by uid <uid>, neither you nor the system; choose a location you reach through your own directories` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`. This is the ancestor test. It judges each directory above the root, top first. The root itself is not judged. The daemon resolves the symlinks of each level, then judges each directory from `/` down to that resolved level. Last it resolves the root and judges each directory above the resolved root. So when the root is a symlink, the chain above its target is judged. `<root>` stays the symlink as sent (row G42, Linux and macOS VMs, `89cb6289` only). A missing level or a failed resolve ends the walk with no refusal. The first failing directory reached is named. When the failing directories lie on one chain, that is the highest one. The kind of failure plays no part. `<dir>` is that directory with its symlinks resolved, and `<root>` is `worktreeRoot` as sent. The owner test comes first. uid 0 and the daemon's user pass. Any other owner gives the second text, even with the sticky bit set. `<uid>` prints unsigned, for example 4294967294 on macOS. Then the sticky bit clears the write tests. The group-write bit refuses unless the group is the private group of the daemon's user. The four tests of the private group are those of the root's own write test (see the `worktreeRoot` paragraph of `git.worktree_create`). The other-write bit always refuses. An owner of uid 0 gets no exemption from the write tests. `<who>` is "its group", "every user on this host" or "its group and every user on this host", as for the root. `<mode>` is the permission bits as four octal digits, so a mode of 2775 prints `0775`. Only the chain of the root is walked, not the chain of `baseRepo`. A create without `worktreeRoot` and `git.worktree_remove` run no ancestor test. It comes after the test above and the 9 git calls (see "Hardened git calls"). It comes before the checkout tests below, the root-chain tests and the tests of the root itself. The root-chain rows below therefore apply only when the ancestor test passes. So a failing directory above the root gets this text even when its owner has no search access to it (row G36a). The `lstat` text does not apply there. Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs (rows K1, G1 to G41b, Y11d, T7 and T7c). In macOS rows G28 and Y11d the root is a firmlink spelling, and `<dir>` is `/System/Volumes/Data`. Only Linux VMs measured rows G1, G32, G33a, G33c, G34b and G36b. A macOS VM measured the other round 2 rows and G42 against `89cb6289` only. Not measured, with claustrum's choice: Two failing directories on different branches of the resolution: the first one reached is named. A failing directory above a symlink loop, a dangling link or a regular file: it refuses. A regular file above the root: its mode is judged. An unsearchable directory with more levels below it: the walk ends, and no directory below it is judged. The target of a symlinked root: it is not judged itself, as the root is not. Whether `/` is judged: it is, and it passes on every host measured. A failed stat of a path that the walk just resolved: the walk returns "". Whether "you" is the real or the effective uid: claustrum uses the effective uid |
| git.worktree_create | `refusing to create worktree: <root> leads into the repository <repo> (at <at>); a worktree location must be outside the repository` | in `error`, `errorCode:"unsafe_path"`, with `worktreeRoot`. The root passes the in-repo test and the ancestor test above but leads into the git top level of `baseRepo`, or into the main checkout. The remove row below gives the test and the spelling of `<at>`. claustrum compares file identity as on remove (not measured on create). The ancestor test in the row above comes first. In macOS row Y11d and Linux row T7 the root also leads into the repository, and both references send the ancestor text. `<repo>` is `baseRepo` as sent. It comes after 9 git calls (see "Hardened git calls") and before the root-chain tests. Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_create | `refusing to create worktree: <root> leads into <wt>, a worktree of the repository <repo> (at <wt>); a worktree location must be outside the repository's checkouts` | in `error`, `errorCode:"unsafe_path"`, with `worktreeRoot`. The root passes the tests above but lies in a linked worktree `<wt>` of the repository. The remove row below gives the test. `<repo>` is `baseRepo` as sent. It comes before the root-chain tests. Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_create | `refusing to create worktree: <root> is inside a git checkout (<dir> has a .git entry); a worktree location must be outside every checkout, so that no session working in one can reach it — choose a directory that is not part of any repository` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`, when a directory from `/` down to the root holds a `.git` entry of any kind. A missing directory is skipped. The ancestor test answers first (rows G18, G40a and G40b). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs, with a `.git` entry up to five levels above the root. `<root>` is `worktreeRoot` as sent, with a trailing slash or `//` kept. `<dir>` is that directory, resolved and cleaned. Linux and macOS VMs measured these spellings. When several directories hold a `.git` entry, the highest one is named. Linux and macOS VMs measured that too |
| git.worktree_create | `refusing to create worktree: <dir> is itself a git checkout (it has a .git entry); a worktree location must be outside every checkout` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`, when the `<directory>` level holds a `.git` entry. Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. Under a symlinked root, `<dir>` has the root resolved. Linux and macOS VMs measured that spelling |
| git.worktree_create | `refusing to create worktree: <root> passes through too many symbolic links (a loop?)` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`, when a symlink loop is in the path of the root. A failing directory above the loop gets the ancestor text first (claustrum's choice, not measured). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. `<root>` is `worktreeRoot` without a trailing slash. claustrum also drops a `//` here, and it sends this refusal for a finite chain of more than 255 links (neither measured) |
| git.worktree_create | `failed to create parent directory: lstat <path>: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`. There are two cases. First, a root symlink names a missing target, and `<path>` is that target. Second, a directory from `/` down to the root cannot be searched. Then `<path>` is `<dir>/.git` for the highest such directory. When that directory is above the root and fails the ancestor test itself, it gets the ancestor text instead (row G36a). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. Other errors from resolving the root are sent as Go prints them (not measured) |
| git.worktree_create | `failed to create parent directory: <path>: not a directory` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`, when the deepest existing part of the root is not a directory after its symlinks are resolved. `<path>` is that resolved path. It can be the root, a path above the root, or the file that a root symlink names. A regular file above the root that fails the ancestor test gets the ancestor text first (claustrum's choice, not measured). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs |
| git.worktree_create | `failed to create parent directory: <path> is not a directory` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS. Without `worktreeRoot`, an existing component above the leaf is not a directory. `<path>` is its full path, with the symlinks of the repo resolved and with `//` and `/./` removed. With `worktreeRoot`, the `<directory>` level exists and is not a directory, and `<path>` is its path with the root resolved. A `<directory>` that is a symlink gets the symlink refusal first. Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. Linux and macOS VMs measured the symlinked `baseRepo`, the `/./` and the symlinked root. A macOS VM measured the `/tmp` repo spelling. On Windows, without `worktreeRoot`, a file in the path keeps the `os.MkdirAll` text, `mkdir <path>: <OS error>` (not measured) |
| git.worktree_create | `failed to create parent directory: statat .: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, without `worktreeRoot`, when an existing `.claude` or `.claude/worktrees` cannot be searched. This holds also when nothing is left to create. Measured against `f6010b97` on Linux and macOS VMs |
| git.worktree_create | `failed to create parent directory: statat <name>/.git: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`, when the `<directory>` level exists and cannot be searched. `<name>` is its last component. Measured against `f6010b97` on Linux and macOS VMs |
| git.worktree_create | `failed to create parent directory: mkdirat <name>: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, when a missing directory above the leaf cannot be created. Without `worktreeRoot`, `<name>` is that directory's last component. With `worktreeRoot`, `<name>` is its path relative to the deepest directory that existed before the call, for example `a/b`. A directory that the call made before the failure stays. Measured with `permission denied` and `file name too long` against `f6010b97` on Linux and macOS VMs. Other errno texts take the same shape (not measured). A root made in the same call, then a failed `<directory>`, reads `mkdirat R/cp` by the same rule (not measured) |
| git.worktree_create | `failed to create parent directory: cannot mark <dir> as a worktree location: openat .claude-managed-worktrees: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`, when the marker cannot be created and no entry of that name exists. `<dir>` is the `<directory>` level with the symlinks of the root resolved. The leaf is not made. Measured against `f6010b97` on Linux and macOS VMs, also under a symlinked root |
| git.worktree_create | `git worktree add failed: <text>` | in `error`, `errorCode:"worktree_add_failed"`. `<text>` is git's stderr, made by the text rule in the method section. On `f6010b97` and on claustrum the text can start with git's graft-file deprecation `hint:` lines, because both set `GIT_GRAFT_FILE`. The 512-byte cap can then cut the rest. The rollback runs no git call and removes the leaf only if it is empty. A pre-existing branch is not deleted (`4534d86`). A failed add answers this frame even when the caller `timeoutMs` expired during the add (measured against `f6010b97` and `90fca6e6` on a macOS VM) |
| git.worktree_create | `git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)` | in `error`, `errorCode:"worktree_add_failed"`, when the attach add for `existingBranch` fails and the fallback `-b <branchName>` add fails too. The text rule makes each text on its own, each with its own 512-byte cap. Measured against `f6010b97` and `90fca6e6` on a macOS VM |
| git.worktree_create | `git worktree add failed (checkout): <text>` | in `error`, `errorCode:"worktree_add_failed"`, when the `read-tree` checkout fails. Same text rule. Where git prints graft-file deprecation `hint:` lines, the text starts with them on `f6010b97` and on claustrum. `90fca6e6` prints no hint. Apart from the hint, the frame matches `90fca6e6` byte for byte. The new directory is removed. The branch the call created goes only when another ref reaches its tip (see [The branch step](#the-branch-step)). In attach mode the attached branch is kept (measured against `f6010b97`) |
| git.worktree_create | `git worktree add timed out after <n>ms (deadline expired {before the checkout started / during the checkout): <text> / after the checkout finished})` | in `error`, `errorCode:"timeout"`, from the caller-supplied `timeoutMs` (`4534d86`). An absent `timeoutMs`, or 0, arms no deadline. `<text>` comes from the stderr of the killed git, by the same text rule |
| git.worktree_create | `<frame>; and the undo could not finish for <leaf>: {the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll <entry>: <OS error>) / the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat <leaf base name>: <OS error>)}` | appended to the checkout-failure frame and to each `timeout` frame when a step of the rollback fails. The `errorCode` stays as it was. Measured against `f6010b97` and `90fca6e6` on a Windows VM. In attach mode the first text reads `the worktree directory and its registration both remain` instead, because the call made no branch (row C08b, `f6010b97` and `89cb6289`, Linux VM) |
| git.worktree_create | `<frame>; and the undo could not finish for <leaf>: <branch part>` | appended when the branch step of the rollback keeps the branch that the call made. See [The branch step](#the-branch-step) for each `<branch part>`. After a failed leaf rmdir, the branch part follows the rmdir text after `; ` (row C05). Some cases also add `"branchKept":true` after `errorCode`. The table there gives each case with its rows and VMs |
| git.worktree_remove | `refusing to remove worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; …}` | in `error`, with no `errorCode`. This is `7d193f89` containment. The spelling refusal is Windows-only and comes before containment. `<repo>` is `baseRepo` as sent. An absent or empty `baseRepo` gives the empty string (`f6010b97`, Linux and macOS VMs). A Windows VM measured an absent one. With `worktreeRoot`, the first two texts also refuse a relative, absent or `..` `baseRepo`, and `<p>` is then `baseRepo` as sent. An empty `worktreePath` gets the first text there (`f6010b97`, Linux and macOS VMs) |
| git.worktree_remove | `refusing to remove worktree: <root> is the repository <repo> or inside it; a worktree location must be outside the repository` | in `error`, with no `errorCode`, with `worktreeRoot`. The test cleans the root and `baseRepo` and compares whole components. A root that is `baseRepo` or lies beneath it is refused, and `<B>/Tx` beside `<B>/T` passes. `<root>` is `worktreeRoot` as sent, so a trailing slash stays. `<repo>` is `baseRepo` as sent, so `<T>/` and `<T>/.` stay too. It comes after the spelling, `baseRepo` and two-level tests, and before any git call. The worktree is not looked at. Nothing is deleted. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_remove | `refusing to remove worktree: <root> leads into the repository <repo> (at <at>); a worktree location must be outside the repository` | in `error`, with no `errorCode`, with `worktreeRoot`. The root passes the test above. The daemon resolves the symlinks of the longest existing part of the root and appends the missing rest. Then it walks from that path up to `/`. `<at>` is the outermost path on that walk that is the same file as one of two checkouts. These are the git top level of `baseRepo` and the main checkout, the first entry of `worktree list`. So `<at>` is a prefix of the resolved root, for example `/private/tmp/…` for a root sent as `/tmp/…` on macOS. A bind mount (Linux), a firmlink spelling and a case variant (macOS) match too, and `<at>` keeps their spelling. With `baseRepo` in a linked worktree, a bind mount (Linux row T1) or a case variant (macOS row T1m) of the main checkout matches too. `<repo>` is `baseRepo` as sent. It is below `<at>` in row Q19, and a linked worktree in row W1. The main checkout wins over a linked worktree nearer to the root. If both checkouts hold the root, the main checkout is named (row T2b). There it is also the outer one, and claustrum names the outer match. It comes after the work-tree step and `worktree list`. Nothing is deleted. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_remove | `refusing to remove worktree: <root> leads into <wt>, a worktree of the repository <repo> (at <wt>); a worktree location must be outside the repository's checkouts` | in `error`, with no `errorCode`, with `worktreeRoot`. The root passes both tests above. `<wt>` is the first linked worktree in `worktree list` that is the resolved root or a parent of it by whole components. The test compares `<wt>` as listed, as a string. It resolves no symlink in `<wt>`, folds no case and compares no file identity. It skips a listed worktree whose path does not exist, also a locked one (rows Y9 and T3). Of two nested worktrees, git lists the outer one first. A root that holds a linked worktree passes. claustrum skips only a path that does not exist. Another error of the lstat keeps the entry (not measured). `<repo>` is `baseRepo` as sent. Nothing is deleted. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_remove | `failed to remove worktree: the worktree location <root> is not reachable (<reason>); nothing was removed — retry once it is available, or remove the worktree by hand` | in `error`, with no `errorCode`, with `worktreeRoot`. If the root passes the three tests above but does not exist or does not resolve, this refusal comes right after them. `<root>` is `worktreeRoot` as sent. For a root that does not exist, `<reason>` is `<root> does not exist`, also with two missing levels (rows Y9, T3, T4, T8 and T9). Row T9 also has a missing `baseRepo`. For a symlink that does not resolve, `<reason>` is the error of resolving it, for example `lstat <target>: no such file or directory` (row T5). claustrum sends other resolve errors the same way (not measured). Another error of the lstat keeps the old answer. For `permission denied` both references and claustrum answer `failed to remove worktree: lstat <root>: permission denied`, and claustrum makes 2 more git calls first (row T6). Nothing is deleted, and the branch stays. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs. Row T6 ran on a Linux VM only |
| git.worktree_remove | `refusing to remove worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, with no `errorCode`. It keeps the delete off a planted link (`7d193f89`) |
| git.worktree_remove | `refusing to remove worktree: <t> {is a symbolic link, not a worktree directory / is not a directory}` | in `error`, with no `errorCode`, for a leaf that is a symbolic link or not a directory. `<t>` has the symbolic links of `baseRepo` resolved. With `worktreeRoot`, the links of the `<directory>` level are resolved instead. Nothing is deleted (`f6010b97`, macOS VM) |
| git.worktree_remove | `refusing to remove worktree: <t> changed while the removal was checking it` | in `error`, with no `errorCode`. claustrum's own text for a leaf that another directory replaced between two looks. Only a race reaches it, and no run has measured the reference there. Nothing is deleted |
| git.worktree_remove | `refusing to remove worktree: <p> is locked (git worktree lock); unlock it to remove it` | in `error`, with no `errorCode`. `7d193f89` refuses a LOCKED worktree (`success:false`) and leaves it in place. The message is fixed whatever the lock reason is. Before `7d193f89` the reference deleted it and answered `success:true`. |
| git.worktree_remove | `refusing to remove worktree: <p> is gone but its registration is locked (git worktree lock); unlock it to remove the registration and branch` | in `error`, with no `errorCode`, for a gone worktree with a locked registration. Nothing is deleted (`f6010b97`, macOS VM) |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (<reason>); retry` | in `error`, with no `errorCode`, for a gone worktree. Without `worktreeRoot`, `<reason>` is the hooks refusal or the trust refusal text (`f6010b97`, macOS VM). Since `89cb6289` it is also the `git cannot run` text (row L11). claustrum puts a limit refusal of the listing there too. `89cb6289` measured the limits on `git.info` only. `<reason>` is also the refusal of the daemon's `GIT_CONFIG_COUNT` (Linux, macOS and Windows VMs). With `worktreeRoot` and a missing absolute `baseRepo` without a `..` component, `<reason>` is the refusal of the daemon's `GIT_CONFIG_COUNT` (Linux and macOS VMs) |
| git.worktree_remove | `failed to remove worktree: "" does not name a directory` | in `error` (empty `worktreePath`, without `worktreeRoot`) |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (its registrations could not be examined); retry` | in `error`, with no `errorCode`. Without `worktreeRoot`, for a worktree directory that exists: the configuration of `baseRepo` cannot be read, `baseRepo` holds no repository, or the trust check refuses its git directory. When the daemon's `GIT_CONFIG_COUNT` is refused, the same holds. Linux and macOS VMs measured that also for a `baseRepo` of `<T>/missing/..`, which does not resolve. In both modes, for a worktree directory that exists: the worktree registry exists but cannot be read. Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (the repository at <baseRepo> could not be read); retry` | in `error`, with no `errorCode`, with `worktreeRoot`, when the daemon's `GIT_CONFIG_COUNT` is refused, `baseRepo` is missing and the worktree directory exists. This row covers an absolute `baseRepo` without a `..` component. Nothing is deleted (`f6010b97`, Linux and macOS VMs). With no `git` on `PATH`, the same text answers a missing `baseRepo` in three rows (`f6010b97` and `89cb6289`, Linux VM). The worktree directory holds a file (row L13wa), is empty (row L13we), or holds a `.git` file that names its entry (row DG1) |
| git.worktree_remove | `failed to remove worktree: cannot determine the repository's work tree: <reason>` | in `error`, with no `errorCode`, with `worktreeRoot` only. `<reason>` is a trust refusal text, `exit status 128`, the hooks refusal, or the refusal of the daemon's `GIT_CONFIG_COUNT` (`f6010b97`, Linux VM). Since `89cb6289` it is also `git finds no repository here: <git text>` (row N04). With no `git` on `PATH`, both references answer the exec error alone in nine rows where `baseRepo` exists (Linux VM, see "Git-directory trust check"). claustrum also puts the `git cannot run` text and a limit refusal of the listing there. Neither is measured with `worktreeRoot`. Nothing is deleted. See the method section |
| git.worktree_remove | `failed to remove worktree: cannot list the repository's worktrees: <exec error>` | in `error`, with no `errorCode`, with `worktreeRoot` only. `git worktree list --porcelain -z` and then `git worktree list --porcelain` both fail in `baseRepo`. Nothing is deleted. `89cb6289` answers `exit status 128` for a repository with a valid `HEAD` whose `.git/commondir` is a dangling relative symlink (row DG2s-g, Linux VM) |
| git.worktree_remove | `failed to remove worktree: statat .claude: permission denied` / `failed to remove worktree: open <baseRepo>: <error>` | in `error`, with no `errorCode`, without `worktreeRoot` only. `baseRepo` cannot be searched (mode 0600), or cannot be opened (mode 0000, or a regular file). Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: RemoveAll <entry>: <errno text>` | in `error`, when the delete of the worktree fails part-way. If an entry other than `.git` fails, `.git` stays. The entry of the worktree and the branch stay in every case (`f6010b97`, macOS VM) |
| git.worktree_remove | `removed the worktree but could not drop its registration (RemoveAll <name>: <errno text>)` | in `error`, `success:false`, when the verified entry cannot be deleted after the tree. The branch step still runs. On `f6010b97` the branch is deleted (macOS VM). Since `89cb6289` a kept branch adds `"branchKept":true` after `error` (row R19, Linux VM) |
| git.worktree_remove | `failed to remove worktree: openat .claude\worktrees: path escapes from parent` | D19, Windows, in `error`, with no `errorCode`, when `.claude` or `.claude\worktrees` is a junction. claustrum's own text. Nothing is deleted, and the branch stays |
| git.worktree_remove | `worktreePath must not be or contain the home directory: …` | D2, in `error`. It sits behind `7d193f89` containment on the default branch, where it fires only if a repo is an ancestor of home. It is the active home guard on the `external_root` branch |
| process.spawn | `Process ID is required` / `Command is required` | |
| process.spawn | `command must be an absolute path when a launcher is given` | with a `launcher` param |
| process.spawn | `the managed launcher cannot be used: <reason>` | -32004. The reasons are the `launcher.resolve` texts, and `a launcher cannot be applied on a Windows host` |
| process.spawn | `the managed launcher <path> could not be started: <reason>` | -32005 |
| launcher.resolve | `cliPath is required` | `{}`, `null` params, a `null` or empty `cliPath`, or a wrong key |
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
  `files.stat {"maxBytes":"{"}`. The reference answers that request with defaults,
  as measured on `f6010b97`. Both daemons ignore a
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

## Methods (20)

`server.capabilities` self-describes the set. Order as returned:

```
server.ping  server.capabilities  server.shutdown
files.list   files.validate  files.stat  files.read  files.extract_tar
git.info     git.status      git.list_branches  git.worktree_create  git.worktree_remove
launcher.resolve
process.spawn  process.stdin  process.kill  process.killAndWait  process.reattach
plugins.prune
```

Reference `7c2f88d` added `process.killAndWait` between `process.kill` and
`process.reattach`, for 19 methods. `7d193f89` then removed `server.version` and
brought the set to 18. Calling `server.version` now answers
`-32601 "Unknown method: server.version"`. The `-version` CLI flag is a separate
surface and still prints the daemon's version. `19f30c46` appended
`plugins.prune` last and brought the set back to 19. `plugins.prune` is the only
member of the `plugins.*` namespace. `89cb6289` added `launcher.resolve` between
`git.worktree_remove` and `process.spawn`, for 20 methods. The Linux, macOS and
Windows VMs saw it at that place.

### server.*

| method | params | result |
|---|---|---|
| `server.ping` | none | `{"pong":true}` |
| `server.capabilities` | none | `{"version":"<id>","methods":[…20…],"instanceId":"<32-hex>","startedAt":<unix-ms>,"features":["process.stdin.offset","git.status.baseRepo","git.info.discovered_root","git.worktree_create.timeoutMs","git.worktree_create.existingBranch","git.worktree_remove.unpushedGuard","process.spawn.shellAgentSocket","launcher.managed","git.worktree.external_root","server.instance_id"]}`. `plugins.prune` is the last method on every OS. `git.worktree.external_root` is omitted on Windows. `git.worktree_create.timeoutMs`, `git.worktree_create.existingBranch`, `git.worktree_remove.unpushedGuard`, `process.spawn.shellAgentSocket`, `launcher.managed`, `instanceId` and `startedAt` are present on every OS |
| `server.shutdown` | none | `{"ok":true}`, when the reply gets out. The handler waits until the teardown starts to close connections, and then returns the reply. The frame arrives only when its write wins the race with the close. See below |

- `server.version` was removed in `7d193f89`. It now answers
  `-32601 "Unknown method: server.version"` like any other unknown method.
- `instanceId` and `startedAt` were added by `4534d86`. They sit between `methods`
  and `features`. `instanceId` is a 32-hex string, and `startedAt` is the daemon's
  boot time in unix milliseconds. Both are present on every OS. claustrum
  generates `instanceId` from 16 crypto/rand bytes at startup. It stamps
  `startedAt` after the reap of the start and the bind. On a Linux VM the
  `startedAt` of `89cb6289` was within 1.5 ms of its `listen` call (rows D1 and
  D2, 8 of 8 runs). macOS has no call times. The owner record of
  `daemon.lock` holds the same two values. It echoes both on the capabilities
  reply for parity.
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
  the login shell's SSH agent socket. `89cb6289` inserted
  `git.worktree_remove.unpushedGuard` after `git.worktree_create.existingBranch`,
  because `git.worktree_remove` keeps a branch that no other ref reaches. The Linux,
  macOS and Windows VMs saw it at that place. `89cb6289` also inserted
  `git.info.discovered_root` after `git.status.baseRepo`. In that build `git.info`
  runs no `rev-parse --show-toplevel` on Linux and macOS (row L01). The Linux, macOS
  and Windows VMs saw the feature at that place. `git.info.discovered_root`, `git.worktree_create.timeoutMs`,
  `git.worktree_create.existingBranch`, `git.worktree_remove.unpushedGuard` and
  `process.spawn.shellAgentSocket` are present on every OS. `89cb6289` also
  inserted `launcher.managed` after `process.spawn.shellAgentSocket`, for
  `launcher.resolve` and the `process.spawn` `launcher` param. Windows lists it
  too, although a Windows spawn refuses every managed launcher. The VMs of all
  three OSes saw it at that place. The `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not
  change the capabilities frame (measured on macOS).
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
  The daemon then logs
  `[Server] cleanup: closed <n> connection(s), killed <n> child process group(s)`,
  as the reference does. The first count includes the connection that sent the
  request. On a Windows VM `89cb6289` logged `closed 0` in 2 of 9 such stops in one
  run. The rate is not measured. On Linux and Windows VMs, `f6010b97` and `89cb6289` logged that line at
  every clean shutdown (rows RP11, RP12a, RP12b, ST03, ST09, WN03, WJ03). The
  counts for a child that ended just before the shutdown are not measured.
- Log lines on a shutdown by `SIGTERM`. The daemon logs
  `[daemon] received terminated; shutting down (children will be killed)`, then
  `[Server] shutdown requested`, then the `cleanup:` line. On a Linux VM,
  `f6010b97` and `89cb6289` logged these three lines (rows RP11, RP12b, ST03).
  The line for `SIGINT` names that signal and is not measured. With
  `-keep-children` claustrum writes `kept` in place of `killed` in the first line.
  The `cleanup:` line then says `killed 0 child process group(s)`.
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

`f6010b97` added this check. The check looks at the directory the method runs
git in. That is `path` for `git.info` and `git.list_branches`. It is `baseRepo`
for `git.status`, `git.worktree_create` and `git.worktree_remove`. Every `git.*`
method runs the check before git runs in that directory. Unless a rule names its
OS, it is measured side by side against `f6010b97` on a Linux VM, and on macOS and
Windows VMs where the case can exist. The rules for a stray `commondir` come from
`89cb6289`, measured on Linux, macOS and Windows VMs. The check has three
outcomes. "Trusted" lets git run. "No repository" answers as if the directory held
no repository. "Refused" answers with one of the texts M2 to M5 and S1 to S3, and
git does not run.

The daemon finds the git directory as follows, when its own environment sets
neither `GIT_DIR` nor `GIT_COMMON_DIR`:

- The walk starts at the request directory with symlinks resolved, and goes up. A
  refusal names the resolved path, never the alias.
- A `.git` directory that passes the git-directory test is the git directory.
- A `.git` directory that fails the test ends the walk. If it holds a `commondir`
  entry of any type, it is the git directory, and the stray-commondir rules below
  judge it. Otherwise the answer is "no repository". Git itself walks on to an outer repository here, so
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
- G is not an entry and holds a `commondir`: the stray-commondir rules below judge
  it. Git writes that file only in an entry. An entry whose grandparent fails the
  test is not an entry, so its honest `commondir` (`../..`) gets S1. That answer
  follows from the rules and is not measured on `89cb6289`.
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
  gets S1 for the content `../..`, which names the short path. `f6010b97` refused it
  on a Windows 11 VM. The S1 text follows from the rules and is not measured on
  `89cb6289`. When a component of G is a symlink or a junction, the daemon resolves
  G. Else it keeps G as spelled.

A stray `commondir` is one in a git directory G that is not an entry. G is a main
`.git`, a bare repository or a submodule's git directory. `89cb6289` judges it in
these steps (rows T00 to T11):

1. A `commondir` symlink that leaves G is not followed (rows T07a, T07d and T07e). A
   dangling symlink whose target is relative and stays in
   G counts as none when G passes the git-directory test. G is then trusted (row
   T07c, target `nothing-here`). Git itself then fails to read the file. The method
   answers what git's failure gives: `git.info` with no `branch`,
   `git.list_branches` `-32603 "exit status 128"`, and the failed add on
   `git.worktree_create`. With `worktreeRoot`, `git.worktree_remove` answers `cannot
   list the repository's worktrees: exit status 128` and deletes nothing (row
   DG2s-g, Linux VM). Without `worktreeRoot` it answers
   `{"success":true,"branchKept":true}`. It deletes the worktree directory and its
   entry, and the branch stays (row DG3-g, Linux VM). When the directory is gone
   before the request, it deletes the entry (row DG3x-g, Linux VM). When G fails the
   git-directory test, that symlink gets S3. `89cb6289` answers so for a `HEAD` that
   reads `garbage` (rows DG2-g, DG2-info and DG2-status, each a main `.git`, Linux
   VM).
2. A `commondir` that is not a regular file of at most 1048576 bytes gets S2 with
   its reason (rows T05 to T09). A relative symlink that stays in G is followed
   (row T07b). A dangling symlink that is absolute, or that leaves G, gets S2 with
   `openat commondir: path escapes from parent` (rows T07d and T07e on a Linux VM).
   Their targets were `/nonexistent` and `../nothing`.
3. The content with every trailing CR and LF removed is exactly `.` or `./`.
   A space or a tab is not trimmed in the measured rows. Any other content gets S1, which quotes it (rows T01 to
   T04 and T10b). So ` .`, `. `, `.<TAB>`, `./x`, `..`, an empty file, `.\` and
   the absolute path of G itself are all refused.
4. G then passes the git-directory test, or it gets S3 (row T10a). The content
   test comes first.
5. Else G is trusted, and git runs with `GIT_COMMON_DIR` pinned to G. That covers a
   main `.git` (row T01), a bare repository (row T11a) and a submodule's git
   directory under `.git/modules/` (row T11b).

These cases are not measured on `89cb6289`. One is a leading CR or LF. Others are a
lone CR and `.//`. claustrum cuts the content to 60 runes after the trim, and that
order is not measured. Nor is the order of an S2 reason and a bad `HEAD`. A
dangling symlink in a git directory with missing `objects` or `refs` is not
measured. Neither is S2 for a FIFO or a mode-000 file on Windows.

The daemon's own environment changes the check as follows:

- A `GIT_DIR` names the git directory to judge, for every request directory, even
  a plain one. A relative value is taken relative to the request directory. A
  `GIT_DIR` that names a gone entry gets M5. On Linux and macOS a `GIT_DIR` that
  names a `.git` file means "no repository". On Windows that `.git` file is trusted
  and pinned to itself. Git then fails on the entry the file names, and the
  configuration listing says `fatal: not a git repository`. So the methods answer
  their "no repository" shapes (`89cb6289`, row N05 on a Windows VM). Any other
  `GIT_DIR` that does not exist is left to git.
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

M2 to M4 and S1 to S3 start with `the repository's git directory could not be
trusted; git not run: commondir not as git writes it: `. Each `%q` of a path is Go
quoting of the path cut to its first 300 runes. The content in S1 is cut to its
first 60 runes instead. A non-ASCII path is cut by runes, not bytes. An invalid UTF-8
byte stays a byte and prints as `\xff`. The rest of the text is never cut.

| id | text after the prefix | operands |
|---|---|---|
| M2 | `%q does not name the entry's own repository, %q; restore the file to read ../.. or remove that worktree entry and add the worktree again` | `<entry>/commondir`, `<repo git dir>` |
| M3 | `worktree entry %q has none (git always writes one there, containing ../..); the entry is damaged or was not made by git — recreate the worktree with git worktree add, or remove the entry` | `<entry>` |
| M4 | `%q is not a small plain file (<reason>); remove it, or remove that worktree entry and add the worktree again so git rewrites it` | `<entry>/commondir` |
| S1 | `%q reads %q; git writes that file only inside .git/worktrees/<name>/, and Claude Code's sandbox may leave one containing just "."; anything else sends git's configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did` | `<G>/commondir`, the content |
| S2 | `%q is not a small plain file (<reason>); git writes that file only inside .git/worktrees/<name>/; one here can send git's configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did` | `<G>/commondir` |
| S3 | `%q sits in a git directory git would not take as a repository (its HEAD, objects or refs missing or invalid), so git would look for one somewhere else; repair that directory (usually its HEAD file)` | `<G>/commondir` |

`<name>` in S1 and S2 is literal text, and the JSON encoder writes it as
`\u003cname\u003e`. `f6010b97` answered every stray `commondir` with one older text,
M1, and `89cb6289` printed it in no measured row.

The M4 and S2 reasons seen include `commondir is not a regular file`,
`commondir is larger than 1048576 bytes`,
`openat commondir: path escapes from parent`,
`openat commondir: no such file or directory` (M4 only) or
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
| `git.worktree_remove` | without `worktreeRoot`, the lock-check refusal for a worktree directory that exists, and `could not check whether <p> is locked (<text>); retry` for a gone one. With it, the work-tree refusal and `<text>` | without `worktreeRoot`, the lock-check refusal for a worktree directory that exists, and `{"success":true}` for a gone one. With it, the work-tree refusal and `exit status 128` |

The lock-check refusal is
`{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`.
It has no `errorCode`, and the path is neither quoted nor cut. The work-tree
refusal is `{"success":false,"error":"failed to remove worktree: cannot determine the
repository's work tree: <reason>"}`, also with no `errorCode`. With `worktreeRoot`,
the listing classes below give their own reason. With `worktreeRoot` and no `git`
on `PATH`, the exec error replaces both texts of the table in the measured rows
(class 1 below).

When the configuration listing fails, `89cb6289` sorts the failure into a class.
The classes come in this order (Linux, macOS and Windows VMs):

1. No `git` on `PATH`: "no repository", and no process starts. Linux, macOS and
   Windows VMs measured `git.info` (row L13). A Linux VM measured the other methods
   (rows L13xa to L13xd, L13t and L13u to L13z). `git.list_branches`, `git.status` and
   `git.worktree_create` answer their "no repository" shapes. The create answers so
   with and without `worktreeRoot`. `git.worktree_remove` without `worktreeRoot`
   answers `{"success":true,"branchKept":true}` for a gone worktree with a
   `branchName`, and the lock-check refusal for a worktree directory that exists.
   With `worktreeRoot`, both references answer the work-tree refusal with the exec
   error alone in nine rows: `exec: "git": executable file not found in $PATH`. In
   each row `baseRepo` exists. Rows L13xc and L13xd are a repository with the worktree directory present
   and gone. Rows L13u and L13v are a regular file and a repository folder of mode
   0600. Rows L13y and L13z are a `.git` file to nowhere and a plain folder. Row L13t
   is a git directory that the trust check refuses, so this answer comes before the
   trust check. Rows LNKb and LNKr are chains of 45 symlinks to a repository. In
   claustrum a refused daemon `GIT_CONFIG_COUNT` answers before the exec error when
   `stat` reads `baseRepo`. For the chains of rows LNKb and LNKr, claustrum runs no
   count check. Neither order is measured. A `baseRepo` that does not exist gets no work-tree
   refusal (rows L13wa to L13wr, see `git.worktree_remove`). Not measured: a
   daemon-created worktree under `worktreeRoot`, and `commondir` contents other than
   `x`. Rows L13xa and L13xb ran one repository layout only.
2. The listing exits 128, and its stderr has `fatal: not a git repository` at its
   start in every measured row.
   Leading white space is dropped first, and letter case does not count (rows L14a,
   L14b, L14d, L14e and N01 to N05). The answer is "no repository". Take
   `git.worktree_remove` without `worktreeRoot`, with a `branchName`. It runs a
   second listing for the branch step and keeps the branch. A gone worktree then
   answers `{"success":true,"branchKept":true}`. For a worktree directory that
   exists, claustrum answers the lock-check refusal. Not measured. With
   `worktreeRoot` the work-tree refusal carries `git finds no repository here: <git
   text>` (row N04). Exit 1 with that text is not this class (row L14c).
3. Any other failure, a failed start included. The daemon runs `git version` with no
   `-c` option. It runs in `/` on Linux and macOS, and in the daemon's working
   directory on Windows. Its environment is the one of the failed listing without
   the `GIT_COMMON_DIR` pin and without `LC_ALL=C` and `LANGUAGE=C`. If that call
   fails too, the text is `git cannot run on this host; git not run: <exec error>:
   <git text>` (rows L11, L12 and L15). Else it is the hooks refusal (rows L10 and
   L14c). With `worktreeRoot`, when git cannot start in `baseRepo`, `git version`
   gets the light environment (rows L16a and L16b, Linux VM).

When git cannot start in the directory itself (a file, or a folder of mode 0600),
the listing fails at the chdir. That is no hostile configuration, and the method
goes on.
`89cb6289` still runs `git version` there, and claustrum does too. The frames do not
change (rows L16a, L16b, L16d and L16f).

Some points are not measured. claustrum tests the class-2 text as a prefix, not as a
substring. Exit codes other than 1 and 128 are not measured. The "cannot run"
text carries the listing's detail. In rows L11, L12 and L15 the version call gives
the same text. `git version` does not get the daemon's own `LC_ALL` and `LANGUAGE`
back. On Windows the daemon's working directory and its socket folder were the same
folder, so the two are not told apart.

The hooks refusal is `config-defined hooks could not be pinned off; git not run:
listing the configuration in force: <exec error>: <git text>`. claustrum makes `<git text>`
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
  and `GIT_GRAFT_FILE=<null>`. The daemon's own `GIT_ALLOW_PROTOCOL` changes the
  first value (see the next section). The heavy variables are, in this order,
  `GIT_NO_LAZY_FETCH=1`, `GIT_ALLOW_PROTOCOL=denied_by_claude_ssh`, an empty
  `GIT_ASKPASS` and `GIT_TERMINAL_PROMPT=0`. Then comes `GIT_COMMON_DIR` when the
  trust check pins it. Then come the hook pins, from `GIT_CONFIG_COUNT`: two base
  pins, and two for each hook name of the listing before the call (see below). The
  daemon's own `GIT_CONFIG_COUNT` moves them (see the next section).
  Linux, macOS and Windows VMs.
- Heavy calls. `git status` and `rev-parse --absolute-git-dir` use the heavy
  profile. Every other hardened call uses the light profile, except the calls of
  [the branch step](#the-branch-step), which have their own variables. `git.status`
  runs more heavy calls (see its section). Of the claustrum calls, only its `status`,
  `ls-files` and `diff-index` calls add `GIT_OPTIONAL_LOCKS=0`. The checkout adds `GIT_INDEX_FILE`.
  Linux, macOS and Windows VMs.
- Null device. `<null>` is `/dev/null` on Linux and macOS, and `NUL` on Windows.
  The same value goes into `-c core.excludesFile` when the user has no global
  excludes file. The `-c core.hooksPath` and `-c core.attributesFile` values stay
  `/dev/null` on Windows. Windows VM. claustrum only: the `status`, `ls-files` and
  `diff-index` calls of `git.status` pass `-c core.excludesFile=/dev/null` on Windows
  too. That is divergence D16.
- Configuration listing. Before each hardened call except the first (see the next
  point), the daemon runs `git config -z --list` in the same directory. `89cb6289`
  dropped `--name-only`. The listing carries the profile variables of the call after
  it, and `GIT_COMMON_DIR`. Before a call of the branch step it carries the light
  variables. It carries no hook pins. The listing before the checkout and the
  listing before the `git status` call start with `--git-dir=<dir>`. Linux, macOS
  and Windows VMs.
- Listing locale. The listing drops the daemon's own `LC_ALL` and `LANGUAGE` and
  ends with `LC_ALL=C` and `LANGUAGE=C`, after the `GIT_COMMON_DIR` pin. `LANG` and
  the rest keep their places. git then writes its listing errors in English (row
  L03, Linux VM). The call after the listing keeps the daemon's `LC_ALL`, `LANGUAGE`
  and `LANG` (row L02, Linux, macOS and Windows VMs). Other `LC_*`
  variables stay too. Only `LC_ALL`, `LANGUAGE` and `LANG` were measured.
- Hook pins. `git config -z --list` ends each record with a NUL byte and puts a LF
  between key and value. A hook key that stands in a value pins nothing (row L04). A
  key `hook.<name>.<var>` names a hook.
  The name is the text between the first and the last dot. It can be empty or hold
  dots. `hook.<var>` names none. Each name counts once, in the order the
  listing first shows it, and gets `hook.<name>.enabled=false` and an empty
  `hook.<name>.event` (row L05). Each hardened call takes the names of the listing
  right before it. That choice is not measured: only configurations that stay the
  same during a request were run. Three limits refuse the method after the first listing, before
  any hardened call. A key over 1048576 bytes gets the hooks refusal with `a
  configuration key is longer than the listing reads` (row L08c). More than 1024
  names get `config-defined hooks could not be pinned off; git not run: too many
  configured hooks to pin` (row L06b). Names whose bytes add up to more than 65536
  get `…git not run: configured hook names exceed the aggregate pin byte bound` (row
  L07b). A value has no cap: 2 MiB passes (row L09). Linux, macOS and Windows VMs.
  Some points are not measured. Only `command` and `event` were used as `<var>`.
  claustrum pins two names that differ in letter case as two names. It counts a
  name under two keys once in the limits. It tests the count before the byte sum.
  A sum over 65536 made of names of at most 1024 bytes was not run. Nor was a value
  over 2 MiB. Every measured failure hit the first listing. A later listing that
  fails or exceeds a limit does not refuse the call after it. That call gets the two
  base pins only.
- Hooks refusal check. The first listing of a method is the hooks refusal check. It
  is not an extra call. If it fails, the method answers by the class of the failure
  (see [Git-directory trust check](#git-directory-trust-check)). If it exceeds a
  limit, the method answers the limit refusal (see Hook pins above). `git.info`,
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
  place, before the variables that the daemon adds. The next section gives the
  exceptions. If the daemon adds a variable that its environment already has, the
  added value and place win. Linux and macOS VMs. On Windows, the Go runtime of claustrum sorts the
  environment block by name. `f6010b97` does not sort it (Windows VM). Go's os/exec
  sorts it, and claustrum does not work around that.
- Temporary names. The temporary git dir of `git status` starts with
  `claustrum-git-dir-`. The temporary index directory of the checkout starts with
  `claustrum-gitidx-`. Each prefix has the length of the `f6010b97` prefix, 18 and 17
  bytes. Linux, macOS and Windows VMs.
- `git.worktree_remove` without `worktreeRoot`, for a worktree directory that exists.
  If the hooks refusal check or the heavy `rev-parse --absolute-git-dir` after it
  fails, the check runs once more. That
  is a second listing, and a second `rev-parse` when that listing passes. Then the
  method answers the lock-check refusal. Linux and macOS VMs.
- `git.worktree_remove` with `worktreeRoot`. A light `rev-parse --show-toplevel`
  follows the light check, with no listing of its own. Before the daemon decides
  whether `worktreePath` is a registered worktree, it runs a light
  `worktree list --porcelain -z` and a heavy `rev-parse --absolute-git-dir`. Each
  of them has its listing. claustrum does not use the answer of the last
  `rev-parse --absolute-git-dir`. For a `baseRepo` that exists, the "leads into"
  and "not reachable" refusals come right after `worktree list`.
  With the excludes read that is 7 calls, as on `f6010b97` and `89cb6289`. For a
  missing `baseRepo` and a missing root, both references send "not reachable" with
  no git call, and claustrum makes 1 call, the excludes read (row T9). Linux and macOS VMs.
- `git.worktree_create` with `worktreeRoot`. After the repo test and the in-repo
  root test, claustrum runs a light `rev-parse --show-toplevel`, a heavy
  `rev-parse --absolute-git-dir` and a light `worktree list --porcelain -z` in
  `baseRepo`. Each has its listing. With the excludes read and the repo test that
  is 9 calls. `f6010b97` makes the same 9 calls before it refuses a root in a
  checkout. `89cb6289` also makes 9 calls, in this order. claustrum does not use the
  answer of `rev-parse --absolute-git-dir`. On a create that goes on, the references
  make more calls than claustrum. Linux and macOS VMs. If the D5 deadline kills
  `rev-parse --show-toplevel` or `worktree list`, the checkout tests run without that
  answer. The root-chain tests still refuse a root that has, or lies below, a
  `.git` entry, with their own text. That is claustrum's choice (not measured). A
  create also goes on after any other failure of `worktree list --porcelain -z`. A
  remove with `worktreeRoot` runs the second call there. If that call fails too, the
  remove refuses.
  The references are not measured on a create there.
- Some calls of `f6010b97` have no claustrum counterpart. One example is a
  `rev-parse --show-toplevel` with `--git-dir` and `--work-tree` in
  `git.worktree_create`. claustrum runs that `rev-parse` pair in three places only.
  One is `git.info` on Windows. One is `git.worktree_create`, after an admin record
  that does not name `worktreePath` after symlink resolution. One is the gate of
  `git.status`, whose calls follow `89cb6289` (see its section).

#### The daemon's own git environment

A logging git wrapper measured the rules below against `f6010b97`. Each point names
its VMs.

Four calls get the daemon's environment as it is, with only their own additions.
These are the excludes read, the `--attr-source` version probe, the `git version`
call of the include scan and the `config --no-includes --file -` call of `git.status`. Linux, macOS and Windows VMs. The other git calls of the
git methods are the repository calls. These are the configuration listings, the
hardened calls, `worktree add`, the checkout and the `git status` call.

- `GIT_CONFIG` and `GIT_CONFIG_PARAMETERS` do not reach a repository call. The
  names match exactly, in upper case. On Windows, `git_config` and
  `Git_Config_Parameters` reach git. Linux, macOS and Windows VMs.
- A listing gets the daemon's `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` as they are. Linux, macOS and Windows VMs.
- A hardened call gets these names after the profile and the `GIT_COMMON_DIR`
  pin. First comes `GIT_CONFIG_COUNT`, with the number of all the pairs. Then come
  the daemon's pairs `0` to `count-1`, in index order. Then come the hook pins, from
  the pair `count` on. They are `hook.enabled=false`, an empty `hook.event`, and
  then the two pins of each hook name (`89cb6289`, rows L04 to L07). The
  `GIT_INDEX_FILE` of the checkout and the `GIT_OPTIONAL_LOCKS` of the `status`,
  `ls-files` and `diff-index` calls follow them. The values were measured on Linux, macOS and Windows VMs, and the
  order on Linux and macOS VMs.
- A hardened call gets no other `GIT_CONFIG_KEY_<digits>` or
  `GIT_CONFIG_VALUE_<digits>`. A leading-zero index such as `GIT_CONFIG_KEY_01` is
  removed too. Linux, macOS and Windows VMs. A listing keeps `GIT_CONFIG_KEY_01` in
  its place (Linux and macOS VMs). Other names, such as `GIT_CONFIGX` or
  `GIT_CONFIG_S4`, reach git. Linux, macOS and Windows VMs.
- The daemon's `GIT_ALLOW_PROTOCOL` sets the light value. If it is not set, the
  value is `https:ssh`. If it is set, the daemon splits it on `:` and keeps each
  entry that is exactly `https` or `ssh`, once, in its order. The match is
  case-sensitive, with no trim. If no entry is left, the value is
  `denied_by_claude_ssh`. Examples: `ssh:https:git` gives `ssh:https`,
  `https::ssh` gives `https:ssh`, and an empty value, `HTTPS:SSH` or `https,ssh` gives
  `denied_by_claude_ssh`. The heavy value stays `denied_by_claude_ssh`. Linux,
  macOS and Windows VMs. On Windows a lower-case `git_allow_protocol` sets the
  value too (Windows VM).

The git methods check the daemon's `GIT_CONFIG_COUNT`. The check reads the count
and its pairs by their exact upper-case names. On Windows it does not see a
lower-case `git_config_count` or `git_config_key_0` (Windows VM).

- If the count is not set, there is no check.
- An empty value counts as `0`. Else the check skips leading space, tab, `\n`,
  `\v`, `\f` and `\r` bytes. One optional `+` and one or more digits follow, and
  then the end. So `\t1`, ` +1` and `01` count as `1`, and `00` counts as `0`.
  Other values are refused. Examples are `-1`, `1\n`, `+ 1`, `++1`, a lone tab and
  a no-break space before the digit. So are `0x1`, `x` and `99999999999999999999`.
  The text is `inherited GIT_CONFIG_COUNT "<value>" is not a count`, with the value in
  Go `%q` quotes. Linux, macOS and Windows VMs.
- For each index from `0` to `count-1`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` must both be set. `<n>` is the plain decimal index, so
  `GIT_CONFIG_KEY_01` is not pair `1`. An empty value counts as set. The first
  index where one is not set is refused with
  `inherited GIT_CONFIG pair <n> is incomplete`. Linux, macOS and Windows VMs.
- An empty key is not refused here. On a repository, git then fails the listing.
  Linux, macOS and Windows VMs. `f6010b97` also refuses `git.info` and
  `git.list_branches` on a plain-directory `path`, and `git.worktree_create` and
  `git.worktree_remove` on a plain-directory `baseRepo`. claustrum answers these as
  it does with nothing set. Linux, macOS and Windows VMs (issue 429).

A refusal text starts with `config-defined hooks could not be pinned off; git not
run: `. Before the refusal, the daemon runs no git call except the excludes read.
The table gives the place of the check in each method, and its frame. A refusal of
the git-directory trust check comes first, with the trust text in the frame. That
order holds in `git.info`, `git.list_branches`, `git.status` and
`git.worktree_create`. It also holds in `git.worktree_remove` of a gone worktree
without `worktreeRoot`, and with `worktreeRoot` for a `baseRepo` that exists.
Linux and macOS VMs. A Windows VM measured it in `git.info`, `git.status` and the
remove of a gone worktree.

| method | frame |
|---|---|
| `git.info` | `{"error":{"code":-32603,"message":<text>}}`, also for a plain dir, a missing dir and a file. Linux, macOS and Windows VMs |
| `git.list_branches` | the same, also for a plain dir, a file, and an empty or absent `path`. For an empty or absent `path`, the daemon's working directory was a repository or a plain dir. Linux, macOS and Windows VMs |
| `git.list_branches`, `path` not empty and does not resolve, or inside a managed worktrees directory | `{"isRepo":false,"branches":[]}`, before the check and with no git call. See the method section |
| `git.status` | the same, also for a `path` that is a plain dir, the repository, missing or a file, and for a plain-dir `baseRepo`. Linux, macOS and Windows VMs |
| `git.status`, `baseRepo` does not resolve | `{"isRepo":false,"clean":false}`, before the check and with no git call. See the method section. From the code: a `baseRepo` in a managed worktrees tree answers the same way. That case is not measured with a refused count |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, also for a plain-dir or missing `baseRepo` and a relative `worktreePath`. Also for a `worktreePath` with a `..` component, outside the repository or that exists. A missing `branchName` gets its `-32602` frame first. A `baseRepo` inside a managed worktrees directory gets its `nested_base_repo` frame first. Linux, macOS and Windows VMs. On Windows the check comes before the `worktreeRoot` refusal (Windows VM). A `baseRepo` that fails claustrum's own trust-root test gets that frame first too. Only the Windows VM measured that order (round 1 row C1 P1) |
| `git.worktree_remove` | Some refusals come first, with their usual frames. These are for a relative path, a path outside the repository, the repository itself and a `..` component (Linux, macOS and Windows VMs). They are also for a path outside `worktreeRoot`, and with `worktreeRoot` for a `..` `baseRepo` (Linux and macOS VMs). A `baseRepo` that fails claustrum's own trust-root test gets the managed-worktrees refusal first. Only the Windows VM measured that order (round 1 row A1 P1). Without `worktreeRoot`, `open <baseRepo>: not a directory` comes first for a regular-file `baseRepo` (Linux, macOS and Windows VMs). So does `statat .claude: permission denied` for a `baseRepo` with mode 0600 (Linux and macOS VMs). With `worktreeRoot`, so does the refusal of a root in `baseRepo` (row E7, Linux and macOS VMs) |
| `git.worktree_remove`, no `worktreeRoot`, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`, also for a plain-dir or missing `baseRepo`. Linux, macOS and Windows VMs |
| `git.worktree_remove`, no `worktreeRoot`, worktree directory present | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`. Linux, macOS and Windows VMs. Linux and macOS VMs also measured a `baseRepo` of `<T>/missing/..`, which does not resolve. On Windows that `baseRepo` gets the managed-worktrees refusal first |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` a repository or a plain dir | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}`. A trust refusal puts its own text there instead. Linux and macOS VMs |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` missing, absolute and without a `..` component, worktree directory present | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (the repository at <baseRepo> could not be read); retry"}`. Linux and macOS VMs |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` missing, absolute and without a `..` component, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`. If the root is missing too, the "not reachable" refusal comes first (row T9). Linux and macOS VMs |

On Windows the `worktreeRoot` refusal of `git.worktree_remove` comes before the
check (Windows VM).

#### git.info
`{path}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""}`

- `repo` is the base name of `root`. A name that starts with `-` or `+` leaves the
  member out, and the other members keep their order: `{"isRepo":true,"branch":"main","root":"<F>/-repo","repoSlug":"i/r","defaultBranch":"main"}`.
  `f6010b97` and `89cb6289` do so on Linux and macOS VMs (rows I11a and I11b). The
  general rule behind those two rows is not measured.

- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method (see "The
  daemon's own git environment").
- Since `f6010b97` the git-directory trust check runs on `path`. A refused
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
- `root` is the absolute repo top-level. A subdirectory as `path` gives the same
  `root` (added by reference `7cbfa471`). Since `89cb6289` it is the folder where
  the walk of the trust check found the repository, the one that holds the `.git`
  directory or file. Its spelling is `path` after symlink resolution and the walk
  up. A component that is not a symlink keeps its letter case and Unicode form as
  sent (rows I07a, I07b and I08 on a macOS VM). A symlink resolves before a
  later `..` pops it (row D01). `core.worktree`, and the daemon's `GIT_DIR` and
  `GIT_WORK_TREE`, do not move it. A walk that finds a git directory with no work
  tree, or nothing, answers the non-repo body after `rev-parse --git-dir` (rows I04,
  I05 and T11a). On Linux and macOS no `rev-parse --show-toplevel` runs. On the first
  request, a repository with no `origin/HEAD` takes 9 calls. They are the excludes
  read, then a listing before each of `rev-parse --git-dir`, `remote get-url
  origin`, `symbolic-ref` and `branch --show-current` (row L01).
- On Windows git checks the root. After `rev-parse --git-dir` the daemon runs `git
  --git-dir=<pin> config -z --list` and `git -c … --git-dir=<pin> --work-tree=<walk
  root> rev-parse --show-toplevel`, both in `<pin>`, the `GIT_COMMON_DIR` pin (row
  W01). When git's answer names the same directory as the walk root, the root is
  git's answer as printed. Else it is the walk root with forward slashes (rows W14 to
  W16). A directory symlink resolves in the walk and a junction does not (row D03).
  Some points are not measured. claustrum tests the same directory by file identity
  (`os.SameFile`). That is derived from rows W08 and W09. claustrum takes no
  relative answer. The spelling of the walk root is not measured. With no pin,
  claustrum runs the pair on the git directory of the walk. A path with a junction
  before its last component gets no pin on claustrum. `89cb6289` sets one there (rows
  D03-junction and W09, equal frames). One measured frame differs for
  that reason. In row W09b-in the junction is inside repository P and points into a
  subfolder of repository R. Both sides answer the `root` and `repo` of P and the
  `branch` of R. `89cb6289` answers the `repoSlug` of P, and claustrum that of R.
- The excludes read runs before the trust check, so it also runs before a trust
  refusal, as on both references.
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
  `refs/remotes/origin/HEAD` is unset. The daemon checks the name. A name that
  starts with `-` gives `""` with no further call. Else a light `rev-parse --verify --quiet
  refs/remotes/origin/<name>^{commit}` runs, with its listing, and only exit 0 keeps
  the name. A dangling `origin/HEAD` and one that names a blob give `""` too.
  `f6010b97` and `89cb6289` do so on Linux and macOS VMs (rows I10a to I10c). Not
  measured: other invalid names, and a `symbolic-ref` answer outside
  `refs/remotes/origin/` (claustrum verifies it the same way).

#### git.status
`{path,baseRepo}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}`

- `baseRepo` is required since `7d193f89`. An absent one answers
  `-32602 baseRepo is required`. Status is reported only when `path` passes the gate
  below. Every other request answers `{"isRepo":false,"clean":false}`, which is the
  full shape, unlike `git.info`.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method (see "The
  daemon's own git environment").
- A `baseRepo` that does not resolve answers `{"isRepo":false,"clean":false}`
  before any git call. claustrum resolves it with Go's `filepath.EvalSymlinks`, and
  any error counts. A `..` after a symbolic link goes up from the target of the
  link. Examples are `<dir>/missing/..`, `<dir>/<file>/../T` and
  `<dir>/<dangling link>/..`. Another is `<dir>/<link>/../T` when the parent of the
  link target holds no `T`. These were measured on Linux, macOS and Windows VMs. A
  link loop and a directory with mode 000 were measured on Linux and macOS VMs, and
  a dangling junction on a Windows VM. On Windows a `baseRepo` with a junction
  before its last component, such as `<dir>\<junction>\T`, does not resolve
  (Windows VM).
- Since `f6010b97` the git-directory trust check runs on `baseRepo`, not on
  `path`. A refused git directory answers `-32603` with the refusal text, even when
  `path` is an honest linked worktree. "No repository" answers
  `{"isRepo":false,"clean":false}`. A `GIT_COMMON_DIR` in the daemon's environment
  does not turn the check off here. See
  [Git-directory trust check](#git-directory-trust-check).
- Status honours replace objects (`refs/replace`), as the reference does. A
  replaced `HEAD` commit therefore shows in `changes`.

**The gate.** The git call logs of `89cb6289` show no git call inside `path` before
the gate decides. Its answers follow the worktree entries under the common directory
of `baseRepo`: the request passes when exactly one entry names `path`. Each rule names its rows. The rows are from a side-by-side probe of
`89cb6289` and claustrum on Linux, macOS and Windows VMs. Rows C1 to C13, row n07L
and the `.t` rows are from the macOS VM. Rows D1 to D16, K02 to K15 and W03 to W06
and x1 to x11 are from the Windows VM. Rows y1 to y7 are from Linux and macOS VMs.
Rows y8a to y10, o1 to o25 and v2 to v6b are from the Linux VM. Rows o19a to o19d
and r1 to r4 are from the macOS VM. Rows E07a and E07b ran on Linux and macOS VMs
with a `GIT_COMMON_DIR` in the daemon's environment. The FIFO rows n05a, n14b,
N05a, N05b, C20a and C20b did not run on Windows. Rule 11 names the systems of the
w rows. Every other row ran on all three systems. On Windows the frames are those of the pass with a user excludes file (see
D16 below). The call numbers are those of row K1, which has 22 git calls. Call 1 is
the read of the user's excludes.

1. No git call, `isRepo:false`:
   - `baseRepo` has the components `.claude/worktrees` in it, or is that folder (rows
     n10, n10b1 and n10c1). `X/.claude` alone passes (rows n10b2 and n10c2).
   - `baseRepo` does not resolve (rows n11 and n11c1), as above.
   - A folder one or two levels above `baseRepo` holds an entry named
     `.claude-managed-worktrees` and no `.git` (rows o23, o23c and o23d, Linux VM).
     The entry can be a file or a directory. With a `.git` in that folder the request
     passes (row o23b, Linux VM). A marker inside `baseRepo` itself is ignored (row
     v5). Not measured: more than two levels. claustrum
     refuses them too.
2. The read of the user's excludes comes next. It also runs before a trust refusal
   (rows o20 and o21, Linux VM). On Windows a `baseRepo` that is a dangling junction
   answers `isRepo:false` with this call only (rows n11-j and x11).
3. Calls 2 and 3 run in `baseRepo` as sent: the configuration listing, then the heavy
   `rev-parse --absolute-git-dir`. The answer of call 3 is the common directory.
   - `baseRepo` can be a symlink to the repository, `T/` or `T/sub/..` (rows n11b,
     n11c2 and n11c3). It can be a bare repository, a submodule checkout or a
     subfolder of a repository (rows A03, B08, A04, A05b and N05a).
   - A listing that fails is the hooks refusal (row o22b, Linux VM).
   - A `rev-parse` that fails answers `isRepo:false` (row n17b).
   - Where the trust check finds no repository, both calls carry `GIT_DIR=<null>` in
     place of the `GIT_COMMON_DIR` pin, and the answer is `isRepo:false` (row n10b2).
   - A common directory with no `worktrees` folder answers `isRepo:false` with no
     further call (rows n18 and n18c).
4. Tests on `path`, each `isRepo:false` with 3 calls in total:
   - `path` is a regular file (row n09), or its folder is gone (row n26b).
   - `path` lies inside `baseRepo` and one of its components below `baseRepo` is a
     symbolic link (rows n07 and n07L) or, on Windows, a junction (row n07-j). The
     link in those rows is `.claude`. A link at the last component fails too (row
     v4, and row x10 for a junction). The same layout with no link passes (rows n07b
     and n08).
   - `path` lies inside `baseRepo` and has a `..` component, with or without the
     folder before it (rows x4 and x4b, Windows VM, and rows y3, y4 and y5, Linux
     and macOS VMs). A `.` component passes (row y10, Linux VM). Outside `baseRepo`
     a `..` passes (row y2).
   - On Windows, `path` lies inside `baseRepo` and its last component ends in a dot
     or a space or holds a colon (rows x1, x2 and x3). Outside `baseRepo` a trailing
     dot or space passes (rows D14dot and D14sp). Not measured: another
     component with such a name. claustrum refuses it too.
5. Calls 4 and 5 run in the common directory: `--git-dir=<common> config -z --list`,
   then the light `--git-dir=<common> --work-tree=<dir> rev-parse --show-toplevel`.
   - `<dir>` is `path` with its symlinks resolved (rows n06 and C12). When `path` lies
     inside `baseRepo` or is `baseRepo`, `<dir>` is `baseRepo` (rows A04, A05a, N05a,
     N05b, n07b, n08, n18b and K02b). An empty `path` gives `.` (rows n25d1 and
     n25d2). Not measured: an empty `path` on a daemon whose working directory is a
     linked worktree of `baseRepo`. A relative `path` goes to git as sent, also when it lies inside
     `baseRepo` (rows n25c, x5 and y6). In row x5 git exits 128 on it, and the answer is
     `isRepo:false` after 5 calls. Row y6 gives the same answer on Linux and macOS.
     On Windows `<dir>` has the letter case on disk and the long names, and
     keeps a junction, a `subst` drive and a `\\?\` prefix (rows D3, D6, D8, D13
     and D16).
   - A listing that fails here is the hooks refusal, and `git version` runs after it
     (row o22, Linux VM). Not measured: a listing here that fails with git's "not a
     git repository" text. claustrum answers the hooks refusal for it too.
   - A `rev-parse` that fails answers `isRepo:false` (row n17). Not measured: what
     `89cb6289` does with the output. claustrum does not read it.
   - When the daemon's own environment sets `GIT_COMMON_DIR`, calls 4 and 5 keep
     that value and get no pin (rows E07a and E07b).
6. The entry match. An entry is a real folder under `<common>/worktrees`. A symlinked
   entry does not count (row n02). A real folder that a symlink also points at counts
   once (row n02b).
   - Its `gitdir` file is a regular file of at most 1 MiB (row n05c passes at 1 MiB,
     row n05b fails at 1 MiB + 1). A FIFO does not match, with no wait (row n05a,
     Linux and macOS VMs).
   - A relative value counts from the entry folder (row n03). Its last component is
     `.git` (row n04).
   - The folder before `.git` equals `path` as text. Two spellings of `path` match:
     as sent, and with its symlinks resolved (rows n06 and C12). A relative `path`
     counts from the working directory of the daemon (row n25c). On Windows a `subst`
     drive, a junction, a `\\?\` path, short names, another letter case and a
     trailing dot or space in `path` match too (rows D3, D4, D6, D8, D13, D14dot,
     D14sp and D16, Windows VM). claustrum takes the path that the file system
     reports for the open folder as a third spelling there.
   - For a `path` inside `baseRepo`, claustrum compares two spellings: as sent, and
     the resolved `baseRepo` joined with the part below `baseRepo` as sent. On
     Windows a `path` with short names below a `baseRepo` in short names finds no
     entry (row K02b, Windows VM). Not measured: a `path` inside a `baseRepo` that
     lies below a symlink. The second spelling is claustrum's choice for it.
   - On macOS another letter case, the NFD form and `/System/Volumes/Data` in `path`
     do not match (rows C9, C6, C2 and C3). Another letter case in the `gitdir` file
     does not match on macOS (row C10). On Windows the folder compare ignores letter
     case: one folder name in another case matches, in the `gitdir` file (row x6)
     and in `path` (row x8). The `.git` name does not: a value that ends in `.GIT`
     fails (rows x7 and D9).
   - Exactly one entry matches. Two matching entries answer `isRepo:false` (row n01),
     and so does none (rows n18b and n25). A second entry whose `gitdir` is a
     directory is passed over (row v2).
   - The `.git` inside `path` plays no part. Missing, damaged or naming another
     place, the answer is `isRepo:true` (the G rows, n26c and n26d). A locked
     worktree answers `isRepo:true` (row n26).
7. The `commondir` file of the matched entry is a regular file, or a relative
   symlink to a file in the entry folder (row n19e). An absolute symlink fails (row
   n19f). A file of 1 MiB + 1 fails (row n19g). Not measured: a file of exactly 1
   MiB, which claustrum takes, and a relative symlink that leaves the entry folder,
   which claustrum refuses. A missing file fails (row n19a). A FIFO fails with no wait (rows C20a and C20b, Linux and macOS
   VMs).
   - The value loses the white space at both ends. A relative value counts from the
     entry folder, and the result is cleaned (rows n19c and n19d).
   - It equals the common directory as text, in the spelling of call 3. On macOS a
     value under `/tmp` fails, because git answers `/private/tmp` (row n19b.t).
     Another letter case fails (row C11, macOS VM, and rows D10, W04 and x9, Windows VM).
     On Windows an absolute value with forward or back slashes passes (rows D11f and
     D11b). A short name, a `\\?\` prefix and a value with no drive letter fail
     (rows K05a, K07a, K15a, W05 and W06).
8. The `HEAD` file of the entry holds 40 or 64 hex characters, or a `ref:` line under
   `refs/` (rows o14b, o14c, o14d and o14f, Linux VM). `garbage`, 39 hex characters
   and `ref: x` answer `isRepo:false` (rows n15, o14a and o14e). A branch that does
   not exist and a detached commit pass (rows n16 and n16b).
9. The `index` of the entry. A relative symlink to a file in the entry folder passes
   (row o15a). Not measured: a symlink that leaves the entry folder. claustrum
   refuses it. A FIFO
   answers `isRepo:false` with no wait (row o15b). An `index` of 1 GiB + 1 answers
   `isRepo:false`, and exactly 1 GiB passes (rows o15c and o15e). An `index` of 1 GiB
   beside a `sharedindex.x` of 1 GiB + 1 answers `isRepo:false` (row o16). A missing
   `index` passes (row o15d). All Linux VM. Not measured: whether the bound is for
   each file or for their sum, and a bound on the count of `sharedindex.*` files.
   claustrum bounds each file and not the count.
10. The `config.worktree` file of the entry. A missing file passes. A folder, a FIFO
    and a syntax error answer `isRepo:false`, with no wait on the FIFO (rows n14,
    n14b and n14c).
    - The daemon runs `git config --no-includes --file - --list -z` with the file on
      stdin. The call runs in the daemon's working directory with the daemon's
      environment (rows n12, n12b, n13 and n14c). It is call 6 there.
    - A file with `core.sparseCheckout` or `index.sparse` passes (rows n12 and n12b),
      and so does one with `core.sparseCheckoutCone` (rows o17 and v3a). `user.name`
      and `core.bare` answer `isRepo:false` (rows n13 and v3b). Not measured: every
      other key. claustrum answers `isRepo:false` for each of them.
11. The `reftable` folder of the entry, in a repository with the reftable format
    (rows o19a to o19d and r1 to r4, macOS VM).
    - Every regular file of the folder goes into the temporary folder, also a table
      that `tables.list` does not name (row r1).
    - An entry that is not a regular file is left out. With a FIFO as `tables.list`
      the daemon does not wait, copies no list, and answers the status. git then
      shows every tracked file as added (row r2).
    - A `tables.list` that is copied names at least one table (row r4), and each
      name is a file that is copied. Else the answer is `isRepo:false` after 5
      calls: for a missing table (rows o19c and o19d) and for a table that is a
      symlink (row r3).
    - Not measured: a size bound of a table or of the list, a folder inside
      `reftable`, and a symlink that the list does not name. claustrum bounds a
      table at 1 GiB and the list at 1 MiB, and leaves the other two out.
    - A `reftable` that is not a real folder counts as none. Nothing of it is
      copied, and the request goes on. That holds for a symlink to a folder outside
      the entry (row w1, Linux and macOS VMs, and row w1b, macOS VM) and for a
      regular file (row w2, Linux, macOS and Windows VMs). On Windows it holds for
      a junction and for a directory symbolic link (rows w1 and w1s, Windows VM).
      In a reftable repository git then shows every tracked file as added (rows w1
      and w1b, macOS VM).

Not measured: the order of rules 7 to 11, and the size bound of `HEAD` and
`config.worktree`. claustrum bounds both at 1 MiB. No read of an entry file blocks:
claustrum opens each file without blocking and reads a regular file only.

**The answer.**

- The temporary git folder holds copies of `HEAD` and `index` of the entry and a
  `commondir` file (row K1). It also holds `config.worktree` when the entry has one
  (rows n12 and o17), `info/sparse-checkout` (row o17, Linux VM) and each
  `sharedindex.*` file (rows o16b1 and o16b2). In a reftable repository it
  holds the regular files of the entry's `reftable` folder (rows o19a and r1, macOS
  VM). Not measured: an `info/sparse-checkout` that is not a regular file, such as
  a FIFO. From the code: claustrum does not wait and answers `-32603` (see the
  failures below). No entry is left after the reply. The status therefore does not refresh or rewrite the caller's index, and it
  does not take `index.lock`. The folder name starts with `claustrum-git-dir-`.
- After the gate comes the `git --attr-source=<empty tree> version` probe (call 6).
  From the code: claustrum runs it once for each daemon. Each measured request was
  the first of its daemon. Then four commands run, each with `path` as its working directory
  (calls 10, 14, 18 and 22):
  `status --porcelain --untracked-files=all --ignore-submodules=all`,
  `ls-files -s -z`, `rev-parse --verify -q HEAD^{commit}` and
  `diff-index --cached --raw -z --ignore-submodules=none HEAD --`.
- Three calls come before each command (calls 7 to 9). Two run in the working
  directory of the daemon: `--git-dir=<common> config -z --list` and the heavy
  `--git-dir=<common> hash-object -t tree <null>`. The third runs in `path`:
  `--git-dir=<temp> config -z --list`. When the daemon's own environment sets
  `GIT_COMMON_DIR`, the two `<common>` calls keep that value. The third call and the
  command get `GIT_COMMON_DIR=<common>` (rows E07a and E07b).
- Each command carries `--attr-source=<id>`, the heavy profile,
  `--git-dir=<temp>` and `--work-tree=<path as sent>`. `status`, `ls-files` and
  `diff-index` add `GIT_OPTIONAL_LOCKS=0`. The `--attr-source` option makes git ignore
  the `.gitattributes` of the repository, so no clean filter runs. It is left out
  when the runtime git predates it (git 2.40). `<id>` is the answer of the
  `hash-object` call right before the command. In a SHA-256 repository it has 64 hex
  characters (rows v6a and v6b). The probe keeps the SHA-1 id there. When
  `hash-object` fails, `<id>` is the SHA-1 id of the empty tree (row n17f).
- When `rev-parse --verify` fails, `HEAD` names no commit. The two `<common>` calls
  run once more, and `diff-index` gets the id of the empty tree in place of `HEAD`
  (rows n16, o13 and o14d, 24 calls).
- Failures. A `status`, `ls-files` or `diff-index` that fails answers `-32603` with
  the Go error string, for example `exit status 1` (rows n17c, n17d and n17e). The
  text of git does not show for these three commands. A `hash-object` that fails changes no answer (row n17f).
  A relative `path` passes the gate. git gets it as sent, `status` exits 128, and
  the answer is `-32603 exit status 128` (row n25c). A `path` outside `baseRepo`
  such as `W/missing/..` passes the gate cleaned. git then does not start in the
  path as sent. The answer is `-32603` with the hooks refusal text and
  `chdir <path>: no such file or directory`. No `git version` follows the refusal
  (row y1, Linux and macOS VMs). With opt-in D5 the same
  `-32603` can carry `signal: killed`.
- claustrum's own `-32603` texts, from the copy into the temporary folder after the
  gate passed. The reference is not measured in any of them. `<file>: not a regular
  file` answers an `info/sparse-checkout` that is a folder or a FIFO.
  `<file> is larger than <n> bytes` answers a copied file over its bound, also one
  that grew after the gate. A file that cannot be copied
  and a temporary folder that cannot be made answer the raw Go error, for example
  `openat index: permission denied`.
- Not measured: a failure of the listing in the temporary folder. claustrum answers
  the hooks refusal, as for the listing of rule 5.
- `changes` starts with the stdout lines of `status`. Stderr warnings never appear.
  Each line is verbatim minus the line ending, the first one included. The XY column
  is positional, so the leading space of an unstaged change is data: `"M  a.txt"`
  is staged and `" M b.txt"` is unstaged. `--untracked-files=all` lists an untracked
  file inside an untracked directory by itself (`"?? sub/u.txt"`).
- An entry longer than 512 bytes is cut to 512 bytes of the whole line, and `…`
  follows (row o1, Linux VM). At most 10000 `status` entries pass, and no entry
  marks the cut (row o2, Linux VM). Submodule entries still follow (row o3). Not
  measured: a cut in the middle of a multi-byte character. Row o1 used an ASCII path.
  claustrum's own behavior there: the cut can split a character, which is possible
  with `core.quotePath=false`, and no profile pins that key. The JSON encoding then
  sends U+FFFD for each leftover byte. The reference is not measured there.
- Submodule entries follow the `status` lines. Each is ` S `, the quoted name and one
  of three texts. All rows below are from a Linux VM. Rows n20 to n20e ran on macOS
  and Windows VMs too.
  - `ls-files` shows each index entry of mode 160000. For each one the daemon looks
    at `<name>/.git` inside the work tree. It follows no link out of the work tree. Not there: no
    entry (rows n20e and o6d). There, as a file, a folder or a dangling symlink:
    ` (submodule present; contents not inspected)` (rows n20, o6a, o6b and o6c). A
    gitlink path that is a symlink to a folder inside the work tree counts as
    present (row o5b).
  - If it cannot be looked at, the text is ` (submodule; could not be inspected)`.
    Three gitlink paths are measured: a regular file (row o4), a symlink to a
    folder outside the work tree (row o5), and a folder of mode 000 (row o5c).
  - There is one entry for each index stage, so a conflicted gitlink gives three
    (row o12).
  - For each `diff-index` record whose old or new mode is 160000, the text is
    ` (submodule change staged; contents not inspected)` (rows o7, o8, o9, o9b, o9c
    and o13).
  - The order is the `status` lines, the `ls-files` entries, then the `diff-index`
    entries (rows o7, o8, o9, o10 and o12). One gitlink can have all three.
  - A name longer than 200 bytes is cut to 200 bytes, and `…` follows. The name is
    then quoted with double quotes, `\"`, `\n`, `\t`, and `\xff` for a byte that is
    not valid UTF-8. A printable character outside ASCII stays (row o11). Not
    measured: a cut in the middle of a character, and a name with `<`, `&`, `>` or
    U+2028. claustrum's own behavior for the cut: it can split a character, and the
    quoting then writes each leftover byte as `\xNN` text. The reference is not
    measured there.
  - At most 100 submodule entries pass. Then one entry gives the count of the rest:
    ` S … (+20 more submodule entries)` (row o10, and row n20d with 1).
- `clean` is true only for an empty `changes`.
- Windows divergence D16. On Windows, when the user has no global excludes file,
  `89cb6289` passes `-c core.excludesFile=NUL` to its `status` call. git 2.55 exits
  128 on that value, and the reference answers `-32603 "exit status 128"` (pass A of
  the Windows rows). claustrum passes `/dev/null` to its `status`, `ls-files` and
  `diff-index` calls there and returns the status. With a user excludes file the
  Windows rows equal the Linux rows (pass X). See [DIVERGENCES.md](DIVERGENCES.md)
  D16.
- On Windows a gitlink path that is a regular file gives no entry (row o4, Windows
  VM). A gitlink path that is a junction gives the "could not be inspected" text
  (row o5b, Windows VM). The o rows ran on the Windows VM too, except o5c, o15b,
  o19 and o20.

#### git.list_branches
`{path}` → `{"isRepo":true,"branches":[…sorted…]}`
- Non-repo → `{"isRepo":false,"branches":[]}`.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method (see "The
  daemon's own git environment").
- A `path` inside a managed worktrees directory answers
  `{"isRepo":false,"branches":[]}` before any git call. That is a path beneath
  `.claude/worktrees`, or beneath a directory that holds a
  `.claude-managed-worktrees` marker. Linux, macOS and Windows VMs. claustrum also
  runs this test on `baseRepo` (not measured).
- A `path` that is not empty and does not resolve answers
  `{"isRepo":false,"branches":[]}` before any git call. The rule is the one that
  `git.status` applies to `baseRepo`. Examples are a missing `path` (Linux, macOS
  and Windows VMs) and `<dir>\missing\..` (Windows VM). On Windows a `path` with a
  junction before its last component, such as `<dir>\<junction>\T`, does not
  resolve (Windows VM).
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
- A failure frame ends with `"branchKept":true` after `errorCode` when the rollback
  check finds commits that no other ref reaches. Rows C02, C04, C05, C09, C10 and
  B2-10 of `89cb6289` show it. A check that does not finish, and a skipped name, add
  no member. The table in [The branch step](#the-branch-step) gives each case.
  The member is only ever true, and absent otherwise. No measured frame with it has `sourceBranch` or `branch`. claustrum
  puts it after them. That is claustrum's choice (not measured).
- The repo is `baseRepo`, not `path`. When `baseRepo` is absent, the daemon uses
  its cwd repo.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method (see "The
  daemon's own git environment").
- On Windows a junction at `.claude` or `.claude\worktrees` fails the parent step:
  `{"success":false,"error":"failed to create parent directory: <repo>\\.claude is not
  a directory","errorCode":"mkdir_failed"}`. Nothing
  is created. Measured against `f6010b97` on a Windows VM (rows JCR1 and JCR2). See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D19.
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
  fallback runs the branch step on `<branchName>`, because the call created it. The natural trigger
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
  only ever created in a fresh directory"`. Take Windows without `worktreeRoot`.
  There the `<p>` of that last text has the on-disk letter case of each component
  that exists (row W15, both references). claustrum keeps the volume name and any
  8.3 short name as sent. Neither is measured. The case rule with `worktreeRoot` is
  not measured. `<repo>` is `baseRepo` as sent. An
  absent `baseRepo` gives the empty string, so a space comes before the semicolon
  (`f6010b97`, Linux and macOS VMs). The recommended location is
  `<repo>/.claude/worktrees/<id>`, but the enforced rule is only containment in the
  repo. An empty `worktreePath` is `{success:false,error:"failed to create parent
  directory: \"\" does not name a directory",errorCode:"mkdir_failed"}`. The daemon
  creates the parent directory before the add, so a nested path succeeds on a fresh
  repo. A `worktreePath` with a trailing slash, `//` or `/./` succeeds too. `path`
  and each undo text below quote `worktreePath` exactly as sent. Measured against
  `f6010b97` and `90fca6e6` on Linux and macOS VMs. On Linux and macOS a directory
  that the create makes above the leaf asks for mode 0755, and the leaf asks for
  0777. The umask applies. An existing directory keeps its mode. A directory that
  the call made before a later failure stays. The texts of this step name the repo
  with its symlinks resolved. Measured against `f6010b97` on Linux and macOS VMs.
  Both VMs ran umask 0000, which tells a leaf request of 0777 from 0775.
- `worktreeRoot` is the `external_root` capability. When the client supplies
  `worktreeRoot`, the worktree is placed OUTSIDE the repository, at
  `<worktreeRoot>/<directory>/<name>`, exactly two levels under the root. On
  Windows this capability is gated off. Any `worktreeRoot` is refused before any
  location test with `"refusing to {create,remove} worktree: <root> cannot be used:
  a custom worktree location is not supported on Windows hosts yet"`. The
  `errorCode` is `"unsafe_path"` on create, and there is none on remove. On unix
  the in-repo containment above is replaced by the tests below. A refusal among
  them has `errorCode:"unsafe_path"`. The error table gives the code of each
  failure. `worktreeRoot` and `worktreePath` must be absolute and
  `..`-free, and `worktreePath` must sit exactly two levels under the root
  (`"<p> is not <worktree location>/<directory>/<name> beneath <root>"`).
  `baseRepo` must also be absolute and carry no `..` component. The refusal uses
  the `worktreePath` texts and names `baseRepo` as sent. An absent `baseRepo` is a
  relative path with the empty string as its name. `f6010b97` sends this refusal
  after the repo test, and nothing is created (rows C7, C8 and C9 on Linux and
  macOS VMs). claustrum tests it after the `worktreePath` spelling and before the
  two-level test, as on remove. That order is not measured. Three tests refuse a
  root in a checkout of the repository. claustrum runs them after the two-level
  test, as on remove. That order is not measured on create. The first test
  compares the cleaned root with the cleaned `baseRepo` by whole components. Then
  the daemon makes the git calls that "Hardened git calls" lists. The second test
  refuses a root that leads into the git top level of `baseRepo` or into the main
  checkout. The third refuses a root in a linked worktree of the repository. A
  root that holds the repository passes, and so does `<B>/Tx` beside `<B>/T`. The
  three tests come before the root-chain tests, and nothing is created. The error
  table gives the texts. Measured against `f6010b97` and `89cb6289` on Linux and
  macOS VMs (rows K1 to K4, K6 to K9, E2, W3, W4 and Y11a to Y11c). After the
  git calls and before the second test, the ancestor test judges the directories
  above the root. A directory owned by a user other than the daemon's user or
  uid 0 refuses the create. So does a directory with the other-write bit, or
  with the group-write bit and a shared group, when the sticky bit is off. The
  error table gives the texts, the rules and the limits. Measured against
  `f6010b97` and `89cb6289` on Linux and macOS VMs (rows K1, G1 to G41b, Y11d,
  T7 and T7c). G42 and the macOS round 2 rows are measured against `89cb6289`
  only. Next, the
  daemon resolves the symlinks of the root. It tests each directory from `/` down
  to the root for a `.git` entry, top first. These root-chain tests come before
  the tests of the root's owner and write access. The symlinked `<directory>`
  test comes next, then the `<directory>`-level tests, then the non-empty and
  existing-leaf tests.
  The error table above gives each text. A `.git` entry or a symlink loop refuses
  the create with `errorCode:"unsafe_path"`. Measured against `f6010b97` on Linux
  and macOS VMs, texts and order. A Linux VM measured the `<directory>` file
  test after the writable-root and foreign-owner tests, and the search test after
  the writable-root test. Linux and macOS VMs measured the symlinked `<directory>`
  test before the `<directory>`-level tests. The root must be owned by
  the daemon's user (`"<root> is owned by uid <o>, not by you (uid <u>); …"`). The root must not be
  writable by its group or by every user on the host
  (`"<root> is writable by <who> (mode <perm>); … chmod go-w"`). The group-write
  bit counts unless the group is the private group of the daemon's user. For
  this test the answers follow only the flat files `/etc/passwd` and
  `/etc/group`. Members and primary-group users in the macOS directory service
  do not count. A directory-service group alone is not enough. A stock macOS
  user has no `/etc/passwd` line, so there every group-writable root is
  refused. A group that counts as shared on a root with mode 0777 gives the
  `<who>` text "its group and every user on this host". These texts, the four
  tests and the line format below were measured against `f6010b97` on Linux and
  macOS VMs, except where a line says otherwise.
  - A private group passes four tests:
    - Its gid is the daemon's gid.
    - `/etc/passwd` has exactly one line with that gid as its primary gid. That
      line must be the daemon user's line. Only Linux VMs measured this.
      Whether it is found by uid or by name is not measured.
    - At least one `/etc/group` line has that gid, and every such line has the
      name of that user.
    - No such line lists a member other than that user.
  - The account-file lines:
    - In both files a line that starts with `#` is skipped.
    - A `/etc/passwd` line with 6, 7 or 8 fields is read.
    - A passwd name with a leading space does not match the user (Linux only).
    - A `+name` line is not skipped.
    - A leading space and a trailing CR around a group member are trimmed, and
      an empty member is ignored.
  - Not measured:
    - claustrum uses the effective gid, and no capture told the real and the
      effective gid apart.
    - A file that cannot be read makes the group shared.
    - A `/etc/passwd` line with fewer than 6 or more than 8 fields is skipped.
    - A `/etc/group` line with other than 4 fields is skipped.
    - A line with a non-numeric uid or gid is skipped.
    - Two identical group lines for the user pass.
    - A member is also trimmed of trailing spaces. No other field is trimmed.
    - A user known only to a Linux NSS source, such as LDAP, counts as shared.

  The `<directory>` level must not be a symlink. Unless it is already marked, it
  must also start out empty (`"<dir> already exists, is not marked as a worktree
  directory, and holds other files (for example \"<name>\"); … must start out
  empty …"`). These two tests take `<directory>` from the cleaned
  `worktreePath`, so for `R/proj/w1/` the refusal names `R/proj`. With
  `worktreeRoot`, the "already exists" refusal also quotes the cleaned path, for
  example `R/cp/w1` for `R/cp/w1/`. Measured against `f6010b97` and `90fca6e6`
  on Linux and macOS VMs. With `worktreeRoot`, each directory that the create
  makes above the leaf asks for mode 0700. That covers every missing directory
  from the highest one down to `<directory>`. After the parent step and before
  the add, the daemon writes a 285-byte `.claude-managed-worktrees` marker at
  the `<directory>` level if no marker exists. An existing entry of that name
  keeps its content and its mode. A marker that cannot be created for another
  reason stops the create with `errorCode:"mkdir_failed"`, and the leaf is not
  made. A failed add keeps the marker. A directory that the call made before a
  later failure stays. Measured against `f6010b97` on Linux and macOS VMs.
  Independently, a `baseRepo` that itself sits under a managed-worktrees marker
  is refused
  `{success:false,error:"baseRepo is inside a managed worktrees directory …",errorCode:"nested_base_repo"}`.
  The same frame answers a `baseRepo` that fails claustrum's own trust-root test, as
  in `git.worktree_remove`. No git runs, and nothing is created. Linux and macOS VMs
  measured that against `f6010b97` in rows G1c, G2c and G4c. The Windows VM measured
  it in round 1 row C1 and in round 2 rows K1, D1, D2, D4, J1 and J3. The refusal
  comes before the check of the daemon's `GIT_CONFIG_COUNT` (round 1 row C1 P1 on the
  Windows VM).
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
    `.git/worktrees/` directory stays. A retry at the same path then succeeds with a
    new `branchName`. It succeeds with the same one only when the rollback deleted
    the branch.
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
  byte for byte. The daemon empties the new directory and removes the worktree's
  registration in the main repository's `.git/worktrees/`. Then it runs the branch
  step on the branch that the call created, and then it removes the empty
  directory. The rollback's git calls are those of the branch step. The `.git/worktrees/`
  directory itself stays, so the first linked worktree's failed checkout leaves it
  empty. With a linked worktree as `baseRepo`, that worktree's own entry stays,
  and a retry at the same path fails the same way. In attach mode the attached
  branch is kept. These end states are measured against `f6010b97`. There the
  branch and its reflog always go. Since `89cb6289` they go only when another ref
  reaches the tip of the branch.
- The checkout is `read-tree -u --reset --no-recurse-submodules <rev>`, after the
  hardening `-c` options and `-c core.splitIndex=false -c core.commitGraph=false`.
  It runs with the new worktree as its working directory, and it passes no `-C`.
  `--git-dir` names the git dir of `baseRepo`, and `--work-tree` names the new
  worktree. For a linked-worktree `baseRepo`, the git dir is that worktree's own
  admin dir. The daemon gets it with `rev-parse --absolute-git-dir` before the add.
  The index goes to a file in a new temporary directory, named by `GIT_INDEX_FILE`.
  Its config precursor, `--git-dir=<git dir> config -z --list`, runs in
  the new worktree too. Measured against `f6010b97` and `90fca6e6` on a Windows VM.
  On Linux and macOS, `--work-tree` names the new worktree with its symlinks
  resolved, as on `f6010b97` and `89cb6289` (rows D02, I01c, I01e and CSc). Those
  rows are creates without `worktreeRoot`. A symlinked `worktreeRoot` is not
  measured. On Windows claustrum passes the path as sent. The resolved form is not
  measured there.
- Before the checkout the daemon reads the new worktree's `.git` file, the admin
  directory it names, and that directory's `gitdir` record. It compares the record
  with `<worktreePath>/.git` byte for byte, with `worktreePath` taken after symlink
  resolution. If they differ, the create answers `{"success":false,"error":"refusing to create
  worktree: <worktreePath> carries a .git file naming an admin directory whose own
  record is of a different worktree","errorCode":"unsafe_path"}`, runs no checkout
  and rolls nothing back. The leaf, the entry and the branch stay. Before the answer
  it runs the `rev-parse --show-toplevel` pair with `--git-dir` and `--work-tree=<baseRepo>`
  in the git dir. On a macOS VM both references refuse an NFC folder sent in NFD,
  because git records the path in NFC (row I07a). An NFD folder sent in NFC succeeds
  (row I07b). The raw bytes of the record are not measured. The byte compare is
  inferred from I07a and I07b. The symlink resolution is claustrum's choice: a
  `/tmp` path creates as usual on both references (row I01e). Linux is not measured.
  claustrum checks there too. On a Linux VM the creates of rows CSa to CSd succeeded
  on both references and on claustrum. The check is off for a relative path or
  record, and on Windows. That is claustrum's choice (measured on macOS only).
  After git exits 0, claustrum moves that index into the worktree's registration
  and removes the temporary directory. The registration of a reference create
  holds an `index` file too. A process that the checkout leaves behind starts in
  the new worktree. On Windows it then blocks the removal of the leaf, and the
  rollback reports it with the undo text below.
- Every rollback after a successful add runs four steps. This covers the checkout
  failure and each `timeout` frame. Steps 1 to 3 and their texts were measured
  against `f6010b97` and `90fca6e6` on a Windows VM. Step 4 follows `89cb6289`:
  1. Delete the entries at the top of the leaf, one at a time, in the order that
     the directory read returns them. The names are not sorted. Stop at the first
     entry that cannot be deleted. Then append `; and the undo could
     not finish for <leaf>: the worktree directory, its registration, and the
     branch all remain; remove them by hand before retrying (RemoveAll <entry>:
     <OS error>)` to the error, and undo nothing else. No branch step runs (row
     B2-11). In attach mode the call made no branch, and the text reads `the
     worktree directory and its registration both remain` (row C08b).
  2. Delete the registration. Then run the branch step on the branch that the call
     created. In attach mode no git call runs (rows C08 and C08b).
  3. Remove the leaf directory, which is now empty. If that fails, append `; and
     the undo could not finish for <leaf>: the worktree directory remains
     (re-populated while undoing?); remove it by hand before retrying (removeat
     <leaf base name>: <OS error>)`.
  4. If the branch stays, append its text after `; and the undo
     could not finish for <leaf>: `. After a step 3 failure, the step 3 text comes
     first, and the two parts are joined by `; `. See [The branch
     step](#the-branch-step).

  The `errorCode` does not change. `<leaf>` is `worktreePath` exactly as sent, and
  `<entry>` is a name at the top of the leaf. With a trailing slash on
  `worktreePath`, the `removeat` part still names the base name, such as `w1`. The
  step 1 order was measured against both references on Linux ext4 and macOS APFS
  VMs. The three wordings are fixed, and only `<OS error>` varies. On Windows the measured causes were an open handle, a
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
  ignore. A runtime-state path (see below) or a nested repository does not go
  to check-ignore. `f6010b97` drops the runtime-state paths on Linux and macOS
  VMs (I15c, I15d). A Linux VM measured the nested-repository rule in this scan
  (D16, with the old scan forced and with git 2.25.1). If every path is dropped,
  no check-ignore call runs. That case was not measured.
  If either call fails, nothing is copied. The full scan searches every ignored
  directory.
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
    matches takes no place. `.claude/worktrees/` never opens, even when a
    pattern names it.
  - The root `.claude/` opens by the rules above. It does not become one
    pathspec. Its children take its place in the directory batch, in the order
    of the directory read. `worktrees` is left out in any case. This is the list
    of the `.claude/` pass below. So a root `.claude/` that holds only
    `worktrees` adds no pathspec. `f6010b97` does the same on Linux and macOS
    VMs (C02, C03, C05, D23, I14b, I15d). The Windows VM shows it too
    (`Cl_anydepth`).
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
  - Three kinds of directory path do not go to check-ignore. The first is a
    path that the file batches also print. The second is a nested repository,
    which ends in `/`. The third is a Claude runtime-state path. If no path is
    left, no check-ignore call runs. `f6010b97` sends the same paths on Linux
    and macOS VMs. The first rule comes from A03 to A05, A14 and A16 to A18. The
    second comes from D16. The third comes from C02 to C05 and I15c.
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
  runs `git config -z --list`, as it does before most hardened
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
`{baseRepo,worktreePath[,branchName][,worktreeRoot]}` → `{"success":true}` (lenient), or `{"success":true,"branchKept":true}` when the branch step keeps the branch

- Since `f6010b97` the git-directory trust check runs on `baseRepo` only, before
  git runs. Without `worktreeRoot` it runs after the containment tests, the
  `.claude` look and the leaf check below. When the worktree directory exists, a
  refused git directory and "no repository" both answer the lock-check refusal:
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
  1. Git cannot start in `baseRepo`. It is a regular file, a directory without
     search permission (mode 0600), or a path that the kernel does not follow.
     `<reason>` is the hooks refusal with the
     start error, for example `…listing the configuration in force: fork/exec
     /usr/bin/git: permission denied` or `…: not a directory`. The path is the git
     that `PATH` resolves. `git version` runs after that listing (`89cb6289`, row
     L16b). If it fails too, the text starts with `git cannot run on this host`
     instead, which is not measured here. A `baseRepo` behind a chain of 45
     symlinks, which the kernel does not follow, gets this reason too. Its text ends
     with `chdir <baseRepo>: too many levels of symbolic links`, and nothing is
     deleted (`89cb6289`, rows LNKb-g and LNKr-g on a Linux VM). Not measured: chain
     lengths other than 5 and 45, absolute link targets, a chain to a plain folder
     and a chain to nothing. Nor are a chain with a locked or marked worktree, a
     chain without `worktreeRoot`, and a path that is too long for the kernel.
  2. The trust check refuses the git directory of `baseRepo`. `<reason>` is the
     refusal text M2 to M5 or S1 to S3. A plain folder as the target gets this text
     too.
  3. The trust check finds no repository. `<reason>` is `exit status 128`. Examples
     are a plain directory, an empty nested `.git` directory, a `.git` file with no
     `gitdir:` line, and a linked worktree whose entry is gone. A `.git` file whose
     target is gone and is not a worktree entry goes on to the next step instead.
  4. Git cannot list the configuration. `<reason>` is the refusal text of the
     failure class. When the listing says git finds no repository, `<reason>` is `git
     finds no repository here: <git text>`. The `.git` file with a gone target gets
     `git finds no repository here: fatal: not a git repository: <target>` here
     (`89cb6289`, row N04 on Linux and macOS VMs). `f6010b97` gave the hooks refusal
     text there.
  5. `baseRepo` is itself a git directory, such as a bare repository or a `.git`
     directory. `<reason>` is `exit status 128`. A repository whose config sets
     `core.bare` still passes.
  6. The hardened `git rev-parse --absolute-git-dir` fails in `baseRepo`. `<reason>`
     is its exec error, for example `exit status 128`. A daemon `GIT_DIR` that names
     nothing is one example.

  With no `git` on `PATH`, claustrum gives a `baseRepo` that exists one reason
  before these six. It is the exec error alone, `exec: "git": executable file not
  found in $PATH`. Both references answer so in nine rows. One is a git directory
  that the trust check refuses (`89cb6289`, row L13t on a Linux VM). Class 1 of the
  failed listing gives the other rows and the place of the `GIT_CONFIG_COUNT` check
  (see [Git-directory trust check](#git-directory-trust-check)). The other examples of
  reason 3 are not measured with no `git`.

  A `baseRepo` that does not exist gets no work-tree refusal, and no git call runs
  for this step. That holds with `git` on `PATH` and without it (`89cb6289`, Linux
  VM). The removal then goes on. Each request sent a `branchName`. The answer
  depends on the worktree directory:
  - It is gone, and the `<directory>` level exists (row L13wb), or the root exists
    and is empty (row L13wc): `{"success":true,"branchKept":true}`. The twins with
    `git` answer the same.
  - It is gone, and the root does not exist (row L13wr): the "is not reachable"
    refusal. The twin with `git` answers the same.
  - It holds a file and no `.git` file. With `git` it gets the "is not a worktree of"
    refusal (row L13wa-g). With no `git` it gets `could not check whether
    <worktreePath> is locked (the repository at <baseRepo> could not be read); retry`
    (row L13wa).
  - It is empty. With `git` the directory is removed, and the answer is success with
    `branchKept` (row L13we-g). With no `git` the lock-check text above answers, and
    the directory stays (row L13we).
  - Its `.git` file names an entry of the missing repository, and it holds other
    files. With `git` the directory is deleted, and the answer is success with
    `branchKept` (row DG1-g). With no `git` the lock-check text above answers, and
    the directory stays (row DG1). `f6010b97` does the same, with no `branchKept`.

  Any other stat error of `baseRepo` keeps the six reasons and the no-`git` answer.
  claustrum runs no `GIT_CONFIG_COUNT` check then (not measured).
  A `baseRepo` that is not a directory gets reason 1. Each of the six reasons and
  the order around this step were measured side by side against `f6010b97` on a
  Linux VM.
- By default, with no `worktreeRoot`, `7d193f89` confines the removal to inside the
  repository. `worktreePath` must be absolute, carry no `..` component, and sit
  strictly under `baseRepo`. Otherwise the reply is `{"success":false,"error":"refusing
  to remove worktree: <p> …"}`, with no `errorCode`, and with the same three reasons
  as `worktree_create`. The "not inside the repository" text names `baseRepo` as
  sent. An absent or empty `baseRepo` gives the empty string, so a space comes before
  the semicolon (`f6010b97`, Linux and macOS VMs). A Windows VM measured an absent
  one. An empty `worktreePath` is
  `{"success":false,"error":"failed to remove worktree: \"\" does not name a
  directory"}`. Only a path that passes these tests reaches the delete below.
- With `worktreeRoot`, the path must sit two levels under that root, under the same
  external containment as `worktree_create`. An empty `worktreePath` is a relative
  path there. After the `worktreePath` checks and before the two-level check,
  `baseRepo` must be absolute and carry no `..` component. The refusal uses the
  `worktreePath` texts and names `baseRepo` as sent, for example
  `{"success":false,"error":"refusing to remove worktree: <baseRepo> contains a \"..\" component; choose the session folder by its absolute path, without \"..\""}`.
  An absent `baseRepo` is a relative path with the empty string as its name. No git
  runs before these refusals. Under a refused daemon `GIT_CONFIG_COUNT`, a `..`
  `baseRepo` still gets the `..` text. Measured against `f6010b97` on Linux and
  macOS VMs. The `..` test is by whole component: `<T>/sub/..` is refused, and
  `<B>/d..d/T` and `<T>/.` pass (Linux and macOS VMs).
- With `worktreeRoot`, four tests refuse a root in a checkout of the repository, or
  a root that does not exist. Nothing is deleted, and the error table gives the
  texts. The first test runs after the two-level check and before any git call. It
  refuses a root whose cleaned path is the cleaned `baseRepo` or lies beneath it by
  whole components. It does not look at the worktree, so a missing worktree gets
  the same answer. The other three tests run after the `worktree list` call, in
  this order. The second refuses a root that leads into the git top level of
  `baseRepo` or into the main checkout. The third refuses a root in a linked
  worktree of the repository. The fourth refuses a root that does not exist or does
  not resolve.
  claustrum runs the `<directory>` symlink check before these three. That order is
  not measured. A root is not refused for holding the repository or a linked
  worktree (rows Q15s and Y7). Neither is a root in a checkout of another repository beside it (rows
  Q20 and Q20b). Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs
  (the Q, E, W and Y rows of the R0 runs). If `worktree list --porcelain -z` fails
  without a D5 kill, the daemon runs one more listing and `worktree list
  --porcelain`. If that call fails too, the answer is `failed to remove worktree:
  cannot list the repository's worktrees: <exec error>`, and nothing is deleted.
  `89cb6289` answers so with `exit status 128` (row DG2s-g on a Linux VM). If the
  second call succeeds, claustrum reads its lines and the tests run as usual. That
  case is not measured. A git older than 2.36 has no `-z` form. A `baseRepo` that
  does not exist runs neither call.
- The managed-worktrees refusal skips a `baseRepo` that does not exist. Measured
  against `f6010b97` on a macOS VM.
- claustrum's own trust-root test also gives the managed-worktrees refusal. It fails a
  `baseRepo` that `os.Stat` does not report as missing and that Go's
  `filepath.EvalSymlinks` cannot resolve. The test is fitted to the measured rows of
  `f6010b97`, and every measured row fits it. On Linux and macOS, rows G1, G2 and G4
  got the refusal. They send `<T>/a.txt/..`, `<T>/loop/..` (a link to itself) and a
  path through a directory with mode 000. There `os.Stat` fails with ENOTDIR, ELOOP or
  EACCES. On Linux and macOS, `<T>/missing/..` and `<T>/dl/..` (a dangling link) are
  missing, and skip the test (rows A1 and G3). On Windows `<T>\missing\..` stats,
  because Windows removes `missing\..` by name, but it does not resolve. The Windows
  VM rows K1, D1, D2, D4, J1 and J3 fit this rule too. They put a missing name, a
  regular file or a dangling directory symlink before `..`, or a junction mid-path. No
  git runs, and nothing is deleted. The refusal comes before the `worktreePath`
  checks, the `worktreeRoot` refusal of Windows and the check of the daemon's
  `GIT_CONFIG_COUNT`. Only the Windows VM measured that order (round 1 rows O1 to O4,
  WR1 and A1 P1).
- The daemon runs no `git worktree remove` and no `git worktree prune`. It deletes the
  worktree directory itself, then the entry of the worktree under `<git dir>/worktrees`.
  `<git dir>` is the repository git directory that the trust check pinned for
  `baseRepo`. For a main repository that is `<baseRepo>/.git`. For a linked worktree
  it is the git directory of the main repository. For a subdirectory of a repository
  and for a submodule it is the git directory of that repository. With a daemon
  `GIT_DIR` alone, it is the directory that `GIT_DIR` names. Row E13 sets `GIT_DIR`
  and `GIT_COMMON_DIR` to the `.git` of another repository. There `f6010b97` and
  `89cb6289` keep `<baseRepo>/.git/worktrees/<name>`, and claustrum deletes it
  (Linux, macOS and Windows). That is an open gap on issue 429. The `worktrees`
  directory itself stays. After its last entry goes, it stays as an empty
  directory. The rules below were measured side by side against `f6010b97` on
  VMs. On macOS all 107 cases are equal. On Linux every remove case is equal. On
  Windows every case is equal except the junction rows of D19. Row E13 above is
  a later exception on all three systems. The full Windows set was measured on an earlier build,
  and the fixed rows again on the shipped one. A sentence marked "not measured" has
  no row behind it.
- On Windows, a junction at `.claude` or at `.claude\worktrees` is refused. Nothing
  is deleted, and the branch stays. The reply is `{"success":false,"error":"failed to remove
  worktree: openat .claude\\worktrees: path escapes from parent"}`. That text is
  claustrum's own. `f6010b97` answers `{"success":true}` there, deletes nothing,
  and deletes the branch. The whole-build check of issue 442 measured `89cb6289`
  the same (Windows rows JC, J03 to J05, JCR1 and JCR2), for a branch that another
  ref reaches. A branch tip that no other ref reaches is not measured there. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D19.
- The last component of `<p>` is checked first. In the repository no git runs before
  this check. A symbolic link answers `{"success":false,"error":"refusing to remove
  worktree: <t> is a symbolic link, not a worktree directory"}`. A file that is not a
  directory answers `refusing to remove worktree: <t> is not a directory`. Nothing is
  deleted. `<t>` is `<p>` with the symbolic links of `baseRepo` resolved. With
  `worktreeRoot`, the links of the `<directory>` level are resolved instead. Under
  macOS `/tmp`, `<t>` therefore reads `/private/tmp/…`. Before, a link to a sibling
  worktree made claustrum delete that sibling, its entry and its branch.
- The entry comes from the `.git` file of `<p>`. The file must hold `gitdir: ` and a
  path of the form `<something>/worktrees/<name>`. The entry `<git dir>/worktrees/<name>`
  must be a directory. Its `commondir` must lead back to `<git dir>`, and its `gitdir`
  record must name `<p>`. Only then is the entry verified. No row tests each of these
  steps on its own. The path match is exact, so on macOS a spelling of `<p>` that
  differs only in letter case does not match. On Windows the match ignores letter
  case and slash direction, because git records its paths with forward slashes there.
  An 8.3 short name does not match on Windows, so such a remove keeps the entry. Rows
  C01 and C02 measure the case rule. Rows N83_leaf and N83_base measure the 8.3 rule.
- If a verified entry holds a `locked` entry of any kind, it is locked. A dangling
  symbolic link counts. Without a verified entry, the daemon checks every entry whose `gitdir`
  record names `<p>`. If the worktrees directory exists but cannot be read, the reply
  is the lock-check refusal above. A locked worktree answers
  `{"success":false,"error":"refusing to remove worktree: <p> is locked (git worktree
  lock); unlock it to remove it"}`, and nothing is deleted.
- The delete first removes each entry of `<p>` except `.git`, in the order of the
  directory read. It stops at the first failure. Then it removes the rest, `.git`
  included, and `<p>` itself. Every delete goes through an `os.Root`, so no delete
  follows a symbolic link out of `<p>` or out of its parent. That is a property of
  `os.Root`, not measured. A failure answers `{"success":false,"error":"failed to
  remove worktree: RemoveAll <entry>: <errno text>"}`, for example `RemoveAll zz:
  permission denied`. If an entry other than `.git` fails, that entry, every entry
  after it, and `.git` stay. If the removal of `<p>` itself fails at the end, `<p>`
  stays empty and `.git` is gone. The entry of the worktree and the branch stay in
  both cases. Both cases are measured on macOS, Linux and Windows.
- After the tree, the daemon deletes a verified entry. If that delete fails, the reply
  is `{"success":false,"error":"removed the worktree but could not drop its
  registration (RemoveAll <name>: <errno text>)"}`, and the branch step still runs.
  A kept branch adds `"branchKept":true` after `error` (rows R19 and B2-06, Linux VM).
  Without a verified entry, the daemon deletes the one entry whose record names `<p>`.
  If two entries match, or a match is locked, it deletes nothing. It does not
  report a failure of that delete (not measured). A plain directory inside the repository is
  therefore deleted, and the reply is `{"success":true}`. Treat `worktreePath` as a
  path you ask the daemon to remove, not as a filter.
- With `worktreeRoot`, a `<p>` without a verified entry is removed in two cases only.
  An empty directory is removed, and the request then goes on as for a gone worktree.
  A stale worktree of `baseRepo` is removed too. Its `.git` names an entry of
  `<git dir>` that is gone, or the whole `<git dir>/worktrees` is gone. A lock by path
  is checked before both cases. Only the lock before a stale or damaged worktree is
  measured. Any other path is refused and left in place, with
  `{"success":false,"error":"refusing to remove worktree: <p> is not a worktree of
  <repo> (<reason>), so it is left in place; remove it by hand if it is a
  leftover"}`. Here `<p>` is the path as sent, a trailing slash included. `<reason>`
  is one of `<t> has no .git file`, `<t>/.git is not a regular file`, `<t>/.git does
  not name a git dir`, `<t> carries a .git file that does not name this repository's
  own worktree admin directory`, or `<t> carries a .git file naming an admin directory
  whose own record is of a different worktree`. Another read error gives the
  transient reply `{"success":false,"error":"failed to remove worktree: could not
  verify that <p> is a worktree of <repo> (<detail>); retry"}`. There `<p>` is the
  cleaned path. One input is measured, with `git` on `PATH` (row DG1c-g, Linux VM).
  It is a missing `baseRepo` and a `.git` file that names an entry of another
  missing repository. Both
  references send this frame there, and nothing is deleted. Their `<detail>` is the
  hooks refusal with `chdir <baseRepo>: no such file or directory`. claustrum's
  `<detail>` is `open <baseRepo>/.git/worktrees: no such file or directory`. No run
  has measured the reference on the other inputs. A refused daemon `GIT_CONFIG_COUNT` changes these answers (see
  "The daemon's own git environment").
- For a worktree whose directory is gone, the daemon checks the registration by path.
  A locked one answers `{"success":false,"error":"refusing to remove worktree: <p> is
  gone but its registration is locked (git worktree lock); unlock it to remove the
  registration and branch"}`. Otherwise the one entry whose record names `<p>` is
  deleted. Here the `baseRepo` part of `<p>` also counts in its resolved form, with
  junctions and 8.3 names resolved on Windows. The rest of `<p>` counts as sent. So a
  `baseRepo` sent in 8.3 form or through a junction still finds a locked registration
  when the worktree and its parent are gone (rows GL1 and GL2, with GL0 as the
  control). With `worktreeRoot`, a root that does not exist is refused before
  this path. A worktrees directory that cannot be read
  does not stop this path: with `worktreeRoot` the reply is `{"success":true}`
  (row K13, Linux VM). Without
  `worktreeRoot` that case is not measured. Without `worktreeRoot`, two more answers
  come first. A configuration listing that fails, or a refused git directory, answers
  `{"success":false,"error":"failed
  to remove worktree: could not check whether <p> is locked (<reason>); retry"}`.
  `<reason>` is the hooks refusal, the `git cannot run` text, a limit refusal or the
  trust refusal text. A listing that says git finds no repository skips the
  registration instead, and so does a `PATH` with no `git`. The reply is then success,
  with `branchKept` when the branch step keeps a branch (see
  [Git-directory trust check](#git-directory-trust-check)). A `baseRepo` that holds
  no repository answers `{"success":true}`, and so does one that does not exist.
  The branch step runs on this path too (rows R17a and R17b). Without a repository its
  for-each-ref fails, so a request that names a branch gets `"branchKept":true`
  (rows R18 and R18b, Linux VM). A `baseRepo` that does not exist gets the member
  too, and its only git call is the excludes read (row X3, Linux VM). There
  `f6010b97` answers a bare `{"success":true}`.
  A refused daemon `GIT_CONFIG_COUNT` changes these answers (see "The daemon's own
  git environment").
- The branch goes last, through the branch step below. A kept branch adds
  `"branchKept":true` after `success`, or after `error` when there is one (rows R02
  and R19). The member is only ever true. The step adds no text to the reply. A
  `branchName` that is empty or starts with `-` or `+` is skipped. A request whose
  branch the listing and rev-parse show absent answers a bare `{"success":true}`.
  That is what "lenient" means here.
- Without `worktreeRoot`, a `baseRepo` that the daemon can open but not search
  (mode 0600) answers `{"success":false,"error":"failed to remove worktree: statat
  .claude: permission denied"}`. One that it cannot open at all (mode 0000) answers
  `failed to remove worktree: open <baseRepo>: permission denied`. Nothing is
  deleted in either case. Measured against `f6010b97` on Linux and macOS VMs.
- A home-directory `worktreePath` is refused. The `7d193f89` containment now does
  it, as parity. A `~`-expanded home path is not strictly under `baseRepo`, so it is
  refused with the reference's `"…is not inside the repository…"` wording before any
  delete. The claustrum-only D2 frame (`"worktreePath must not be or contain
  the home directory: …"`) is now behind that containment on this method's default
  branch. It fires only in the exotic case of a repository that is itself an
  ancestor of home. On the `worktreeRoot` / `external_root` branch the in-repo
  containment does not apply, so there D2 is the active home guard. D2
  remains the primary guard for `files.extract_tar`, which gained no containment. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- A relative `worktreePath` is refused upfront (`"…is a relative path…"`). Send an
  absolute path under the repository.
- `gitTimeout` (D5) is off by default. On this method no D5 kill leads to a delete.
  A kill only refuses or skips a step. When a hit stops the config or repository
  check, the request refuses, or it skips the registration of a gone worktree. With
  `worktreeRoot` that hit answers the work-tree refusal, and so does a hit on
  `rev-parse --show-toplevel` or the first `worktree list` call. A hit on the second
  `worktree list` call answers the `cannot list the repository's worktrees` text. A
  D5 stop of a call of the branch
  step keeps the branch, and the reply adds `"branchKept":true`. Not measured. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D5.

#### The branch step

Since `89cb6289` the daemon checks a branch before it deletes it. It deletes the
branch only when another local branch or a remote-tracking ref reaches its tip.
Else it keeps the branch. `git.worktree_remove` runs this step after the worktree
directory and its entry go. The `git.worktree_create` rollbacks run it on the
branch that the call made. `f6010b97` deleted the branch with no check. The rules
below were measured side by side against `f6010b97` and `89cb6289`. L, M and W name
the Linux, macOS and Windows VMs of each row. `<b>` is `branchName` as sent.

- No call runs for an empty `branchName`, or for one that starts with `-` or `+`
  (rows R12e, R12d and R12p, L).
- Each call runs in `baseRepo`, right after its own light config listing (see
  [Hardened git calls](#hardened-git-calls)). The calls, in order:
  1. `for-each-ref --count=10001 --format=%(objectname)%00%(refname)%00%(symref)
     refs/heads/`. The format is one argument. Each record is the object name, a NUL
     byte, the refname, a NUL byte, the symref target and a newline.
  2. `rev-parse --verify --quiet refs/heads/<b>^{commit}`. It runs only when the
     listing does not hold `refs/heads/<b>`.
  3. `rev-list -n 1 <tip> --not --remotes --not --stdin --`. `<tip>` is the full
     object name in the record of `refs/heads/<b>`.
  4. `update-ref --no-deref -d refs/heads/<b> <tip>`. `f6010b97` sent no old value.
     With it, git refuses the delete when the branch moved after the check.
- Every call carries the light `-c` set. rev-list adds `-c core.commitGraph=false`
  after it. The daemon's own environment comes first, as for every hardened call
  (see "The daemon's own git environment"). Then come `GIT_TERMINAL_PROMPT=0`,
  `GIT_NO_REPLACE_OBJECTS=1`, `GIT_GRAFT_FILE=<null device>`, the `GIT_COMMON_DIR`
  pin, the count, the inherited pairs, the hook pins, `GIT_NO_LAZY_FETCH=1`,
  `GIT_ALLOW_PROTOCOL=denied_by_claude_ssh` and `GIT_ASKPASS=`, in that order (row
  R01, L M W). Only rev-list gets a pipe on stdin. The other calls get a character
  device (macOS VM).
- With no repository at `baseRepo`, both references put `GIT_DIR=<null device>` in
  the slot of the pin, and claustrum does not. Both references also run
  `rev-parse --absolute-git-dir` there, which exits 128, and claustrum does not. The
  frames are equal in rows R18 and R18b (L). This is a gap in the calls only.
- Both references run `--git-dir=<T>/.git --work-tree=<T> rev-parse --show-toplevel`
  in `<T>/.git`, after its listing. They run it on the gone path of a remove, before
  a second create at the same path, and before an attach add. claustrum does not.
  The frames are equal in rows R17a, R17b, C06, C06b, C06c and C09 (L). This is a
  gap in the calls only.
- The steps, in order. "Keep" means that the branch stays.
  1. for-each-ref fails or is stopped: keep (rows R18 and R22a, L, B2-02, M W). A
     listing that does not parse keeps the branch too. That is claustrum's choice
     (not measured). If the configuration listing before for-each-ref exits 128
     with `fatal: not a git repository`, for-each-ref does not run. The branch is
     then kept (`89cb6289`, rows L14a, L14b, L14d, L14e and N01 to N04, L M).
  2. The listing holds 10001 records: keep. This comes before step 4, so a branch
     past the 10001st record is kept with no rev-parse (rows R15a and R15c, L M W).
     With 10000 heads the whole check runs (row R15b, L M W).
  3. A listed refname differs from `refs/heads/<b>` only in letter case: keep (rows
     R16 and R16b, L M W). claustrum folds case with Go's `strings.EqualFold`,
     which also folds letters outside ASCII. That is claustrum's choice (not
     measured).
  4. The listing does not hold `refs/heads/<b>`: rev-parse runs. Exit 1 means the
     branch is absent, and nothing else runs (rows R11, L M W, and B2-09d, L). If
     rev-parse exits 0 or fails another way, claustrum keeps the branch. A rev-parse
     stopped at a bound is such a failure, also on W, where the kill gives exit code
     1. That is claustrum's choice (not measured).
  5. rev-list gets one `^<refname>` line for each other listed record, in listing
     order. A record with a symref target is left out, and so is `refs/heads/<b>`
     (rows R04, R08 and B2-05). The stdin is empty when `<b>` is the only branch
     (rows B2-04a and B2-04b, L). A printed commit keeps the branch (row R02, L M W).
     A failed or stopped rev-list keeps it too (rows B2-01 and B2-03, L).
  6. update-ref runs. A failure or a stop keeps the branch (rows R14, L M W, and R23a).
- Another local branch at or past the tip reaches it, and so does a remote-tracking
  ref (rows R03, R04, R04b and B2-04b). A tag, a detached HEAD, a symref and a branch
  at a parent commit do not (rows R05 to R08 and B2-04a). A branch checked out in
  another worktree gets no guard of its own (rows R10a and R10b, L). With a symref as
  `branchName`, update-ref deletes the symref only (row B2-05, L).
- The measured bounds are parity. They are always on, and they are not a D-series
  divergence.
  - One bound of 60 s covers for-each-ref and rev-list together. At the
    bound the running call is killed, with no SIGTERM first. In row R22a (L)
    for-each-ref ended 60.002 s after its start. In row B2-01 (L) for-each-ref used
    40 s, and rev-list got the other 20 s. On W the call ended at about 59.9 s with
    exit code 1 (row B2-02). On M it ended at about 61.2 s, in a run with one VM stall.
  - claustrum starts the bound right before the config listing of for-each-ref. The
    bound also covers rev-parse. Both are claustrum's choice (not measured).
  - update-ref gets SIGTERM 5 s after it starts, on L and M. A call that ignores it
    is killed 5 s later (rows R23a, R23b, R23a1 and R23b1). On W it is killed at
    about 5 s, with exit code 1 (row R23a). The 5 s count from the start of
    update-ref, after its config listing.
  - The same stop holds in the create rollback. `f6010b97` already stopped update-ref
    there, and the undo text ends `: signal: terminated` (row B2-09e, L M). A rollback
    update-ref that ignores the SIGTERM is killed 5 s later, as on remove. That is
    claustrum's choice (not measured).
  - Only the git process gets the SIGTERM or the kill, not its children. That is
    claustrum's choice (not measured).
  - The caller's `timeoutMs` does not stop the step (rows C04 and B2-10, L).
  - With `-git-timeout` (D5) opted in, each call and its config listing also get
    their own D5 deadline, as every hardened call does. On update-ref that stop is
    the SIGTERM above.
- Within one daemon, the check and the delete of a branch run one at a time for one
  repository, also through a linked worktree. Two removes of worktrees whose
  branches share a unique commit then leave one of the two branches. The reply that
  comes second carries the member. This holds for one `baseRepo` (rows R21, L, and
  B2-12, M) and through a linked worktree (row R21b, L).
  The branch steps of different repositories run at the same time (row B2-08, L). The
  rows do not tell a lock on the common dir from one on the main worktree. claustrum
  keys the lock by the git dir of the entries, the common dir. That is claustrum's
  choice (not measured). A create rollback takes the same lock (not measured).
- claustrum still runs two requests with the same `baseRepo` one after the other,
  as before. `89cb6289` deletes both worktree directories first (row R21, L). No frame
  shows that difference.

The remove frame never gains text from this step. A kept branch adds only
`"branchKept":true`. Every kept case of steps 1 to 6 sets it on remove (rows R02,
R14, R15a, R16, R18 and R22a).

The create rollback adds a text part after `; and the undo could not finish for
<leaf>: `, and sets the member in one case only:

| outcome | part | member | rows |
|---|---|---|---|
| rev-list printed a commit | `the worktree itself was removed, but branch <b> was left in place: it now has commits no other branch or remote-tracking ref reaches. To keep that work, push the branch or rename it. To discard it, run git branch -D <b>. This branch name cannot be reused until the branch is renamed or deleted` | `"branchKept":true` | C02 (L M W), C04, C09, C10, B2-10 (L) |
| the same, after a failed leaf rmdir | `<rmdir text>; branch <b> was left in place: it now has commits …` with no `the worktree itself was removed, but ` | `"branchKept":true` | C05 (L W) |
| 10001 records, a case twin, a listing that does not parse, or rev-parse exit 0 | `branch <b> still exists; delete it by hand (for example: git branch -d <b>, which git refuses if that would lose commits) before retrying this branch name: could not check whether another branch or remote-tracking ref reaches its commits` | none | C06 (L M W), C06b, B2-09c (L) |
| for-each-ref, rev-parse or rev-list failed or stopped | the same, then a space and the process error in parentheses, for example ` (exit status 128)` or ` (signal: killed)` | none | B2-09a, B2-09b, B2-09f (L) |
| update-ref failed or stopped | `branch <b> still exists; delete it by hand (for example: git branch -D <b>) before retrying this branch name: <process error>`, for example `exit status 128` or `signal: terminated` | none | C07a, B2-09e (L), B2-09e (M) |
| the same, with `refs/heads/<b>.lock` present | `… before retrying this branch name. A lock refs/heads/<b>.lock is also present: if another git process is running against this repository, the lock may be live and clears on its own; if none is, it is stale debris of the interrupted delete — remove the lock file by hand too: <process error>` | none | C07b (L) |
| the name starts with `+`, so no call runs | `branch <b> left in place (its name is unsafe to pass to update-ref); delete it by hand` | none | X1, X2 (L) |
| the branch is absent at the check, or deleted | none | none | B2-09d, C01, C03, C06c (L), C01 (M W) |

- The other rows used `s` only. claustrum puts `<b>` in each place where `s`
  stands. The dash in the lock text is the raw UTF-8 em dash U+2014.
- When claustrum's home or identity guard skips the leaf, the leaf stays. A kept
  branch then gets its text without `the worktree itself was removed, but `. No
  honest input reaches that path. It is claustrum's own (not measured).
- The rows with a parse failure and with rev-parse exit 0 or another rev-parse
  failure are claustrum's choice (not measured).
- The text of a skipped name is older than `89cb6289`. `f6010b97` gives the same
  bytes in rows X1 and X2 (L), whatever reaches the branch. A name that starts with
  `-` gets the same text. That is claustrum's choice (not measured). The create does
  not reach it, because git refuses `-b -<name>` (host git, not measured on a VM).
- The lock part appears when the file `<common git dir>/refs/heads/<b>.lock` exists
  after the failure. The rows do not tell whether the references test the file or
  read git's stderr. claustrum tests the file. It does so after a stop too. That is
  claustrum's choice (not measured).
- On W a stopped call ends with exit code 1. A W rollback text then ends `exit status
  1` or ` (exit status 1)` where L and M show a signal. W did not measure these texts.
- A rollback that fails in two places, other than C05, joins the two parts with `; `
  in the same order. That is claustrum's choice (not measured).
- The texts of rows C07a, C07b and B2-09e are older than `89cb6289`, and so is the
  5 s stop of the rollback's update-ref. `f6010b97` gives the same bytes (L, and M
  for B2-09e).

### launcher.* (added `89cb6289`)

A host administrator names a managed launcher in the managed settings. The
managed launcher is a program that runs the Claude Code CLI on this host. The
daemon puts its argv in front of the CLI command. Three surfaces use it:
`launcher.resolve` below, the `launcher` param of
[process.spawn](#processspawn), and the launcher runs of `-install` and
`-probe-cli`. Every rule below is measured against `89cb6289` on a Linux VM
unless it says otherwise. A rule that names macOS or Windows was measured there too.

#### launcher.resolve
`{cliPath}` → one of four shapes. Each shape keeps this field order:

```jsonc
{"status":"none"}
{"status":"usable","argv":["<launcher>","<arg>",…],"source":"<file>"}
{"status":"unusable","source":"<file>","reason":"<text>"}
{"status":"unreadable","reason":"<text>","path":"<file or folder>"}
```

- No `params` member, a non-object `params` or a non-string `cliPath` →
  `-32602 Invalid params`. `{}`, `"params":null`, a `null` or empty `cliPath`, or a
  wrong key (wrong key: Windows only) → `-32602 cliPath is required`. Any other string is a `cliPath`, a
  relative one too. Another `launcher.<x>` method → `-32601 Unknown method:
  launcher.<x>`. The params checks are the same on Windows.
- The method reads the settings files on every call. It caches nothing, so an
  edit shows on the next call. It never runs the launcher. The
  `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not change a frame (measured on macOS).
- claustrum does not `~`-expand `cliPath`. That is claustrum's choice (not
  measured).

The settings folder and its files:

- The folder is `/etc/claude-code` on Linux and `/Library/Application Support/ClaudeCode`
  on macOS. The base file is `managed-settings.json` in it. The drop-in folder is
  `managed-settings.d` in it.
- `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` moves the folder only when the system
  folder has neither the base file nor a drop-in. With the system folder absent,
  `89cb6289` reads the variable's folder and logs nothing (row RX, measured on
  Linux and macOS). A base file in the system folder makes it ignore the variable
  and read the system folder (row RXs, Linux and macOS). A drop-in there does too
  (row RXd, Linux). A hidden `.json` entry alone in `managed-settings.d` counts as
  well (row RXd2, Linux). The resolve then answers from the system folder and
  skips that entry, so it answers `{"status":"none"}`. It then logs
  `[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: <system folder> holds this host's policy`
  once per daemon (row RXs: two resolves, the line only on the first).
  `<system folder>` is the folder of the OS. An existing but empty system folder
  does not count (row RXe, Linux). An empty value counts as unset, and the system
  folder is read (row RY, measured on Linux and macOS). claustrum's choices (not
  measured) follow. A base file counts when its path exists, a dangling symlink
  too. A base file counts whatever it holds, `{}` or an unreadable file too. A
  `managed-settings.d` counts when it holds an entry whose name ends in `.json`
  and that is a file or a symlink, a dangling symlink too. A non-`.json` entry and
  a folder entry do not count, as for the drop-in rule. More than two resolves
  still log one line. `-install` and `-probe-cli` honor the variable the same way
  and log nothing.
- On Windows `89cb6289` answered `{"status":"none"}` for every fixture tried. The
  fixtures were a file in `C:\Program Files\ClaudeCode` (both keys, also with the
  gate), the E2E folder and `C:\ProgramData\ClaudeCode`. The launcher never ran.
  claustrum answers none on Windows and reads no file.
- A missing or empty folder answers none, with no log line. So does a base file
  of `{}`, of zero bytes or of white space only.
- The base file is read first, then the drop-ins in name order. A later file
  with a value wins, and `source` names that file. A missing base file is fine.
- A drop-in name ends in `.json` in lower case and does not start with a dot. So
  `.hidden.json`, `x.JSON` and `a.js` are skipped. A folder entry and a FIFO
  entry are skipped. The FIFO is not opened.
- A symlink entry is followed. `source` then keeps the link path. A dangling
  symlink is skipped. A symlink to a folder makes the answer unreadable, with
  `is a directory`.
- A regular file named `managed-settings.d` is ignored.
- A value of the wrong type in a later file is skipped, and the earlier value
  stays. Rows: `{"processWrapper":5}`, `{"env":"x"}` and
  `{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":true}}`.
- One unreadable file makes the whole answer unreadable, and `path` names it. A
  valid base file does not help.
- claustrum's choices (not measured) follow. The drop-ins sort in byte order. The
  first unreadable file in read order is the one reported. Each file gives one
  value, so a later file's `processWrapper` wins over an earlier file's env key.

| unreadable file | `reason` |
|---|---|
| mode 000, daemon not root (Linux and macOS) | `permission denied` |
| a folder | `is a directory` |
| a FIFO (answered at once, not opened) | `not a regular file` |
| more than 2097152 bytes (2097152 is read) | `larger than 2 MiB` |
| `{` | `not valid JSON` |
| `[]` or `null` | `not a JSON object` |
| a UTF-16BE BOM, or a raw U+00A0 before the JSON | `not valid JSON` |
| a drop-in folder whose listing fails | `the drop-in directory could not be listed: permission denied`, with the folder as `path` |

claustrum gives a regular-file drop-in, and the target of a drop-in symlink, the
same reasons (not measured, except `is a directory` and `not valid JSON`).

A UTF-8 BOM is dropped. A UTF-16LE BOM with UTF-16LE text is read. Both answer
usable. claustrum drops a trailing odd byte of UTF-16LE text. That is
claustrum's choice (not measured).

The value:

- Two keys carry it: `env.CLAUDE_CODE_PROCESS_WRAPPER` and `processWrapper`, each a
  string. Both keys gave the same frames on every R10 and R11 row. In one file the env key
  wins, in both key orders.
- An env value of `""` counts as unset, so `processWrapper` answers. An env value
  of three spaces counts as set with no launcher. claustrum also counts a
  `processWrapper` of `""` as unset. That is claustrum's choice (not measured).
- A value that starts with `[`, after leading white space, is a JSON array of
  strings. JSON escapes decode, and the metacharacter check does not apply.
- Any other value is an argv list, not a shell command. A space, U+00A0 and U+FEFF
  split words. U+0085 does not. Double quotes group, so a quoted metacharacter is
  fine. Inside double quotes `\"` gives `"` and `\\` gives `\`. Any other
  backslash stays, so `"e\nf"` keeps its two characters. Outside quotes a
  backslash is a plain character, and a `"` after it opens a quote. Single
  quotes are plain characters.
- An invalid byte in the file comes out as U+FFFD.
- claustrum's choice (not measured): the other white space of Go's
  `unicode.IsSpace`, except U+0085, splits words too. The scan stops at the first
  unquoted metacharacter.

The reason texts. The frame escapes `&`, `<`, `>` and `"` as JSON does. The log
line writes them raw. `—` is the raw bytes `e2 80 94` in both.

| case | `reason` |
|---|---|
| no launcher: three spaces, `""`, `[]` or `[""]` | `the value is set but contains no launcher — unset it to run without one, or set it to the absolute path of your launcher` |
| a later empty JSON element, `["<w>",""]` | `the JSON array contains an empty element — remove it, or fill in the value it was a placeholder for` |
| a JSON element that is not a string | `JSON form must be an array of strings` |
| starts with `[` and is not valid JSON | ``value starts with `[` but is not valid JSON`` |
| an unquoted `;` `\|` `&` `$` `(` `)` backtick `<` `>` | ``the value contains an unquoted shell metacharacter (one of ; \| & $ ( ) ` < >) — it is an argv list, not a shell command`` |
| an open double quote | `unterminated double quote` |
| a later empty `""` word | ``the value contains an empty `""` token — remove it, or fill in the value it was a placeholder for`` |
| argv[0] is not absolute | `the launcher must be an absolute path, not a bare name resolved via PATH` |
| argv[0] equals `cliPath` | ``launcher `<argv0>` is Claude Code's own path`` |
| argv[0] ends in `.js`, `.mjs`, `.ts`, `.tsx` or `.jsx` (case-sensitive) | ``launcher `<argv0>` is a script the SDK would run in place of Claude Code`` |
| argv[0] is missing, a folder or not executable | ``launcher `<argv0>` does not exist or is not an executable regular file`` |

The launcher checks:

- They check argv[0] only. The launcher args are not checked against files.
- The own-path check is a string compare. It comes before the absolute check and
  before the existence check. So `c` with `cliPath` `c` is the own-path text,
  and a missing `/opt/claude/cli` with that `cliPath` is too.
- A symlink to an executable file is usable, and `argv` keeps the link path.
  argv[0] is quoted as parsed, not cleaned or resolved.
- A long argv[0] is cut in the text. One row is measured: a 300-byte path with a
  3-byte character at bytes 254 to 256 kept 254 bytes, then `…` (`e2 80 a6`),
  inside the backticks. The log line shows the same cut, and `argv` keeps the whole
  path. claustrum's choice (not measured): the limit is 256 bytes, cut back to the
  start of a character, for the own-path, script and existence texts alike.
- claustrum's choice (not measured): the script check comes before the existence
  check, so a missing `x.js` gets the script text.

Output bytes follow Go's default JSON encoder. In a `usable` frame `<`, `&`, `>`
and U+2028 come out as JSON escapes. U+0085, U+FFFD and `—` stay raw.

Log lines. An unusable or unreadable answer writes one line. A usable or none
answer writes none:

```text
[LauncherHandler] managed launcher from <source> refused: <reason>
[LauncherHandler] managed settings unreadable: <path>: <reason>
```

A repeat of the same answer writes no line. A usable answer between two equal
refusals makes the second one log again. A `process.spawn` refusal writes no
`[LauncherHandler]` line. claustrum's choices (not measured) follow. The repeat
test compares the whole line. A none answer resets it like a usable one. The
lines log at WARN.

### process.* (the agent/MCP-hosting core)

The client supplies its own `id`, which is any string. The daemon delivers output
as id-less stream notifications, and it buffers them for a later replay.

#### process.spawn
`{id,command[,args][,cwd][,env][,disableShellAgentSocket][,launcher][,wantPid]}` → `{"success":true}`, then stream frames
- `args`: string[]. `env`: `{KEY:VAL}`, merged over the daemon environment.
- Missing `id` → `-32602 Process ID is required`. Missing `command` →
  `-32602 Command is required`.
- A request with the `id` of a process that still runs succeeds and replaces the
  table entry. The first process gets no signal and keeps running. A
  `process.kill` of that `id` then reaches the second process only (row A2). In
  claustrum `process.stdin` and `process.reattach` name the second process too.
  `-stop` and `server.shutdown` end only the processes of the table, so the first
  process outlives the daemon (row A3). Linux and macOS VMs measured this against `89cb6289` (rows
  A1 to A4), and a Windows VM too (rows EV and EVInh). On Linux and macOS the
  record of the first process stays, and the next daemon start ends that process
  from it (row A4). The first process keeps its own connection. On a Linux VM
  (row P2, two runs), the first process of `89cb6289` wrote and ended after the
  replace. Its stream frames still came on its own connection, under the same
  `processId`, with seq 2 to 9. Its exit frame came there with seq 10. The
  connection of the second process got the frames of the second process only.
  claustrum is built to that row. macOS row P2 and Windows row EVc gave the same
  split of the frames on `89cb6289` and on claustrum. Three more Linux rows
  measured two processes under one `id`. `89cb6289` and claustrum gave the same
  frames in each:
    - Both spawns come on one connection (row N2a). That connection gets both
      streams under the one `processId`. Each process counts its own `seq`, so
      seq 1 to 9 and an exit frame with seq 10 come twice.
    - The connection of the first process sends `process.reattach` for the `id`
      with `fromSeq` 0 (row N2b). The reply names the second process
      (`"firstSeq":1,"lastSeq":2`), and its two frames come before the reply. The
      connection of the second process gets EOF. The first connection then
      carries both processes up to both exit frames.
    - The first connection closes, and a third connection sends
      `process.reattach` with `fromSeq` 5 (row N2c). The reply is
      `{"found":true,"running":true,"firstSeq":1,"lastSeq":3,"stdinApplied":0}`
      with no replay. The second connection gets EOF. The third one gets seq 4 to
      9 and the exit frame of the second process, and no frame of the first.
- Session superseding is `4534d86` parity. A `process.spawn` whose `args` name a
  stream-json CLI session terminates any OTHER running process of the SAME session
  id. Such `args` carry an `--input-format=stream-json` or
  `--output-format=stream-json` arg, or a bare `stream-json` arg. They also carry a
  valid `--session-id` or `--resume` token, with the resume fallback suppressed by
  `--fork-session`. The superseded process is killed through the
  SIGTERM-then-SIGKILL path, and its exit frame reaches its client. That
  frame carries `killedBy:"client"` (rows B1 to B3 below). A spawn with
  no session key, or with a different session id, supersedes nothing. The eviction,
  the `client` kill reason, and the session-key rules above are measured against
  the reference. claustrum also serializes concurrent spawns of one session.
  The daemon ends the other process and waits for its end before it starts the new
  one. The reply of the new spawn therefore comes after the exit frame of the old
  process. Linux and macOS VMs measured that against `89cb6289` (rows B1 to B4, two
  runs each):
    - The old process exits on `SIGTERM`: the reply follows at once (row B1). Its
      group gets no `SIGKILL` (Linux row X2a, see
      [process.killAndWait](#processkillandwait)).
    - The old process ignores `SIGTERM`: the daemon sends `SIGKILL` to its group
      after 3 s, and the reply follows (row B2).
    - The old process exits, and a process in another group holds its pipes: the
      exit frame and the reply come 5 s after the request (row B3).
    - A spawn with the `id` and the session of a process that still runs
      supersedes nothing and sends no signal (row B4).
  The daemon log holds the exit line of the old process, then the `supersedes`
  line, then the started line of the new process. A Windows VM measured the same
  order of exit frame and reply on `89cb6289` and on claustrum (rows SS and SSInh,
  3 runs each).
  There the first child ended and its descendants lived on. Not measured: two or
  more other processes of one session. claustrum ends them one after the other.
  - A session spawn whose command does not exist supersedes nothing. On a Linux VM
    (rows P3 and H1) and on a macOS VM (row P3), `89cb6289` answered
    `-32603 fork/exec <path>: no such file or directory`. The old
    process got no signal and kept its record. claustrum is built to those rows. It
    tests the command after it builds the child environment and before the
    supersede.
  - The test is one `stat` of the command path. Linux rows H1 to H6 and H9
    measured it with `strace`. `89cb6289` and claustrum made the same `stat`
    calls and gave the same replies in each row:
      - The path is read as the request gives it, and it is not joined with `cwd`.
        A session spawn of `./tool` with a `cwd` that holds `tool` answered
        `fork/exec ./tool: no such file or directory` (row H3). The same request
        with no session arguments started.
      - A spawn with no session key runs no `stat` of the command (row H2).
      - A command that exists and does not start passes the test. A file of mode
        0644, a directory and a script with a missing interpreter each got the
        supersede first. The old process got `SIGTERM` and ended, and the start
        error came after it (rows H4a to H4c).
      - A bare command name that the lookup does not find answers
        `exec: "<name>": executable file not found in $PATH`. The old process
        gets no signal (row H9).
      - A `cwd` that is missing or is a file answers its own error, and the old
        process gets no signal (rows H5a and H5b). That test comes before the
        PATH read of the first spawn (row H6) and before the `stat` of the
        command (row H3).
    With a `launcher`, the fifth check below is that test.
  - On Windows the lookup answers first. Then claustrum runs one `stat` of an
    absolute command path. On a Windows VM (row SSm-path, 3 of 3 runs),
    `89cb6289` answered
    `-32603 fork/exec <path>: The system cannot find the file specified.` for a
    missing path that ends in `.exe`, and the old process lived on. Build
    `ac5cadb` of claustrum ended the old process there. claustrum is now built to
    that row. A bare name (row SSm-bare) and a path with no extension (row
    SSm-noext) answered `exec: "<name>": executable file not found in %PATH%` on
    both, with no supersede. Not measured: a relative path. claustrum runs no
    `stat` of it.
  - A `-stop` can come while a spawn waits for the old process. Linux and macOS
    VMs measured that against `89cb6289` (row P1, 7 Linux runs and 2 macOS runs). The old process
    did not end on `SIGTERM`, and `-stop` came 1 s after the spawn request. The spawn got
    no reply, and no connection got an exit frame. The new process never started.
    The daemon sent `SIGKILL` to the group of the old process and removed its
    record. `-stop` printed `stopped` and exited 0. The log held no `supersedes`
    line and no exit line. claustrum gave the same events. Its log differs: build
    `ac5cadb` wrote the exit line of the old process and the `supersedes` line in
    1 of 6 Linux runs and in 2 of 2 macOS runs. That difference is open. See
    [UPSTREAM-TRACKING.md](UPSTREAM-TRACKING.md).
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
    a 4 s deadline, and a shell that the kill cannot end is given up on at 5 s. This
    run therefore adds about 4 s to its spawn when the shell does not exit, and
    about 5 s when the kill cannot end it. The first spawn of a daemon also runs
    the PATH read, which has its own 4 s deadline. On a Linux VM with a shell that
    sleeps 10 s, the first spawn of `89cb6289` answered after 8 s (row G3).
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
- The `launcher` param is `89cb6289` parity: a string array that names a
  managed launcher. The child runs as `<launcher...> <command> <args...>`. The
  launcher is the process, and its stream frames are its own.
  - `launcher` absent or `null` means no launcher, and the command runs directly. A
    non-array `launcher` or a non-string element → `-32602 Invalid params`, with no
    log line. A `null` element becomes an empty arg.
  - The checks run in this order. First, on Windows any non-null `launcher`, `[]`
    too, → `-32004 the managed launcher cannot be used: a launcher cannot be
    applied on a Windows host`. The command does not run.
  - Second, a relative `command` with any non-null `launcher` →
    `-32602 command must be an absolute path when a launcher is given`.
  - Third, the launcher checks of [launcher.resolve](#launcherresolve) run, with
    the command in place of `cliPath`. A refusal →
    `-32004 the managed launcher cannot be used: <reason>`. `[]` gives the
    no-launcher text.
  - Fourth, the cwd check gives its usual frame (its place before the command
    check is inferred from log order). Fifth, a command that is missing, a folder
    or not executable gives the same `-32603 fork/exec <command>: <reason>` frame
    as a spawn without a launcher. The launcher does not run. Measured for a
    missing file, a folder and a 0644 file, on Linux and macOS.
  - Sixth, a launcher that passes the checks but does not start →
    `-32005 the managed launcher <path> could not be started: <reason>`. A missing
    `#!` interpreter, or a script with Windows CRLF line endings, gives
    `its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)`.
    A 0755 text file with no `#!` gives `exec format error`. macOS gives the same
    interpreter text (S6a only). claustrum gives the same frames through its
    exec-child trampoline (tested, not VM-measured).
  - claustrum's choices (not measured) follow. The Windows refusal comes before
    the relative-command check. The bad-command check comes before the launcher
    starts. Any other start failure gives its errno text.
  - The log lines are `[process.Manager] Failed to start process <id>: <message>`
    for each refusal, and
    `[process.Manager] Process <id> started, PID=<n>, command=<command> via launcher <argv0>`
    for a start. The started line names argv[0] only. A refusal writes no
    `[LauncherHandler]` line.
  - A spawn does not read the managed settings. Rows SP-a and SP-b measured this
    on Linux: with a usable launcher in the settings, a spawn without the param
    runs the command directly, and a spawn with the param runs the launcher it
    names. The caller resolves the launcher with `launcher.resolve` and passes it
    here. The
    `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not change a spawn.
- Every spawned child loses `CLAUDE_SSH_MANAGED_LAUNCHER` and
  `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` (`89cb6289` parity, measured on Linux and
  macOS). A child with a launcher also loses `CLAUDE_CODE_PROCESS_WRAPPER`. A
  child without one keeps it. The strip covers the
  daemon env and the spawn `env` param. claustrum strips on Windows too. That is
  claustrum's choice (not measured).
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
- The exec-child trampoline is `19f30c46` parity on linux and darwin. On every
  socket shape, a spawned child is launched
  through a self-re-exec trampoline (`<self> --exec-child <path> <argv…>`). That
  trampoline adds two environment markers that give the child a stable,
  pid-reuse-safe identity. The first is `CLAUDE_SSH_RUN_DIR=<run dir>`, set by the
  daemon. The run dir is the folder of the socket path, cleaned and not made
  absolute. The second is `CLAUDE_SSH_CHILD=<pid>:<startTicks>`, set by the
  trampoline from the child's own pid and its start-time. On linux the start-time
  is the `/proc` clock ticks. On darwin it is a date text: the `ps` start-time
  with one blank between its words. The five
  Go-runtime vars (`GODEBUG`, `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT`, `GOTRACEBACK`)
  are stashed under `CLAUDE_SSH_HELD_<name>` across the re-exec. They are restored
  before the target runs, so they do not perturb the transient trampoline. A
  command that
  does not resolve to a runnable file is not trampolined, so its spawn error frame
  is unchanged (`fork/exec …` or `exec: … not found in $PATH`). When the
  trampoline itself does not start, the daemon starts the command directly and
  answers with the error of that start. Measured on Linux against `89cb6289` with
  a `cwd` of mode 000 (row CW03): the frame is
  `-32603 fork/exec <command>: permission denied`. The daemon first logs
  `[process.Manager] exec trampoline failed for <id> (fork/exec /proc/self/exe: permission denied); starting it directly — a successor daemon will not be able to reap it`.
  That log text is the Linux text. This changes one `process.spawn` frame of
  claustrum. For a `cwd` that the daemon cannot enter, the error text named the
  trampoline before (`fork/exec /proc/self/exe: permission denied`). It now names
  the command (row CW03). Not measured: the trampoline start fails and the direct
  start works. claustrum then answers success, where it answered an error before.
  It gives that child `CLAUDE_SSH_RUN_DIR` and no `CLAUDE_SSH_CHILD`.
  `f6010b97` and `89cb6289` set both markers on every socket
  shape (Linux and macOS rows EV01a to EV01i, and Linux row K3). Linux and macOS VMs measured the
  values and the order against `89cb6289` (Linux rows F1 to F5, macOS rows F1 to
  F4). The socket
  `<R>/b/s.sock` gives `CLAUDE_SSH_RUN_DIR=<R>/b`. A relative socket `s.sock` gives
  `CLAUDE_SSH_RUN_DIR=.`. The socket `<R>//run/./c1/rpc.sock` gives `<R>/run/c1`.
  Not measured: an abstract socket name (`@name`). claustrum gives it the run dir
  `.`.
  The child environment is the daemon environment, where
  `CLAUDE_SSH_DAEMON_CHILD` is the last entry. A caller key that the daemon
  environment holds replaces that entry in place. A new caller key comes after the
  daemon entries. `CLAUDE_SSH_RUN_DIR` and then `CLAUDE_SSH_CHILD` are the last two
  entries. A caller value of those two does not reach the child (Linux rows EV02a
  and EV02e). An empty caller value of `CLAUDE_SSH_DAEMON_CHILD` stays empty.
  claustrum builds the environment by these rules. A Go-runtime variable keeps its
  place. On a Linux VM (rows P4a to P4c), a `GOGC` of the daemon environment of
  `89cb6289` stayed at its place there, before `CLAUDE_SSH_DAEMON_CHILD`. A
  `GOMAXPROCS` of the spawn `env` stood with the new caller keys, before
  `CLAUDE_SSH_RUN_DIR`. The two markers were the last two entries. claustrum is
  built to those rows. `GODEBUG`, `GOMEMLIMIT` and `GOTRACEBACK` are not measured,
  and claustrum treats them the same way. Two or more new caller keys have no
  fixed order on `89cb6289`. A Linux VM ran 20 spawns per row. For the request
  order `ZZ, AA, MM` it gave `ZZ, AA, MM` 16 times, `AA, MM, ZZ` 3 times and
  `MM, ZZ, AA` once (row P9). For the request order `ZZ, MM, AA` it gave
  `ZZ, MM, AA` 16 times, `MM, AA, ZZ` 3 times and `AA, ZZ, MM` once (row N7). Each
  order is a rotation of the request order. Build `ac5cadb` of claustrum gave name
  order in 20 of 20 spawns of both rows, and name order is not one of the three
  orders of row N7. claustrum now iterates the decoded map again and builds no
  order of its own. That gave a rotation of the request order in each of the 20
  spawns of a row (Linux rows P9 and N7, macOS row EV20). A request with more keys
  is not measured. The
  trampoline is off-wire and adds no JSON-RPC frame. The start failure above is
  the one frame that it changes. It was verified against
  `19f30c46` on a VM. The marker set and format match, and the values are
  per-process. On macOS the start text in `CLAUDE_SSH_CHILD` has one blank before
  a one-digit day (`Fri Oct 2 17:35:19 2026`), where `ps -o lstart=` prints two. A
  macOS VM measured that on `89cb6289` (rows F1 to F4). In those rows the text of
  the record and the text of the marker were byte-equal, on `89cb6289` and on
  claustrum. Only days 1 and 2 of a
  month are measured. The held Go vars round-trip. A missing target, a
  non-executable-format target, and a relative-under-`cwd` target each return the
  identical `-32603 fork/exec …` frame, or are trampolined exactly as the
  reference does. Darwin links the same subsystem, with the start-time from `ps`
  instead of `/proc`. This was validated against `19f30c46` on a macOS VM. On
  windows the reference daemon reports it is not the run-dir lock holder, so it
  stamps neither marker. A windows VM showed that a run-shaped-socket child has the
  same environment as a bare-socket child.
- The orphan-child registry record is `19f30c46` parity on linux and darwin. On
  every socket shape, each spawned child with a pid of 2 or more
  and a readable start-time is recorded to `<runDir>/children/<pid>.json`. The run
  dir is the folder of the socket path. The references write the record on every
  socket shape (Linux and macOS rows EV01a to EV01i, and rows F1 to F4 of both
  systems on `89cb6289`). On macOS the start text of the record has one blank between its
  words, as in `CLAUDE_SSH_CHILD`. A macOS VM measured mixed pairs of `89cb6289`
  and claustrum (rows RP15c, RP15d, RP15f and RP15g, 3 runs each). A daemon of
  one binary ended the recorded child of a dead daemon of the other, in both
  directions, with a launcher and without one. claustrum compares the start
  texts exactly at the reap, as `89cb6289` does. A record whose `start` holds two
  blanks before a one-digit day does not match. `89cb6289` and claustrum dropped
  such a record, sent no signal and logged the same line (rows TBr and TBe):
  `[process.Registry] record <pid>.json: pid <pid> is not a child to end (it did not start at the recorded "<text>": the pid has been reused); dropping the record, signalling nothing`.
  A `daemonStart` with two blanks does not match either. Both binaries then read
  that daemon as gone and ended its recorded child, while the daemon was alive
  (row N6, 2 runs each). With the one-blank text they kept the record and sent no
  signal (row N6k). From the code: an older claustrum build wrote two
  blanks on day 1 to 9 of a month. The first start of a newer daemon on macOS
  does not end a child of that build from those days.
  The
  daemon writes it atomically, through a temp file renamed into place. The temp
  file is `children/.rec-<pid>.json.<12 chars>` (`89cb6289`, measured on Linux, row
  SP06). The source of the 12 chars is not measured. claustrum uses the nanosecond
  clock in base 36. A later
  daemon reads these to reap children a since-exited daemon left behind. The record
  is an ordered JSON object:
  `{"pid":<int>,"node":"<boot-id>/pid:[<inode>]","host":"machine-id:<hex>","instance":"<daemon instance id>","daemonPid":<int>,"daemonStart":"<ticks>","argv0":"<child argv0>","start":"<ticks>","at":<epoch-ms>}`.
  The field ORDER and the string-vs-number typing are the on-disk contract,
  measured byte-for-byte against `19f30c46`. With a `launcher`, `argv0` is the
  launcher, and a `program` field holds the command, after `argv0` and before
  `start`. That is `89cb6289` parity, measured on Linux. claustrum omits `program`
  without a launcher, so that record keeps its `19f30c46` bytes. That is
  claustrum's choice (not measured). `daemonStart` and `start` are STRINGS,
  holding clock ticks on linux and a date text on darwin. `pid`, `daemonPid`
  and `at` are numbers. This record is off-wire, because it adds no JSON-RPC
  frame. On linux and darwin the daemon reaps these records at `-serve` startup,
  before it binds the socket.
  See [ARCHITECTURE.md](ARCHITECTURE.md) → orphan reap. On darwin the node is the
  boot-session UUID and the host is the hostname, and the start-times come from
  `ps` rather than `/proc`. On windows the reference daemon reports it is not the
  run-dir lock holder, so it records no children and reaps none. A windows VM
  showed that a spawned child leaves the children dir empty, and that planted
  records survive startup.
- The life of a record on linux and darwin. Each row is measured on Linux against
  `89cb6289`. The macOS rows SP01a to SP09b are equal on `89cb6289` and claustrum.
  - The end of the child removes its record (rows SP01, SP03, SP04, SP06 and
    CW02b). The removal comes before the pipe drain, so it does not wait for the
    exit frame (row SP05).
  - A graceful shutdown leaves no record at the default, without `-keep-children`
    (rows RP12a, STa and STb, equal on `89cb6289` and claustrum). claustrum waits
    for the children that it killed, for 1 s at most. That bound is claustrum's own
    value. Not measured: a child that outlives the bound.
  - With `-keep-children` the shutdown removes the records of the children that it
    leaves alive. The next daemon then finds no record of them and sends them no
    signal. That rule is claustrum's own, as the flag is. A Linux VM ran it on
    claustrum only (row P14). `-stop` of a daemon with the flag sent no kill and
    removed the records of its two children. A new daemon then sent no signal and
    logged no reap line, with the flag and without it. Both children stayed
    alive. The rule has two limits. A process whose `id` a later spawn took keeps
    its record. The host cleaner does not read the records. See
    [DIVERGENCES.md](DIVERGENCES.md) CT-2.
  - A spawn that takes the `id` of a running child leaves the record of that child
    in place. Linux and macOS rows A1 to A3 measure that.
  - A daemon that is killed leaves its records. The next daemon on that socket
    reads them at its start.
  - If `children` is a regular file or a symlink, the daemon writes no record
    through it. It logs
    `[process.Registry] <runDir>/children is not a directory; recording nothing`.
    Row SP07a measures a regular file. Row SP07b measures a symlink that leads out
    of the run dir. Linux rows SLa and SLb and macOS rows KDa and KDb measure a
    relative symlink to a folder inside the run dir. Neither side writes a record
    through it.
  - If the record cannot be written, the daemon logs
    `[process.Registry] cannot record child <pid>: <error>`. Row SP07c measures a
    `children` folder of mode 000, where the error is
    `openat children/.rec-<pid>.json.<12 chars>: permission denied`. Row P11a
    measures a `children` folder of mode 755 that another user owns. On `89cb6289`
    and claustrum the spawn succeeded, the child got both markers, no record was
    written, and the log held that line.
  - In rows SP07a to SP07c the daemon still tries the removal at the end of
    the child. It logs
    `[process.Registry] cannot remove record of child <pid>: removeat children/<pid>.json: <reason>`
    before the exited line. The reason is `not a directory` for a regular file,
    `path escapes from parent` for a symlink that leads out of the run dir, and
    `permission denied` for mode 000.
  - The removal goes through a relative symlink to a folder inside the run dir.
    Both sides remove a file `<pid>.json` in that folder at the end of the child
    (Linux row SLb, macOS row KDb). With no such file both sides log nothing
    (Linux row SLa, macOS row KDa).
  - A relative symlink that leads out of the run dir is refused like an absolute
    one. Both sides keep a file `<pid>.json` in the outside folder and log the
    `path escapes from parent` line (Linux row SLc, macOS row KDc).
  - A removal that finds no record logs nothing. That is claustrum's choice (not
    measured).
  - The daemon holds its run dir open. After a rename of the run dir, a record goes
    under the new name, and the old path is not made again (row SP08).
  - claustrum prints each of these lines after its level tag.
- The reap of the records at a `-serve` start on linux and darwin. Each row is
  measured on Linux against `89cb6289`, unless the bullet names macOS.
  - The reap runs on every socket shape, in the folder of the socket (Linux and
    macOS rows EV01a to EV01i).
  - The daemon walks the children folder in directory order, the order of `ls -U`.
    One start handles the first 128 names that end in `.json`. One more such name
    ends the walk, and the daemon logs
    `[process.Registry] more than 128 records in <runDir>/children; leaving the rest for the next start`.
    Row C1: of 131 names, the first 128 of the directory order were gone after the
    first start. The second start removed the last 3 and logged no line. Not
    measured: whether a name that does not end in `.json` counts. claustrum does
    not count it.
  - At most 64 groups get `SIGTERM` at one start, in the order of the walk (rows C2
    and C4). For more verified groups the daemon logs
    `[process.Registry] <n> more orphaned group(s) recorded in <runDir>/children than the 64 one start ends; leaving those for the next start`.
    Row C4: with 70 recorded children, 64 got `SIGTERM` and 6 records stayed.
  - claustrum reads at most 4096 bytes of a record. A record whose first 4096
    bytes are not one JSON value is removed, with no signal and no line. On
    `89cb6289`, records of 5259 and 5290 bytes were removed with no signal and no
    line while their children ran (rows C3a and C3b). Both held an `argv0` of 5000
    characters, so the rows do not tell a read limit from a field limit. A
    valid record with blanks after it, 2 MiB in all, was read (row ED14g). Row P8
    measured the bound with a pad in an added last field. Records of 4095 and
    4096 bytes got `SIGTERM`. Records of 4097 and 5000 bytes got no signal and no
    line, and they were removed. `89cb6289` and claustrum did the same in each
    of the four sizes, so the bound is 4096 bytes on both.
  - Records that the daemon keeps count toward the 128 too. Row P10 staged 128
    records of another machine and then one record of a live orphan, the last in
    directory order. In two starts, `89cb6289` and claustrum sent no signal and
    removed no record. Each start logged the 128 line and a summary with
    `0 orphaned group(s)` and `128 kept`. The orphan stayed alive.
  - The owner of a record file is not tested. Row P11b replaced the record of a
    live child with a copy that root owns (mode 644, same bytes). The next start
    of `89cb6289` and of claustrum sent `SIGTERM` to that child and removed the
    record.
  - A record that is not a child to end is dropped at once, with the line
    `[process.Registry] record <pid>.json: pid <pid> is not a child to end (<reason>); dropping the record, signalling nothing`.
    The reason is one of the texts that the bullets below list. Not measured: the
    place of this line when the 128 line and the 64 line print too. claustrum
    prints the 128 line, then the record lines, then the 64 line.
  - Each group that gets `SIGTERM` logs
    `[process.Registry] ending orphaned process group <pgid> recorded by daemon instance "<instance>" (pid <daemon pid>): SIGTERM`.
    A group that gets the `SIGKILL` after the 2 s grace logs
    `[process.Registry] group <pgid> outlived SIGTERM for 2s: SIGKILL`.
    Linux and macOS rows A4 and D1 measure both lines.
  - The records of the groups that got `SIGTERM` are removed together, when the
    last group is done. Row RP04: of five groups, all five records were gone at
    the 2.1 s poll and none earlier.
  - The sweep ends with one summary line when it sent a `SIGTERM` or kept a record:
    `[daemon] serve: predecessor's children — <n> orphaned group(s): <a> ended on SIGTERM, <b> on SIGKILL, <c> survived; <d> stale record(s) dropped, <e> kept`.
    A sweep that only removed records logs no summary (row C1, second start).
    `<n>` counts the groups that got `SIGTERM`. `kept` counts the records of a live
    owner or of another machine, and the verified groups over 64 (row C4:
    `6 kept`). `dropped` counts a record with the record line (row RP07), a record of a
    dead pid (row C1: `127 stale record(s) dropped`), and a group that no longer
    verifies after its `SIGTERM` (row PG05a). Not measured: the number
    that the other removed records take. claustrum counts each of them as dropped.
  - Only a regular file is read as a record. A symlink, a FIFO or an empty folder
    with a `.json` name is removed, with no signal and no log line (rows ED16a to
    ED16c). The target of the symlink stays.
  - A record with a `program` key is accepted in three cases. The process runs
    `argv0`. The process runs `program`, by its full path or by its base name
    (rows PG02a, ED03a and ED03b). One whole argument of the process equals
    `program` byte for byte (rows PG02b, ED04a and ED04e).
  - An argument does not match by its base name, by a cleaned path, or as a part
    of a longer argument (rows ED04b, ED04c and ED04d). An empty `argv0` is refused
    before `program` is read (row ED02c). `f6010b97` sends no signal in rows ED03a
    to ED04e.
  - On Linux the daemon reads the process from `/proc`. The program is the first
    argument of `/proc/<pid>/cmdline`, and the arguments are whole, blanks included.
    A recorded name matches the program when the two are equal or have the same
    last path component. A `program` with a blank matches the argument that equals
    it (Linux row ED04e: `SIGTERM`).
  - On macOS the daemon judges the process by the command text that `ps` prints,
    as the macOS rows of `89cb6289` do. The words of that text are cut at blanks. The first
    word is the program. A recorded name matches in three cases. It equals the
    whole text. The text starts with it and one blank. It has the last path
    component of the first word.
  - So on macOS a program at a path with a blank is still ended (rows PG04s and
    PG02s, `/tmp/e/bin/my stub`). A `program` with a blank is never taken as an
    argument, because no single word equals it (macOS row ED04e: no signal).
  - These macOS rows are equal on `89cb6289` and claustrum in 3 of 3 runs: PG02a,
    PG02b, ED03a, ED04a and B1 to B8 send `SIGTERM` or none as the rule gives, and
    ED01a sends none. Rows ED03b and ED04b to ED04d are measured on `89cb6289`
    only. For claustrum they follow from the rule.
  - Rows B4a to B4c measure two blanks and a tab on macOS. A path with two blanks
    is ended when the record holds both blanks (B4a), and is not when the record
    holds one (B4b). `ps` prints a tab as the four characters `\011`, so a record
    with a tab matches no text and the child gets no signal (B4c). Not measured: a
    `program` key with two blanks or a tab.
  - The children folder must be a real folder inside the run dir. The daemon lists
    it through the run dir that it holds open, so the reap never leaves the run
    dir. If `children` is a symlink to a folder, the daemon reads nothing, sends
    nothing and removes nothing. It logs
    `[process.Registry] <runDir>/children is not a directory; reaping nothing`
    before the listening line. Rows SYa and SYb measure that on `f6010b97` and
    `89cb6289`, for a folder outside the run dir and for one inside it. claustrum
    does the same for every `children` entry that is not a real folder. The other
    shapes are not measured.
  - During the 2 s grace, a group that got `SIGTERM` takes the whole record test
    again at each poll, and once more before the `SIGKILL`. If the test fails, the
    daemon sends nothing more. claustrum removes that record at the end of the
    sweep, with the records of the other groups. When `89cb6289` removes it is not
    measured. Row PG05a
    measures a child that replaced itself with another program: `89cb6289` logs
    the line below 0.1 to 0.3 s after its start.
  - The line is
    `[process.Registry] group <pgid> no longer verifies (<reason>); nothing more is sent to it`.
    The reason is the text of `89cb6289` for the test that failed, where a row
    measures one. Otherwise the text is claustrum's own. For row PG05a it
    is `it does not run the recorded program "<argv0>"`. With a `program` key the
    text goes on with `and neither runs nor was handed "<program>"` (rows ED04b to
    ED04d).
  - The other reason texts are these. `it does not lead its own process group`
    (row RP07). `it did not start at the recorded "<start>": the pid has been reused`
    (row ED07).
    `it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it`
    (row ED10a).
    `it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one`
    (row ED10b).
    `it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)`
    (rows ED10c and ED10d).
    `environment unreadable: open /proc/<pid>/environ: permission denied` (one run
    of row PG05a).
  - Those rows measure each text in another line of the reference, at the first
    test of a record. In the `no longer verifies` line, only the program text and
    the environment text are measured.
  - A group that is gone at a poll counts as ended on the `SIGTERM`. A read that
    fails because the process has just ended is not a failed test. Rows K5 and
    ED04a measure a child that exits on the `SIGTERM`: `89cb6289` logs
    `1 ended on SIGTERM`.
  - A read that takes the leader for gone is not final. claustrum waits 50 ms and
    reads the leader again. If the leader is still gone and the group still
    answers, the daemon sends `SIGKILL` to the group. Row RP05a measures that
    `SIGKILL` for a leader that exited and left members that ignore `SIGTERM`.
    There `89cb6289` sends it right after the `SIGTERM`, with no wait.
  - The 50 ms wait is claustrum's own value. It is there for a child that replaces
    its program on the `SIGTERM`. In the first 30 runs of row PG05a (three sets of
    ten), `89cb6289` sent a `SIGKILL` within 4 ms of the `SIGTERM` in 9 runs, and
    the child ended. In its other 21 runs the child stayed alive. With the wait, a
    leader that reads alive again takes the record test and gets the
    `no longer verifies` line. This is [`DIVERGENCES.md`](DIVERGENCES.md) → D20.
  - Measured on Linux with the wait, in 30 later runs of row PG05a for each
    binary: claustrum ended the child in no run, and `89cb6289` in 5 runs. In 10
    runs of row RP05a, claustrum sent the group `SIGKILL` 51 to 55 ms after the
    `SIGTERM` in 9 runs and 152 ms after it in one. `89cb6289` sent it 0.4 to
    4.9 ms after the `SIGTERM`.
  - claustrum prints each line of the reap after its level tag. `89cb6289` prints
    no level. The level of each line is claustrum's own choice. The 128 line, the
    64 line and the outlived line are WARN. The `is not a directory` line is
    ERROR. The others are INFO.
- The log lines of a spawn and of an exit. They are off-wire. Each one is measured
  against `89cb6289`. The macOS rows CW01 to CW05 are equal on `89cb6289` and
  claustrum.
  - A `cwd` that is missing or is not a folder logs
    `[process.Manager] Failed to start process <id>: <message>`. The message is the
    message of the error frame (Linux rows CW01, CW02a, CW02c, CW04c and CW05,
    Windows rows CW01, CW02a, CW02c and CW04c). On Windows, rows K4, CW01,
    CW02a, CW02c, CW04a, CW04b and CW04c are equal on `89cb6289` and claustrum:
    the same frame, and the same line apart from the level tag. Neither side
    makes a `children` folder or a record file there.
  - The exited line is `[process.Manager] Process <id> exited with code <n>`. A
    child that a client request ended adds `, signalled at client request` (Linux
    rows SP03a and SP03b). A child that a signal ended adds
    `, terminated by <signal>` (Linux row SP04, with `SIGKILL`). A plain exit adds
    nothing (Linux row SP01).
  - A line with both parts has the signal part first. `89cb6289` printed
    `Process <id> exited with code -1, terminated by SIGKILL, signalled at shutdown request`
    in Linux row STb, a `server.shutdown` request with two children.
  - That line is a race on the reference. `89cb6289` printed it in row STb, and
    printed no exited line in two runs of rows RP11 and RP12a. claustrum prints it
    when the exit goroutine reaches the line before the daemon exits. Measured on
    Linux: one line for two ended children in rows STa and STb (three or more runs each).
    In a later Linux stop row with two children, each side printed one such line,
    for one child. `89cb6289` printed it after the `cleanup:` line, and claustrum
    before it.
    In rows RP11, RP12a and RP12b the count was none, one or two, and it changed
    between runs.
  - When the drain grace expires, the daemon logs
    `[process.Manager] Process <id>: pipe drain grace expired (grandchild holding stdio?); force-closing`
    before the two read-error lines (Linux row SP05).

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
      `process.kill` does not kill the tree. Use `signal:"KILL"` for that.
      `killAndWait` with `escalate:true` sends the group `SIGKILL` only when its
      grace runs out.
  The split does not apply on Windows. There every signal form ends the direct
  child only, with exit code 1. See [Windows child trees](#windows-child-trees).
- On Linux the `TERM` to the direct child is `pidfd_send_signal` on the pid file
  descriptor that the start of the child gave. A `KILL` is `kill(-<pid>, SIGKILL)`.
  A Linux VM measured both calls with `strace` against `89cb6289` (row E), for
  `process.kill` and for `process.killAndWait`. The `SIGTERM` of a session
  supersede is the same call (row B1). In claustrum `INT` and `HUP` go the same
  way, and they are not measured. On a kernel without pid file descriptors, and on
  macOS, the daemon sends `kill(<pid>, <signal>)`. The reference on such a kernel
  is not measured.
- claustrum diverges here. It skips the signal when the child has already exited,
  because the OS can recycle a reaped pgid. This is OS-level only, and the reply is
  identical.

##### Windows child trees

On Windows a spawned child is in no Job Object. A kill ends the direct child only,
with exit code 1. The descendants of the child live on. A Windows VM measured this
against `89cb6289` with a tree of a child, a grandchild, a great-grandchild and an
orphaned grandchild:

| action | direct child | descendants at 1 s, 5 s, 15 s |
|---|---|---|
| `process.kill` with no signal, `TERM`, `INT`, `HUP`, `KILL`, `SIGTERM`, `SIGKILL` | ended, code 1 | alive |
| `process.killAndWait` default, `escalate:false`, `signal:"KILL"` | ended, code 1 | alive |
| `-stop` and `server.shutdown` | ended, code 1 | alive |
| `taskkill /F` of the daemon | alive | alive |

A later `-stop` of the daemon does not end the descendants either. A child reported
`job=false jobflags=none`. On a Windows VM, the children of `f6010b97` and
`89cb6289` were alive 60 s after `taskkill /F` of the daemon (rows WJ02, WJ05,
WJ06). A new daemon on the socket did not end them in 120 s (row WJ07). A second
daemon beside a live first one left them alive for 60 s (row WJ04). A clean stop
ended them with exit code 1 (row WJ03, and row WJ08 on `89cb6289`).

A later run on that VM put claustrum beside `89cb6289`. In each row of the table
and in rows WJ01 to WJ08, the same processes were alive on both. The children of
claustrum reported `job=false jobflags=none` too.

With descendants that have their own stdio, both sent these frames with equal bytes.
`process.kill` answers `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`.
`process.killAndWait` answers
`{"jsonrpc":"2.0","id":1,"result":{"found":true,"died":true}}`. The exit frame is
`{"type":"stream","processId":"c1","stream":"exit","seq":1,"exitCode":1,"killedBy":"client"}`.
The log line of each killed child is
`[process.Manager] Process c1 exited with code 1, signalled at client request`.

Descendants that inherit the stdout and stderr of the child hold the pipes of the
daemon open after the child ends. The exit drain then runs to its 5 s bound, as on
Linux. Measured against `89cb6289`, with the same frames on claustrum:

- `process.kill`, also with `signal:"KILL"`: the reply comes at once, and the
  exit frame comes 5.002 s to 5.004 s later.
- `process.killAndWait` with defaults, also with `signal:"KILL"`: the reply comes
  after 5.004 s to 5.006 s, and it is
  `{"jsonrpc":"2.0","id":1,"result":{"found":true,"died":true,"escalated":true}}`.
  In claustrum the 3000 ms grace ends before the drain does, so the daemon
  escalates. The kill of the escalation meets a child that has ended. Both log
  `[process.Manager] KillAndWait c1: group kill failed: TerminateProcess: Access is denied.`
- `process.killAndWait` with `escalate:false`: the reply comes after 3.002 s, and
  it is `{"jsonrpc":"2.0","id":1,"result":{"found":true,"died":false}}`. The exit
  frame comes at 5.004 s.
- In each of these forms both log
  `[process.Manager] Process c1: pipe drain grace expired (grandchild holding stdio?); force-closing`,
  then `[process.Manager] stdout read error for process c1: read |0: file already closed`
  and the same line for `stderr`.

This is a wire change on Windows against earlier claustrum builds. Those builds
ended the whole tree through a Job Object, so the pipes closed at once. For the
same requests one such build sent the exit frame within milliseconds. Its
`killAndWait` answered `{"found":true,"died":true}` with no `escalated` member.
That is one build on a Windows VM. Not measured: a child that ignores a console
signal, and more than three generations.

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
      sends the SIGKILL at the end of the grace also when the graceful signal
      already ended the child. A process in the group of the child that holds the
      stdout pipe keeps the drain pending past the grace. A Linux VM measured both
      cases against `89cb6289`: a child that ignores `SIGTERM` (row B2), and a
      child that exited with such a pipe holder in its group (row X1). In both,
      `kill(-<pid>, SIGKILL)` came about 3 s after the `SIGTERM`. `false` leaves the process running and reports
      `{"found":true,"died":false}`, with no `escalated` and no SIGKILL, which
      spares the tree. On Windows the escalation ends the direct child only. See
      [Windows child trees](#windows-child-trees).
- A process that dies within the grace → `{"found":true,"died":true}`, with no
  `escalated`. Its group gets no `SIGKILL`. On a Linux VM (rows X2a and X2b, by
  `strace`), the child exited on `SIGTERM`, and a kid in its group had its own
  stdio. `89cb6289` sent the `SIGTERM` to the one process and made no other call.
  That holds for `process.killAndWait` with the default (3 of 3 runs) and for a
  session supersede (4 of 4 runs). The kid was alive 5 s later. Build `ac5cadb`
  of claustrum sent `kill(-<pid>, SIGKILL)` right after the exit of the child,
  and the kid ended. claustrum is now built to those rows.
    - That group `SIGKILL` came from a run against `5db5e4a` with a child that
      backgrounds a sleeper. Whether the sleeper held the pipes of the child is
      not recorded.
    - Four more forms were equal on `89cb6289` and build `ac5cadb` (rows X2c to
      X2f). `escalate:false` and a plain `process.kill` sent the one `SIGTERM`.
      `process.kill` with `signal:"KILL"` and `-stop` sent one group `SIGKILL`.
    - macOS rows X2a to X2c give the same on `89cb6289` and claustrum: the kid
      lives, 3 of 3 runs each. Windows has no X2 row. There is no group there, so
      nothing changes.

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
| `-keep-children` | `keep-children` | off | survive restart (CT-2) | -serve |
| `-listen-pipe` | `listen-pipe` | off | named-pipe transport, Windows-only (CT-5) | -serve |
| `-max-extract-bytes <n>` | `max-extract-bytes` | `0` | cap `files.extract_tar` bytes (D3) | -serve |
| `-git-timeout <dur>` | `git-timeout` | `0` | deadline on git invocations (D5) | -serve |
| `-files-read-regular-only` | `files-read-regular-only` | off | refuse non-regular `files.read` (D4) | -serve |
| `-max-cli-bytes <n>` | `max-cli-bytes` | `0` | cap CLI decompress + download (D10) | -install |
| `-cli-probe-timeout <dur>` | `cli-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The direct `<cli> --version` run always has its 30 s and 120 s bounds (retired D11) | -install |
| `-cli-download-timeout <dur>` | `cli-download-timeout` | `0` | download deadline (D12) | -install |
| `-libc-probe-timeout <dur>` | `libc-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The `ldd` probe always has its 5 s bound (retired D14) | -install |
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
runs the RPC server. On success it
prints `Claustrum remote server listening on <socket> (pid <pid>, instance <32-hex>)`
to stdout.

On Unix the daemon runs no login shell at its start. The first `process.spawn`
reads the login-shell PATH, and that spawn waits for the read. The daemon keeps
the result for its life, and it keeps a failed read too. Linux and macOS VMs
measured that against `89cb6289` (rows G1 and G2). No shell ran in 20 s without a
spawn. The first spawn ran the shell, and the second spawn ran none. A Linux VM
measured a login shell that sleeps 10 s (row G3). The shell was killed after 4 s.
The first spawn answered after 8 s, because the SSH agent read ran that shell
again for 4 s. The second spawn answered at once.

When `$SHELL` is an executable file, login-shell PATH extraction on Unix runs
`$SHELL -l -i -c …`. Otherwise it runs the first usable of `/bin/zsh`,
`/bin/bash` and `/bin/sh`. zsh comes first, which matches the reference. The value
reaches
`process.spawn` children as their `PATH` only. It never reaches the daemon's own
environment, so it never changes how the daemon resolves a `command`. The
extraction has a cap of 4 s. On a timeout the daemon discards whatever the shell
printed, even a valid PATH, and children fall back to the inherited PATH.

The cap also holds after the shell is gone, while another process holds the
output pipe of the shell. On a Linux VM (rows P13 and P13k), the login shell left
`setsid sleep 30` on the pipe and ended after 0.06 s. The first spawn of
`89cb6289` answered after 4.6 s, in 3 of 3 runs. Its log held
`[shellenv] Failed to extract PATH from login shell: shell PATH extraction timed out (<shell>)`.
The daemon sent no signal, and the second spawn answered at once. claustrum is
built to those rows. claustrum answered after 4.6 to 4.7 s with the same line
(Linux row P13, 3 runs). A macOS VM measured the same (row P13): the first spawn
answered after 4.7 s on `89cb6289` and after 4.7 to 4.9 s on claustrum, with the
same line. One part is claustrum's own and is not measured: it closes its end of
that pipe two caps after the shell ended.

The shell of the PATH read gets a small environment. On a Linux VM (row P13v),
the PATH shell of `89cb6289` got seven entries from the daemon. They were
`DISABLE_AUTO_UPDATE=true`, `HOME`, `PATH`, `SHELL`, an empty `TERM`, `USER` and
`ZSH_DISABLE_COMPFIX=true`. The daemon had no `TERM`. Its `LANG` and its
`CLAUDE_SSH_DAEMON_CHILD` did not reach the shell. claustrum is built to that
row. macOS rows P13v and P13z gave the same seven entries on `89cb6289` and on
claustrum. In row P13z the daemon had no `SHELL`, and the entry was empty on
both. Not measured: the order of the entries, and a daemon that has a `TERM`.
Also not measured: a daemon with no `HOME`, `PATH` or `USER`.
claustrum copies the five values from its environment and writes an empty value
for a name that is not set. The shell of the SSH agent read gets another
environment. In Linux row P13v and macOS row P13z that one was equal on
`89cb6289` and claustrum.

A token source is required. The detached child tests for it, not the launcher:
- Both flags missing → the launcher daemonizes anyway, the child refuses to start,
  and the launcher reports its accept timeout after 12 s:
  `claustrum: timeout waiting for daemon to accept on <socket>`, exit `1`. The
  specific reason (`claustrum: daemonized child requires --token-file or
  --token-fd`) reaches only the child's detached stderr. This is deliberate
  parity. `5db5e4a` exited 1 after 10.07 s in that case. A zero-byte
  `-token-file` behaves identically. On a Linux VM (row P7), the launcher of
  `89cb6289` exited 1 after 12.06 s with a zero-byte `-token-file`, in three
  forms: a stale socket file, no socket file, and an empty run folder. The
  launcher bound of claustrum is 12 s, to that row. Both flags missing is not
  measured on `89cb6289`.
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
- `-keep-children` is CT-2. It is off by default, so a graceful
  shutdown kills the children: the whole tree on Linux and macOS, the direct child
  on Windows. When set, it leaves spawned children running
  across a restart, and logs `[Server] -keep-children: leaving <n> running child
  process(es) alive across shutdown`. On Linux and macOS that shutdown also
  removes the records of the kept children. The reap of the next daemon start
  then sends them no signal. That rule is claustrum's own, and CT-2 names its two
  limits: a process whose `id` a later spawn took, and the host cleaner. The new daemon does not re-adopt them, and
  the survivors lose their stdio. Their stdin reaches EOF, and a stdout or stderr
  write gets SIGPIPE, or EPIPE for a child that ignores SIGPIPE, such as Node. It
  therefore suits only children that tolerate dead stdio. The stdio of a survivor
  is not measured on Windows. The flag works on
  Windows too, as claustrum's own flag. There it changes each graceful shutdown:
  the direct child stays alive as well. On a Windows VM the whole tree of claustrum
  was alive after `-stop` and after `server.shutdown` with the flag. The reference
  has no such flag. On that VM `89cb6289` refused it with exit code 2.
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

A holder with the short record, a daemon that has not bound yet, gives the first
two lines with `pid <N>` and no instance. On a Linux VM (row N5), `89cb6289`
logged `[daemon] stop: run dir is held by a live daemon, pid <N>; sending SIGTERM`
and `[daemon] stop: previous daemon pid <N> exited after SIGTERM`. claustrum is
built to that row. The other lines are not measured for a short record, and
claustrum writes them with `(instance "")`.

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
claustrum -install -cli-version <v> [-cli-dir <d>] \
          [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>] \
          [-max-cli-bytes <n>] [-cli-download-timeout <dur>]
```

`-install` downloads, verifies, extracts and prunes, and then prints one
`__INSTALL_RESULT__<json>` facts line (schema in
[ARCHITECTURE.md](ARCHITECTURE.md)). `-install` exits `0` whenever it prints
the facts line. It reports a failure inside the facts as `cliError`, not through
the exit code. Three cases print no facts line. A flag that does not parse exits
`2`. `-cli-version` with no `-cli-dir` and no home folder exits `2`. Any other
run with no home folder exits `1` (see the default CLI folder below). `-install`
reaches the network only with `-cli-url`.

The `cliError` catalogue follows:

| `cliError` | trigger |
|---|---|
| `installed cli at <path> is not runnable` | the `--version` run of a new CLI did not start or exited non-zero. `<path>` is the final path |
| `cli <v> missing and no --cli-url or --cli-zst provided` | cache miss with no source flag |
| `cli unresponsive: the installed Claude Code binary started but did not answer --version within 30s (120s for a first run) and was stopped; the host is not letting it run (endpoint security software or a stalled network home are the usual causes)` | a direct `--version` run stopped at 30 s (120 s after an install in the same run). Always-on, parity (see below) |
| `checksum mismatch: expected=<x>, actual=<y>` | `-cli-checksum` verify failed. This applies to `-cli-url` always, and to `-cli-zst` only when a checksum is supplied. The compare is case-sensitive |
| `opening input: <err>` | `-cli-zst` read error |
| `decompressing: <err>` | bad zstd blob, for example `invalid input: magic number mismatch` |
| `decompressing: decompressed CLI exceeds <n> bytes` | D10 cap, opt-in |
| `download failed: response exceeds <n> bytes` | D10 cap on the download body, opt-in |
| `download failed: <transport err>` | a transport error before the body, for example a refused connection |
| `download failed: Get "<url>": net/http: timeout awaiting response headers` | no response headers for 60 s (always-on, parity, see below) |
| `download interrupted after <got>/<total> bytes: <transport err>` | a body read that ended with a transport error, for example `read tcp …: read: connection reset by peer` (parity, see below) |
| `download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)` | D12 download deadline, opt-in |
| `download stalled: no data for 60s after <got>/<total> bytes` | read-idle abort, meaning no bytes for 60 s on the `-cli-url` body (always-on, `4534d86` parity, VM-measured) |
| `mkdir cli dir: <err>` | cli-dir uncreatable. `cliPath` is then empty. A file or a FIFO at the cli-dir path is replaced first (see Staging and cleanup) |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli version "…" collides with the install download blob` | version starting `.blob-` (D18) |
| `cli path must not be or contain the home directory: "<path>"` | a folder at the final path that is the home folder or one of its parent folders (D2 hardening) |
| `clearing stale dir at <path>: <err>` | an occupied `cliPath` directory that claustrum cannot remove |
| `staging file vanished before install: <err>` | a concurrent sweep took the staging file |
| `cli unresponsive: the installed Claude Code binary was started through the host's managed launcher <argv0> and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish` | a managed launcher run stopped at 33 s (123 s after a fresh install), with `CLAUDE_SSH_MANAGED_LAUNCHER=1` (see below) |

The managed launcher (`89cb6289` parity):
- `-install` uses the [managed launcher](#launcher-added-89cb6289) only when
  `CLAUDE_SSH_MANAGED_LAUNCHER` is exactly `1`. The values `true` and `1 ` (a
  trailing space) and an unset variable add no field and run no launcher, even with
  a usable launcher. claustrum treats every value other than `1` as off. That is
  claustrum's choice (not measured for other values).
- With the gate and a CLI path, `-install` resolves the launcher for that path, as
  `launcher.resolve` does. The facts line then gains launcher fields after
  `cliWasPresent`, `cliError` and `cliUnresponsive`, in this order:
  `launcherStatus`, `launcher`, `launcherSource`, `launcherPath`,
  `launcherReason`, `launcherStderr`. A field that does not apply is omitted.
  With `-cli-url`, `fetch` comes after `cliUnresponsive` and before
  `launcherStatus`. That is measured on a Linux VM for the statuses `usable`,
  `none`, `unusable`, `probe_failed` and `unresponsive`.
- Without `-cli-version` the `cliPath` is empty and the facts line gains no
  launcher field. A `cliPath` that is empty because the cli-dir cannot be made
  gains no launcher field either (measured on a Linux VM with the gate, against
  `f6010b97` and `89cb6289`). Without
  `-cli-version`, with or without `-cli-dir`, the line ends
  `"cliPath":"","cliWasPresent":false}` and no launcher runs. That is measured on
  a Linux VM with the gate, for a usable launcher, an unusable one and none.
- With `-cli-version` and no `-cli-dir`, the CLI path is in the default CLI
  folder (see below), with the gate and without it. The launcher gets that path
  (measured on macOS).
- `none`: the CLI runs directly, as without the gate, under the direct bounds
  below. That run keeps the gate in its env. The facts end
  `"cliWasPresent":true,"launcherStatus":"none"}`. A stopped direct run ends
  `"cliUnresponsive":true,"launcherStatus":"none"}` (measured on Linux, macOS and
  Windows).
- `usable`: the CLI runs once, as `<launcher argv...> <cli> --version`. No direct
  run happens. The launcher's stdout is not reported, and its stderr does not
  reach `-install` stderr. The facts add `launcher` and `launcherSource`.
- `unusable` or `unreadable`: the CLI does not run at all. `cliWasPresent` stays
  true, and there is no `cliError`. The facts add `launcherSource` or
  `launcherPath`, and `launcherReason`.
- A launcher run that exits non-zero, or ends on a signal, is `probe_failed`.
  `launcherReason` is `exit status <n>` or `terminated by SIGTERM`. The CLI file
  stays, `cliWasPresent` stays true, and there is no `cliError`.
  `launcherStderr` holds the launcher's stderr and is omitted when it is empty.
- `launcherStderr` keeps 8192 bytes. More stderr adds `\n[… <n> more
  bytes]`, with the JSON escape `\n` on the line and `…` as raw `e2 80 a6`.
  `<n>` is the count of dropped bytes. Measured with 10000 bytes: 8192 kept and
  1808 named. claustrum's choices (not measured): it keeps the first bytes, and it
  adds the `\n` even when the kept part ends in a newline.
- On a cache hit, a launcher run that has not ended at 33 s is stopped. The facts
  then read `"cliWasPresent":false`, the `cli unresponsive: …` `cliError` above,
  `"cliUnresponsive":true`, `"launcherStatus":"unresponsive"` and
  `"launcherReason":"did not exit within 33s and was stopped"`. The CLI file
  stays. The reference stopped a 60 s launcher at 33.057 s and left no process.
  This bound is parity. With a usable launcher the direct bounds below do not
  apply. A CLI that answers at 31 s through a launcher is present (measured on
  Linux and macOS).
- The run right after a fresh install has a bound of 123 s. The reference stopped
  a 300 s launcher at 123.048 s after a `-cli-url` install and at 123.066 s after
  a `-cli-zst` install (Linux VM). It let a 100 s launcher finish as `usable`
  after a `-cli-url` install.
  The stopped facts are the ones above, with
  `"launcherReason":"did not exit within 123s and was stopped"`. The `cliError`
  text is the same. The CLI is installed all the same, and the blob is consumed.
- No install follows a stopped launcher run on a cache hit, and nothing is
  pruned. The sweep runs: a swept file more than 10 minutes old is removed
  (measured on Linux and macOS).
- claustrum's choices (not measured): the stop kills the run's whole process
  group, then waits up to 2 s for its output. The texts name the launcher's
  argv[0] only. No install follows a stopped launcher run on a cache hit when a
  source flag is given either.
- A fresh install puts the CLI at its final path, then runs it once through a
  usable launcher. The facts read `"cliWasPresent":false` with the usable fields.
  The launcher gets the final path (measured on Linux and macOS).
- Right after a fresh `-cli-url` install, a failed run still installs the CLI.
  An unusable launcher installs the CLI without a run. Both are measured on a
  Linux VM. claustrum does the same for an unreadable answer and for `-cli-zst`.
  That is claustrum's choice (not measured).
- After a stopped first run no prune follows (measured on Linux and macOS).
- The launcher run gets the daemon env without `CLAUDE_SSH_MANAGED_LAUNCHER`
  (measured on Linux and macOS). claustrum also drops `CLAUDE_CODE_PROCESS_WRAPPER`
  and `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` there, as for a spawn with a launcher.
  That is claustrum's choice (not measured).
- `-install` still exits `0` with an empty stderr, and it writes no
  `[LauncherHandler]` line.
- On Windows the resolve answers none. With the gate and a CLI path `-install` appends
  `"launcherStatus":"none"` last and runs the CLI directly (measured on a Windows
  VM).

The direct `--version` run and its bounds are parity. They are measured on
`f6010b97` and `89cb6289`. A direct run is a run with no usable launcher: the gate
is unset, or the launcher status is `none`.
- On a cache hit, a run that has not ended after 30 s is stopped. The facts
  then read `"cliWasPresent":false`, the `cli unresponsive: …` `cliError` of a
  direct run and `"cliUnresponsive":true`. `-install` exits `0` with an empty
  stderr. A CLI that answers at 28 s is present. Measured on Linux, macOS and
  Windows. A CLI that answers at 31 s is stopped (Linux and Windows).
- The run of a CLI that this run installed is stopped at 120 s, with the same
  text. A CLI that answers at 118 s installs with no error. Measured on Linux,
  macOS and Windows. A CLI that answers at 121 s is stopped (Linux and Windows).
- The class of the bound follows the install in this run. With a file that does
  not run at the final path and a blob, the new CLI gets the 120 s bound
  (measured on Linux).
- The download time is outside the bound. A 15 s download and a CLI that answers
  at 110 s install with no error (measured on Linux and Windows).
- claustrum starts the clock right before the process start call. Measured on
  Windows against `89cb6289`: a stopped run has a wall time of 30.01 to 30.06 s,
  or 120.02 to 120.06 s. The start delay of the CLI does not change it. That the clock
  of the reference starts before the start call is inferred from those wall
  times. On Linux and macOS both readings fit the rows.
- On Linux and macOS the CLI runs in a process group of its own, in the session
  of `-install`. The stop ends a child in the group of the CLI. A child in a new
  session survives. A CLI that ignores SIGTERM is stopped at the same time.
  claustrum kills the group with SIGKILL. The name of the signal is not measured.
- On Windows the stop ends the CLI process. Its children stay alive (measured).
- A process that holds the stdout and stderr of the CLI after the CLI exited
  delays nothing (measured on Linux, macOS and Windows). claustrum gives the CLI
  the null device as stdin, stdout and stderr. What the reference gives it is
  not measured.
- After a stopped cache hit the CLI file stays, and nothing is installed. A
  `-cli-zst` blob stays and nothing is pruned. The sweep runs. Measured on
  Linux, macOS and Windows. A `-cli-url` server gets no request (Linux and
  Windows).
- After a stopped first run the new CLI stays at its final path, the `-cli-zst`
  blob is consumed and nothing is pruned. Measured on Linux, macOS and Windows.
  With `-cli-url` the facts carry `fetch` after `cliUnresponsive`.
- Not measured: a CLI that exits non-zero after a long time, and a second
  `-install` on the CLI that a stopped first run left.

The default CLI folder is parity, measured on `89cb6289`:
- With `-cli-version` and no `-cli-dir`, or an empty `-cli-dir`, the CLI folder
  is `<home>/.claude/remote/ccd-cli`. Measured on Linux, macOS and Windows. On
  Windows `<home>` is `USERPROFILE`, and `HOME` is not read.
- The other steps work in that folder as in an explicit one. The install from
  `-cli-zst` or `-cli-url` is measured on Linux, macOS and Windows. The cache hit
  is measured on macOS and Windows. The launcher run is measured on macOS.
- A cache miss makes the folder chain, with or without a source flag. Each new
  level gets mode `0700`. A level that exists keeps its mode. Measured on Linux
  and macOS. With umask `000` the new levels still get `0700` (Linux). The
  Windows listing shows no modes.
- Without `-cli-version` the `cliPath` is empty, nothing runs and no folder is
  made. The default folder does not apply (measured on macOS and Windows).
- With no home folder, `-install -cli-version <v>` with no `-cli-dir` prints
  nothing on stdout and exits `2`. The reference prints
  `claude-ssh: cannot resolve home directory: $HOME is not defined` on stderr.
  claustrum prints the same line with its own prefix, `claustrum: `. Measured on
  Linux against `f6010b97` and `89cb6289`, with `HOME` not in the environment.
- Not measured: no home folder together with `-cli-dir`, without `-cli-version`,
  or in another mode. claustrum exits `1` in each of those cases, with the same
  stderr line.
- Not measured either: macOS, and a missing `USERPROFILE` on Windows. claustrum
  exits `2` there for the same arguments. On Windows its stderr line names
  `%userprofile%`.

The CLI file name on Windows is parity, measured on a Windows VM against
`89cb6289`:
- The CLI file is `<cli-dir>\<version>.exe`. The `cliPath`, the file on disk,
  the path of the `--version` run and the `not runnable` text all carry that
  name. The `missing` text names the version with no suffix.
- Only `<version>.exe` counts as present. A file with the bare name counts for
  nothing, and it does not run.
- The suffix is added always. `-cli-version 9.9.9.exe` installs `9.9.9.exe.exe`.
- The prune counts every file, with or without the suffix.
- A folder at the bare name stays. A folder at the `.exe` name is replaced by
  the CLI file.
- A CLI that an older claustrum installed at the bare name counts for nothing.
  `-install` answers `missing` for it until a source flag installs the `.exe`
  file beside it.
- Not measured: a version that ends in `.EXE`, and which of `<v>` and `<v>.exe`
  the prune keeps when `-cli-keep` leaves room for one of them. A version that
  the sweep claims by its end, such as `1.0.zst`, installs as `1.0.zst.exe`. The
  sweep does not claim that name. No row measures that case.

Download progress and `fetch` stats came with `4534d86`, on the `-cli-url` path:
- While downloading, `-install` prints `__INSTALL_PROGRESS__<json>` lines to
  stdout: `{"phase":"download","bytes":<n>[,"total":<m>]}`.
  `total` carries the Content-Length and is dropped when the server sends none,
  as with a chunked body.
- A leading `bytes:0` line comes first, after the response headers and before
  any body byte. A server that sends no headers gets no line (measured on
  `89cb6289` on Linux and Windows). A final
  `bytes:<n>` line comes only after the checksum passes. A failed check prints no
  final line. Measured on a Linux VM against `f6010b97` with a 57-byte blob, 5 of
  5 runs per row: `bytes:0` then `bytes:57` on success, and `bytes:0` alone on a
  mismatch.
- Between those two lines a tick comes each 1 s. A tick prints a line only when
  the byte count differs from the last printed line. Measured on `89cb6289`: a
  body of 6 parts that are 20 s apart prints 7 lines, not one line per second
  (Linux, macOS and Windows). A body with no byte prints the leading line only
  (Linux and Windows). A body with no Content-Length follows the same rule (Linux and
  Windows). claustrum printed a line on every tick before.
- The tick lines show the count at the tick, so their byte counts jump
  irregularly. A consumer treats them as progress, not as a byte-exact sequence.
- Not measured: whether the final line repeats a count that a tick printed
  already, and what starts the tick clock. claustrum always prints the final
  line, and it starts the tick clock when the body becomes readable.
- The `__INSTALL_RESULT__` facts line gains a `fetch` object after `cliError`. It is
  the last field, except with the managed launcher gate (see above):
  `{"bytes":<n>,"ms":<n>,"longestPauseMs":<n>}`. Those are bytes read, download
  duration, and the largest gap between reads. It appears whenever a `-cli-url`
  download was attempted, even a 0-byte 404. It is dropped on the `-cli-zst` path
  and on the cache-hit path.
- claustrum does not close its idle connections after the download. Its
  transport is a copy of the Go default transport, whose idle limit is 90 s.
  When the client closes the download connection is measured with three kinds
  of server:
    - macOS, a server that never closes first (18 runs). `89cb6289` and
      claustrum both close the connection 90.0 to 90.1 s after the last body
      byte, while the CLI runs.
    - macOS, a server that closes at its own 5 s limit (18 runs): neither
      binary closes first.
    - Linux, a server that sends `Connection: close`: `89cb6289` closes 2 to
      51 ms after the last body byte, and claustrum 0 to 5 ms after it.
    - Not measured: Windows, and a CLI run shorter than 90 s against a server
      that never closes first.
- The wait for the response headers has an always-on 60 s limit. A server that
  reads the request and sends nothing fails the install with
  `download failed: Get "<url>": net/http: timeout awaiting response headers`.
  No progress line is printed, and `fetch` reads `"bytes":0` and
  `"longestPauseMs":0`. This is parity: `f6010b97` and `89cb6289` give up after
  60.0 s (measured on Linux and Windows). claustrum had no such limit before.
  Not measured: macOS, and the exact start point of the limit.
- A body read that ends with a transport error fails the install with
  `download interrupted after <got>/<total> bytes: <transport err>`, with no
  `download failed: ` prefix. The wait from the last byte to the error counts for
  `longestPauseMs`. One progress line is printed, and the cli-dir stays empty.
  This is parity: measured on a Linux VM against `f6010b97` and `89cb6289` with a
  connection reset after half the body. claustrum answered
  `download failed: <transport err>` with `"longestPauseMs":0` before. Not
  measured: any other read error, a body with no Content-Length, and an error
  before the first body byte. claustrum words them the same way, with a `<total>`
  of `0` for a body with no length. The opted-in D12 deadline keeps its own
  text.
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
  reference says `decompressing: unexpected EOF`. A connection reset in the
  body is not a D13 case: both binaries answer the `download interrupted after …`
  text above (Linux VM). Any other interruption is not measured. Both binaries
  fail the install either way. A download that is not
  a zstd archive, with the right checksum for its bytes, gives the same
  `decompressing: invalid input: magic number mismatch` text on both. The
  reference reports 4 bytes there: `"fetch":{"bytes":4,…}` and one progress
  line. claustrum reads the whole body first: `"bytes":4096` for a 4096-byte
  body, and two progress lines. Measured on a Linux VM against `f6010b97` and
  `89cb6289`. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D13.
- On the `-cli-zst` path claustrum verifies the blob only when a `-cli-checksum`
  is supplied, as the reference does since `7d193f89`. A mismatch answers
  `checksum mismatch` and keeps the blob. An absent or empty checksum verifies
  nothing.
- The compare is case-sensitive on both paths. The right digest in upper case
  answers `checksum mismatch: expected=<UPPER>, actual=<lower>`, as on the
  reference.
- On every cache miss claustrum creates the cli-dir (mode `0700`, with its
  parents) first. That is before the source check, before it opens a `-cli-zst`
  blob and before any `-cli-url` network access. So every failed cache miss past
  the version check leaves the cli-dir, empty if it was new. A mismatch, a 404, a
  refused connection and a missing source all leave it. That matches the
  reference: on every measured build from `5db5e4a` for `-cli-zst`, and on
  `f6010b97` for `-cli-url` and a missing source. Measured on a Linux VM.
- The folder that claustrum creates is the cleaned cli-dir path. For
  `-cli-dir <dir>/sub/..` that is `<dir>`, so it makes no folder `sub`. That is
  a code fact. `89cb6289` creates `<dir>/sub` (mode `0700`), when no folder
  `sub` exists before the run (Linux cell HS10b, three runs). In that cell
  claustrum refuses at the home guard, and no folder `sub` exists after the run.

One opt-in wall-clock bound exists, and it is off by default, so a stock
claustrum does not apply it. The other `-install` clocks in this paragraph are
always on, and each one is a clock of the reference. The direct `--version` run has its
30 s and 120 s bounds, and a launcher run has its 33 s and 123 s bounds. The
download has the 60 s response-header limit and the 60 s read-idle abort. The `ldd` probe has the
reference's 5 s bound on ldd itself and a 2 s drain after ldd exits. The stdlib
transport clocks (`net.Dialer{Timeout:30s}`, `TLSHandshakeTimeout:10s`) apply on
`-cli-url` only. They are always on, and they are not probed on the reference.
One clock is claustrum's own choice and is not measured: the wait of up to 2 s
for the output of a stopped launcher run.
See [`DIVERGENCES.md`](DIVERGENCES.md):
- `-cli-download-timeout <dur>` is D12. `0` gives `http.Client{Timeout:0}`, which
  is no bound. When armed, it bounds the whole exchange. An honest download that is
  merely too slow therefore trips `download failed: context deadline exceeded (…)`
  as surely as a black hole does.
- `-cli-probe-timeout <dur>` is a deprecated no-op. It was the opt-in D11, which
  is retired: the reference bounds the direct `--version` run itself.
- The `ldd --version` libc probe bounds ldd itself at 5 s, as on the reference
  since `19f30c46`. It is not a knob. `-libc-probe-timeout` is a deprecated no-op. See
  Staging and cleanup below.

D10 is the opt-in size cap. `-max-cli-bytes <n>`, or the `max-cli-bytes`
configuration key, governs both the decompressed CLI and the download body. `0` is
off, which is parity, because the reference took a 600 MiB payload to the
runnability test. claustrum streams the blob and never buffers it. It keeps a path,
not a `[]byte`, so the staging retry can re-read it. "Cap off" therefore does not
mean unbounded memory. See [`DIVERGENCES.md`](DIVERGENCES.md) → D10.

`-cli-version` hardening is claustrum-only:
- D6 requires a single path component. The clearing step is an `os.RemoveAll`
  on the CLI path, `<cli-dir>/<version>` (on Windows `<version>.exe`). A version
  that escapes the cli-dir
  therefore deletes unrelated data. Measured, the reference destroys the target on
  `../victim`. `link/1.0.0` through an intermediate symlink also escapes. claustrum answers
  `cli version "…" must be a single path component` and touches nothing. That rule runs on the install path. A regular file that
  is already at the joined path is first run with `--version`, as the cache-hit
  check. A run that passes answers `"cliWasPresent":true` with that path, as
  on the reference. That is measured with `-cli-version ../x` on Linux against
  `f6010b97` and `89cb6289`, and on macOS against `89cb6289`. A run that is
  stopped answers the unresponsive text and sweeps the cli-dir. D6 answers only
  when that run fails, or when no file is there. The reference has no such rule.
  With `../x` and a blob it installs to `<parent>/x`, outside the cli-dir, and
  runs that file. That is cell V1c on Linux, macOS and Windows. It
  refuses `.`, `..`, `/` and `\` on every OS. claustrum uses a single-component
  test and not lexical containment, because containment accepts `link/1.0.0`, and
  `EvalSymlinks` adds a TOCTOU window. A final component that is itself a
  symlink stays legal, because `os.RemoveAll` unlinks it and does not follow it. The
  real client passes bare versions. `1.0.86`, `2.0.0-beta.1`, a commit sha,
  `latest` and `1.0.86+build.5` are all measured as accepted.
- A version that the sweep claims, such as `.fetch-x` or `1.0.zst`, installs.
  The same run keeps it, because the sweep removes only old entries. A later
  install removes it once it is more than 10 minutes old. This matches the
  reference since `4534d86`. The retired D7 refused such a version.

Staging and cleanup:
- An install follows the order of the reference. The new CLI is put at its
  final path, the sweep runs and the download blob is removed. Then
  `<final path> --version` runs. At that run the
  cli-dir holds no `.fetch-` temp of this install, no `.blob-` file and no swept
  file more than 10 minutes old. The prune follows a good run only. A run that
  exits non-zero removes the new CLI again, so nothing is left at the final
  path. A stopped run keeps the new CLI. Measured on `89cb6289` on Linux, macOS
  and Windows. claustrum ran its staged `.fetch-<random>` file before, and it
  swept after the run.
- What is at the final path goes before the new CLI runs. A folder there is
  removed with the file in it, and at the run the cli-dir holds the CLI file
  only. Measured on Linux, macOS and Windows. A file there that does not run is
  replaced by the new CLI before the run (measured on Linux). claustrum replaced
  both only after the run before.
- claustrum does not remove a folder at the final path that is the home folder
  or contains it. It answers
  `cli path must not be or contain the home directory: "<path>"`. The folder is
  not removed, the blob stays and no CLI runs. This is a divergence
  ([D2](DIVERGENCES.md#d2)). The reference removes that folder as a tree and puts
  the CLI file there. When the new CLI then fails, the folder is gone and nothing
  is left at the path (Linux cell H2, macOS cell HX2L). Measured with
  `-cli-zst` on Linux, macOS and Windows. [D2](DIVERGENCES.md#d2) lists the
  cells and what no cell covers. Not measured: `-cli-url`.
- That guard compares the folder at the final path with the home folder and
  with each parent folder of it, by file identity. It does that for the home
  path as given and for its resolved path. The home guard of the RPC paths
  stays lexical.
- The volume of the macOS VM ignores letter case. There a `-cli-version` in
  another letter case names the folder on disk. With `-cli-version ALICE` and a
  folder `alice` that is not the home folder, both binaries replace the folder.
  The new file has the name `ALICE`, as the command gave it (cells HXKC and
  HXKC0). With `-cli-version USERS` and a folder `users` that contains the home
  folder, `89cb6289` removes the folder and names the new file `USERS`.
  claustrum refuses, and the folder keeps its name (cell HXK5).
- Not measured: a new CLI that exits non-zero over a file or a folder at the
  final path. claustrum then leaves nothing at the final path, as for an empty
  one.
- Not measured: how the reference writes the final file, and the order of its
  sweep against that write.
  claustrum decompresses to `<cli-dir>/.fetch-<random>`, renames that file to the
  final path and then sweeps. It consumes the `-cli-zst` blob after the run.
- A cli-dir path that names something that is not a folder is replaced, as on
  the reference. The trigger is a regular file, a symlink to a regular file or a
  FIFO at the cli-dir path. `-install` removes that entry. For a symlink it
  removes the link and keeps the target. It also removes the regular file named
  exactly `ccd-cli-version` in the parent folder of the cli-dir. Then it makes
  the cli folder with mode `0700` and goes on. That holds for the default CLI
  folder too, and with no source flag, before the `missing` answer.
- The name `ccd-cli-version` is literal. It does not follow the name of the
  cli-dir, and a file named `other-version`, `ccd-cli-version.bak` or
  `zzz-version` stays.
- A folder, a symlink to a folder and an absent cli-dir trigger nothing, and
  `ccd-cli-version` stays. A dangling symlink triggers nothing either: the
  answer is `mkdir cli dir: mkdir <path>: file exists` with an empty `cliPath`.
- With a parent folder that is not writable nothing is removed. The answer is
  `mkdir cli dir: mkdir <path>: not a directory` with an empty `cliPath`.
- The four bullets above are measured against `f6010b97` and `89cb6289`, one
  run for each shape: 14 shapes on Linux and on macOS, 13 on Windows. Windows has
  no FIFO row and no row with a parent that is not writable. The `mkdir` texts
  above are the Linux and macOS texts. No mode is claimed on Windows.
- Both removes are plain removes of one path. Nothing is removed as a tree.
  claustrum's remove does not open the FIFO. When a FIFO stays behind the
  cli-dir path, claustrum's sweep after the mkdir error opens that path once as
  a directory. That open fails at once (measured on Linux).
- Not measured: a socket, a device or a symlink to a FIFO at the cli-dir path.
  A `ccd-cli-version` that is a folder or a symlink is not measured either.
  claustrum leaves each of them alone. Not measured: the `-cli-url` source and the launcher gate with
  these shapes, and the order of the two removes. claustrum removes the version
  file first.
- claustrum stages the CLI at `<cli-dir>/.fetch-<random>` and renames it into
  place. It never stages at `<cliPath>.tmp`. This is one code path
  for `-cli-url` and `-cli-zst` alike. The orphan sweep matches `.fetch-*`, so it
  reclaims the litter of an interrupted install.
- A `-cli-url` download lands at `<cli-dir>/.blob-<random>`, because `ensureCLI`
  creates the cli-dir first. If the cli-dir is unwritable, it lands at
  `$TMPDIR/claustrum-fetch-<random>`. The `.blob-` prefix is deliberately
  different, so that the `-cli-keep` prune does not count it and the sweep does
  not claim an in-flight blob. That is also why claustrum refuses a
  `-cli-version` that starts with `.blob-` (D18). The install removes the blob
  before the `--version` run of a new CLI. An attempt that ends earlier removes
  the blob at its end. Only a SIGKILLed download
  leaves it behind, and nothing reclaims
  it. The sweep must not take it, because a retry re-reads the blob after the
  staging file, which is never older. No frame changes either way.
- Two differences on a completed install, which claustrum keeps for now.
  Measured on macOS against `89cb6289`, 4 runs per binary (cell HX5C). No rule
  is claimed beyond these rows.
    - `89cb6289` removes empty folders that sit beside the new CLI file in the
      cli-dir. claustrum keeps them. Both keep a regular file and a folder that
      is not empty.
    - With `-cli-zst`, the blob is already gone when the CLI runs `--version` on
      `89cb6289`. On claustrum it is still there during that run, and gone
      after it.
- The `-cli-keep` prune counts every other non-directory as a version. It skips
  every name the sweep claims, at any age. Measured on a Linux VM against
  `f6010b97`: a fresh `.fetch-o` and `x.zst` beside three real CLIs, with
  `-cli-keep 3`, leave all three real CLIs in place.
- claustrum consumes the `-cli-zst` blob once decompression succeeds, and not
  only on a fully successful install. An extracted CLI that fails the runnability
  test still costs the blob. claustrum leaves a blob that is not valid zstd alone.
  One exception: the home guard refusal (D2) keeps the blob.
- claustrum clears an occupied `cliPath`, and that is not fatal. `rename(2)`
  refuses to replace a non-empty directory, so claustrum removes it first, as a
  tree. It removes it only when `cliPath` is a directory. A regular file, which an
  installed CLI always is, is replaced atomically by the rename. If claustrum cannot remove it →
  `clearing stale dir at <path>: <err>`. If the staging file vanished →
  `staging file vanished before install: <err>`, and a file at `cliPath` stays
  untouched. On Windows the cleared name is `<version>.exe`. The D6 rule runs
  before this step, so `cliPath` is a direct child of the cli-dir.
- The orphan sweep removes a `.fetch-*` or `*.zst` entry only when its mtime is
  about 10 minutes old or more. The gate exists from `4534d86` on. `5db5e4a` and
  `7d193f89` swept at every age. On `19f30c46` through `f6010b97`, 599 s stays,
  601 s goes and a future mtime stays. claustrum removes an entry only when it is
  more than 600 s old. The names are case-sensitive, and the bare `.fetch-` and
  `.zst` count. The age is the entry's own mtime, for a symlink the link's own, so a symlink is
  judged by the link, and only the link goes. The sweep uses one `os.Remove` per
  entry. It therefore clears files and *empty* directories, and leaves a
  non-empty `.fetch-dir/` at every age. Unrelated files survive. Measured on a
  Linux VM. The sweep runs once whenever an
  install was attempted, and after a stopped run on a cache hit. The `-cli-keep`
  prune runs only on success. claustrum
  stages its extract in this same `.fetch-*` namespace, until the rename. A concurrent
  install can reclaim that staging file only once it is more
  than 10 minutes old. claustrum handles that case with a single retry of the
  staging step.
- claustrum runs `ldd` on every libc probe since build 3ef9370, and its output
  decides the answer. A "musl" banner reports `musl`, and any other output reports
  `glibc`. The `/lib/ld-musl-*.so.*` marker is consulted only when `ldd` produced
  no output.
- The probe bounds ldd itself at 5 s, as on the reference since `19f30c46`. `ldd`
  runs in its own process group. If ldd still runs at 5 s, the whole group is
  killed. Output written before that kill is dropped, so the marker decides. No log line is
  written, and the facts line keeps its shape. The bound applies on a fresh
  install and on a cache hit. Measured on a Linux VM.
- If `ldd` exits before 5 s while a child still holds its output pipe, the probe
  waits up to 2 s more. Then it uses the output it has, even past 5 s. The child
  is not killed. Measured on a Linux VM against `f6010b97` for exits from 0.5 s to
  4.5 s: the reference answered musl about 2 s after ldd exited.

### -probe-cli — classify a CLI binary

`-probe-cli <path>` runs the bounded `<path> --version` runnability probe and exits
`0`. It is how Claude Desktop classifies a CLI binary out of band, without a full
`-install` (reference build `19f30c46`). Its stdout is:

- If the CLI runs and exits 0 within the bound, stdout stays empty.
- If the deadline had to kill it, stdout is `__CLI_HUNG__\n`.
- If the binary is missing or does not run, stdout is `__CLI_BAD__\n`. Not running
  means it either fails to start or exits non-zero.
- On Windows, a path with no file at exactly that name gives `__CLI_BAD__\n`, and
  nothing starts. The probe never adds `.exe`. Measured on a Windows VM against
  `89cb6289`: with only `9.9.9.exe` on disk, `-probe-cli <dir>\9.9.9` answers
  `__CLI_BAD__` and the CLI does not run. claustrum ran `9.9.9.exe` there before.
  Not measured: the exact test of the reference, a folder, a relative path and a
  path with another extension. claustrum tests only that the path exists.

With `CLAUDE_SSH_MANAGED_LAUNCHER=1` the probe uses the
[managed launcher](#launcher-added-89cb6289) (`89cb6289` parity). Every
`-probe-cli` row exits `0`, and each stderr text below ends in one newline:

- A usable launcher runs `<launcher argv...> <path> --version`. If the run exits
  0, stdout and stderr stay empty.
- An unusable launcher prints `__CLI_LAUNCHER__\n`, and the CLI does not run.
  stderr is `claude-ssh: <path> was not run: managed launcher unusable: <reason>`.
- Unreadable settings print `__CLI_LAUNCHER__\n`, and the CLI does not run. stderr
  is `claude-ssh: <path> was not run: managed settings unreadable: <file>: <reason>`.
- A failed launcher run prints `__CLI_LAUNCHER__\n`, not `__CLI_BAD__`. stderr
  holds four lines. The launcher output `L5: to stderr\n` is followed by one more
  newline:

```text
claude-ssh: the managed launcher's run printed:
L5: to stderr

claude-ssh: <path> --version through the managed launcher <argv0> did not succeed: exit status 3
```

- A launcher run that has not ended at 33 s is stopped. stdout is
  `__CLI_HUNG__\n`, and stderr is `claude-ssh: ` and the `cli unresponsive: …`
  text of `-install`. The reference stopped a 150 s launcher at 33.069 s.
- Without the gate the probe runs directly, as before. On Windows the gate shows
  no difference (measured on a Windows VM).
- claustrum's choices (not measured) follow. A none answer runs the probe
  directly. An empty launcher stderr leaves out the `printed:` block. The stderr
  cap of `-install` applies. A signal reads `terminated by SIG<name>`.

The `-help` text of `-probe-cli` names this mode. It is the one `-help` line that
differs from `f6010b97` (measured on Linux, macOS and Windows).

The direct bound is a fixed 30 s, always applied. Measured on `89cb6289` on
Linux, macOS and Windows: a CLI that answers at 28 s passes, and one that answers
at 33 s gives `__CLI_HUNG__` at 30.0 s. The mode unsets `CLAUDE_RPC_TOKEN` so the
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
