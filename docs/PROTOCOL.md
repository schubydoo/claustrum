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
  guard that `daemon.token` carries. See
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
exception, and it is not authenticated at all. A shutdown frame that the daemon
reads stops it, with any `auth` member or none. `-stop` sends no
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
daemon wrote. At startup the daemon reads that identity through an open handle
of the file. At the stop it compares the file at the path to it (`os.SameFile`). A
restart's successor can republish `daemon.token`
before the predecessor leaves. The
departing predecessor then leaves the file alone. It does not delete the
successor's token and break the new daemon's reconnect auth. `5fd08069` keeps that
token on Windows too (cell W3 of round `task091-01`, Windows VM). On Linux and macOS the socket file
carries the same identity guard on the same graceful path. On Windows the
`rpc.pipe` pointer carries it too (`unlinkOwnSocket` /
`dropOwnTokenFile` / `removePipeNameFileIfOwned`). Each removes its file only
when the recorded identity still matches. They differ only in the no-identity
fallback. A socket the daemon never recorded is removed with a plain unlink. A
token or pipe pointer with no recorded identity is left in place, not unlinked.
Two states remove the new `rpc.sock` on `5fd08069` and on claustrum `080c94a`
(round `task091-01`). On Linux ext4 the new socket can get the inode of the old
one. The old daemon then tests it after the bind (cell L3t, Linux VM). On Windows
the old daemon stops after a take-over (cell W3, Windows VM).
This is the departing-daemon half of the daemon-to-daemon handoff. The launcher
half is under [Daemon startup](#daemon-startup-serve). It is off the JSON-RPC
wire.

### Run-dir lock (`daemon.lock`)

Before it binds the socket, a `-serve` daemon takes an exclusive `flock` on
`daemon.lock` in the socket's directory (mode `0600`). It then writes an owner
record into that file. The record is a JSON object. At first it is
`{pid, role, node}`, where `role` is `serve`. After the reap of the start and the
bind, the daemon writes it again as `{pid, role, node, instanceId, startedAt}`.
`node` is omitted when the machine identity is unknown. Build `4534d86` added the
lock, and claustrum matches it. It is a file beside the socket, off the wire. On
graceful shutdown the daemon truncates the record, drops the lock and leaves the
file in place. `daemon.token` is unlinked instead. A
Linux VM measured the same end state against `89cb6289` in the stop rows:
`daemon.token` and `rpc.sock` removed, `daemon.lock` left empty. On a Linux VM the
`instanceId` of the record, of the listening line and of `server.capabilities` held
one value in 36 of 36 daemons of `89cb6289`.

On Linux and macOS the stop closes the listener, tests `rpc.sock` and removes it
if it is its own, closes the connections and kills the child groups. Then it removes `daemon.token` and releases the lock.
`5fd08069` stops in that order (round `task091-01`, cells L1 to L3 and L1k, Linux VM).

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
holder again. The eviction fires only against a holder that is verifiably one of
our own `-serve` daemons for this socket, on this machine (`daemon_runlock_unix.go`).

A holder whose record has role `stop` gets no signal. The newcomer tries the lock
every 50 ms for 2 s. Then it logs this line and serves without the lock:

```
serve: <dir>/daemon.lock is held by a --stop (pid <n>) that has not let go; leaving it
```

`5fd08069` does so in cells L2 and L3 of round `task091-01` (Linux and macOS VMs).

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
  now waits on the lock, as `89cb6289` does.

A process that is no daemon can hold the lock, with no owner record in the file.
The daemon then signals nothing and logs three lines. On a Linux VM (row ST02),
`f6010b97` and `89cb6289` logged the same three texts:

```
[daemon] serve: WARNING <dir>/daemon.lock is held by a live process we will not signal (its holder is not a daemon (role "")); leaving it alone
[daemon] serve: previous owner of <dir>: survivor
[daemon] serve: not the run dir's lock holder; leaving any predecessor's children alone and recording none
```

claustrum logs the third line at every start that ends without the lock, and then
reaps and records nothing. Other cases and role texts are not measured.
Cell H5 (`5fd08069`, Linux VM) measures a held flock.
With such a holder on an empty file, `5fd08069` tries the lock every 30 s, and a
failed try logs nothing (round `task005-01`, Linux and macOS VMs). A good try writes
the full record, holds the lock until the exit and logs this line. claustrum is built
to both:

```
[daemon] run-dir lock acquired late (previous holder gone); recorded pid <n> (instance "<hex>")
```

claustrum also tries after its other refusals (claustrum's own), not after a failed open
or a refused lock call.

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
(rows ST01, ST02, ST04 to ST10, HC16 to HC19, HC23 to HC26). On Linux and macOS
the stranded daemon ends by itself. See [Orphan-exit self-probe](#orphan-exit-self-probe). On a macOS
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
run has three bounds: a command deadline, a wait for the output pipe, and a
last bound that gives up on it. Those bounds are claustrum's own.

The cleaner asks `lsof` two more things on macOS: whether the stdio of an orphan
is three pipes, and whether a process holds the `daemon.lock` of an idle run dir.
claustrum tells these outcomes of an `lsof` run apart. A macOS VM measured the first two against
`89cb6289`, 3 of 3 runs in each row. The rows
are of the macOS host cleaner round of issue 429. The rows where the command
does not start ran under a sandbox rule that denies the exec of `lsof`. Rows E3i
and E4 hold an idle daemon, and rows D1k and E3k hold a daemon with one client:

| `lsof` outcome | busy read of a daemon | lock read of a run dir |
|---|---|---|
| the command does not start | the daemon gets no SIGTERM (rows D1i and D1k, as `89cb6289`) | a dir with a lock file stays (row C3, as `89cb6289`) |
| the run writes to stderr and exits 1 | the daemon gets no SIGTERM (rows E3i, E3k and E4, as `89cb6289`) | a dir with a lock file stays (rows D2p, D2f and E4, as `89cb6289`) |
| the run is given up on | busy, so no SIGTERM ([DIVERGENCES.md](DIVERGENCES.md) D17, not measured) | kept (not measured, claustrum's own) |
| the 15 s deadline ends the run | no SIGTERM (`5fd08069` under a hung `lsof`, cells B1 and B2. claustrum gives the same result, round 8, macOS VM) | kept (cells L1 and L2) |
| the run completes with nothing on stderr | the output decides | the output decides |

The exit code is no rule. `lsof` exits 1 with no text when no process holds the
file, and the dir then goes (rows C1 and C6). In rows
D1i, D1k, E3i and E3k the pass of `89cb6289` and of claustrum logs these two lines
for the daemon, the first one before every run dir line. Its summary counts
`abandoned daemons retired 0` and `candidates left undecided 1`:

```
[hostclean] daemon pid <pid> left alone this pass: its open files could not be inspected
[hostclean] run dir "<dir>" unused for 40 days: its daemon (pid <pid>) was not retired: a connection is attached to it right now, or that could not be read: in use after all
```

In the two control rows `lsof` starts. Both sides retire the idle daemon (row D1ci)
and spare the daemon with a client (row D1ck).

On claustrum the first line goes to a daemon that is a candidate for the retire. In row E2 two
daemons are over 5 minutes old on run dirs that are 40 days old. Each gets the
line, and the summary counts `undecided 2`. In row E1 a second daemon is about 33 s old
on a fresh run dir. It gets no line, and the count stays 1. claustrum asks for
both: an age of 5 minutes and an idle run dir. Both hold on `89cb6289` in two more
rows. In row F1 a daemon about 33 s old is on a run dir that is 40 days old. It
gets no such line on either side, and both sides print
`its daemon (pid <pid>) was not retired: it is too young to judge` for the run dir.
In row F2 a daemon 5 minutes old is on a run dir with a fresh mtime, and no line
names it on either side. From the code: with more than 4096 processes claustrum
prints no such line. That state is not measured.

Row E5 adds an orphaned Claude Code group to the state of row D1i. Neither side
signals the group. Both log this line after the daemon line and before the run
dir lines, and the summary counts `undecided 2`:

```
[hostclean] process group <pgid> left alone this pass: its descriptors could not be inspected
```

With a working `lsof` both sides end the group (row E5c). After an `lsof` run that
writes to stderr and exits 1, both sides print the same line and signal nothing
(row F3). A run that is given up on spares the orphan with claustrum's own line.

The lock read has these measured states (macOS VM against `89cb6289`, 3 of 3 runs
in each row, and claustrum gives the same answer):

| row | state | `89cb6289` and claustrum |
|---|---|---|
| C3 | the `lsof` command does not start, and the dir has a `daemon.lock` (empty, or the record of a dead daemon) | the dir stays, with the line `[hostclean] run dir "<dir>" unused for 40 days: kept, its daemon.lock could not be examined` |
| D2p, D2f | the `lsof` run writes to stderr and exits 1. In rows D2p and D2f a live process holds one of the lock files | the dir stays, with the same line |
| D4 | the `daemon.lock` has mode 0000 | the dir stays, with the same line |
| E7a, E7b, F4 | the `daemon.lock` is a symlink to a regular file, a folder, or a FIFO | the dir stays, with the same line |
| C3, D2p, D2f, D4 | the dir has no `daemon.lock` | the dir goes |
| C2 | another live process holds the lock file open | the dir stays, with the held line |
| C4 | only the cleaner itself holds the lock file open | the dir goes |
| D3 | the cleaner and a child of the cleaner hold the lock file open | the dir stays, with the held line |
| C5 | the lock file holds 7 bytes that are no record, and no process holds it | the dir goes |
| D7a, D7b | the lock file holds `{}`, or the first half of a record | the dir goes |
| D5 | the lock holds the record of a live pid that does not hold the file | the dir goes |
| C6 | the lock holds the record of a dead pid, and no process holds it | the dir goes |

Not measured: a second pass, and an `lsof` run that writes to stderr with another
exit code or with output on stdout too. claustrum reads each such run as no
answer, whatever the exit code and the output are. Linux is not
measured for these states. On Linux, a lock that cannot be opened, a lock that is
no regular file, and lock content that is no record keep the dir with claustrum's
own line. On Linux the pass logs no `left alone this pass` line.

### Daemon startup (`-serve`)

This section holds what the launcher and the daemon do at a `-serve` start. The
flags are under [-serve, run the daemon](#-serve-run-the-daemon). The launcher
wait and the self-locate are measured against `5fd08069`, and the other rules
against `89cb6289`, on the systems that each rule names.

The launcher is the `-serve` process that the operator starts. It re-execs its own
binary as the detached daemon and waits for it. On Linux a launcher that cannot
read `/proc/self/exe` prints `claustrum: locate self: <error>` and exits `1` before
any write. macOS and Windows re-exec `os.Args[0]` instead (not measured). The
launcher creates a missing socket folder with mode `0700`. It sends the stdout and
stderr of the daemon to the [daemon log](#daemon-log-remote-serverlog).

##### The launcher wait

The launcher connects to the socket path every 50 ms, for 12 s at most. It closes
each connection at once, so the log of the daemon that answers holds a
`New connection from: @` / `Connection closed: @` pair. A socket file that refuses
the connect does not end the wait. A killed daemon leaves such a file. A folder at
the path does not end the wait either.

- The predecessor is the daemon that answered on the path before the start. On
  Linux and macOS, a daemon that answers and is not the predecessor ends the wait
  with exit `0`.
- On Windows the first connect that works ends the wait, also one that the
  predecessor answers.
- At 12 s the launcher gives up with exit `1` and prints one of two lines to
  stderr. A connect that reached the predecessor gives
  `claustrum: daemon did not take over <socket> (predecessor still owns it)`. Any
  other wait gives `claustrum: timeout waiting for daemon to accept on <socket>`.

After exit `0` the socket accepts connections. The launcher prints nothing on
success. The daemon prints its ready line
`Claustrum remote server listening on <socket> (pid <pid>, instance <32-hex>)` on
its own stdout, so the line goes to the daemon log.

A restart on a live socket unlinks the old socket and binds a new one. This is off
the JSON-RPC wire. The departing daemon's matching half is the inode-ownership
unlink under [Token persistence](#token-persistence-daemontoken).

##### The bind after the reap (Linux and macOS)

The daemon takes these steps in order. It claims `daemon.lock` and reaps the
recorded children of a dead daemon. Then it removes the old socket file, binds,
and completes the owner record of the lock. So nothing listens on the path while
the reap waits for a group to end. A client connect in that wait fails. With the
socket file of the dead daemon there, it gets `ECONNREFUSED`. With no socket file,
it gets `ENOENT`. A recorded child
that ignores `SIGTERM` makes the wait about 2 s. See
[Child records and the reap](#child-records-and-the-reap-linux-and-macos) for the
reap itself.

##### The job of the launcher (Windows)

On Windows the launcher starts the daemon outside the job of the launcher
(`CREATE_BREAKAWAY_FROM_JOB`). So the daemon stays alive after an SSH session whose
job ends its processes. How the reference leaves the job is not measured. A job
that refuses breakaway refuses that start. The launcher then starts the daemon
inside the job and prints one line on its stderr. `89cb6289` prints this text after
a time stamp, and claustrum puts its level tag before it:

```
[daemon] detached spawn failed (fork/exec <binary>: Access is denied.); retrying without breakaway
```

#### Child records and the reap (Linux and macOS)

A daemon that holds the [run-dir lock](#run-dir-lock-daemonlock) records each [process.spawn](protocol/process-spawn.md) child in `<run dir>/children/<pid>.json`, on every socket shape. The run dir is the folder of the socket path. The child needs a pid of 2 or more and a start time that the daemon can read. Windows writes no record and reaps none. Unless a rule says otherwise, the rules below are measured against `89cb6289` on Linux, and a rule that names macOS on macOS.

The record is one JSON object with the members in this order:

```json
{"pid":<int>,"node":"<boot-id>/pid:[<inode>]","host":"machine-id:<hex>","instance":"<daemon instance id>","daemonPid":<int>,"daemonStart":"<start>","argv0":"<child argv0>","start":"<start>","at":<epoch-ms>}
```

`daemonStart` and `start` are strings. They hold clock ticks on Linux and the date text of `CLAUDE_SSH_CHILD` on macOS. On macOS the node is the boot-session UUID, and the host is the hostname. With a `launcher`, `argv0` is the launcher, and a `program` member after `argv0` holds the command. claustrum writes no `program` without a launcher (not measured on the reference).

- The daemon writes a record through a temp file `children/.rec-<pid>.json.<12 characters>` and a rename. The source of the 12 characters is claustrum's own.
- The end of the child removes its record, before the pipe drain.
- A graceful shutdown removes the records of the children that it ends. claustrum waits 1 s at most for them (claustrum's own). With `-keep-children` it removes the records of the children that stay alive, so the next daemon leaves them alone. That rule is claustrum's own, as the flag is.
- A record stays after a kill of the daemon, and for a process whose `id` a later spawn took.
- After a rename of the run dir, a record goes under the new name. The daemon holds the run dir open.
- No record goes through a `children` that is not a real folder inside the run dir. The remove at the end of the child is the one exception: it follows a symbolic link to a folder inside the run dir.

At a `-serve` start, after the run-dir lock and before the bind, the daemon reads the records:

- It walks `children` in directory order and handles the first 128 names that end in `.json`. The rest wait for the next start. Kept records count toward the 128. So 128 records of another machine, which stay for 30 days, stop every reap, and an orphan behind them stays alive.
- It sends `SIGTERM` to 64 groups at most. The others keep their records for the next start.
- Only a regular file is a record. A link, a FIFO or an empty folder with a `.json` name is removed, with no signal. The target of a link stays. A folder with content stays (from the code).
- It reads at most 4096 bytes of a record. A record whose first 4096 bytes are not one JSON value is removed, with no signal.
- If `children` is not a real folder, it reads, signals and removes nothing. That is measured for a symbolic link to a folder. The other kinds are claustrum's own.

A record of a daemon that is alive stays. So does a record from another machine, for 30 days (from the code). A record of a dead pid is removed. So is a record from an earlier boot of this machine (from the code). For a record of a dead daemon on this machine, the daemon tests the process. If it still is the recorded child, its group gets `SIGTERM`. Else the record goes with no signal. The test has four parts:

- The process started at the recorded `start`, so the pid is not reused. On macOS a `start` with two blanks before a one-digit day does not match.
- It leads its own process group.
- It runs `argv0`. With a `program` member, it can also run `program`, or hold `program` as one whole argument. On Linux the program is the first argument of `/proc/<pid>/cmdline`, and a name with the same last path component matches. On macOS the daemon reads the command text of `ps`, which cuts words at blanks. So a `program` with a blank never matches an argument there.
- `CLAUDE_SSH_RUN_DIR` names this run dir, and `CLAUDE_SSH_CHILD` names the pid and start of the process itself.

A process that the daemon cannot inspect keeps its record and gets no signal (`5fd08069`, from the code). The daemon does not test the owner of a record file.

On macOS a `daemonStart` with two blanks before a one-digit day does not match the live daemon either. An older claustrum build wrote such a text on days 1 to 9 of a month. A new daemon on macOS reads such a daemon as gone, although it is alive. It ends a recorded child whose own `start` matches, such as one that started on day 10 or later. A child that started on days 1 to 9 has two blanks in its `start` too, so its record is dropped with no signal.

After the `SIGTERM` the daemon polls for 2 s. At each poll a group takes the whole test again. A group that fails it gets nothing more. A group that still passes after 2 s gets `SIGKILL`. After the last group is done, the records of all signalled groups go together. A leader that reads as gone is read again after 50 ms before the `SIGKILL` of its group ([D20](DIVERGENCES.md#d20)). A group that still cannot be inspected at the end, or that outlives the `SIGKILL`, keeps its record. The [log lines](#lines-of-child-processes) of the reap are under the daemon log.

### Idle-connection close

A `-serve` daemon closes any accepted connection that goes 5 minutes with no read
or write activity. This matches `f6010b97`, as measured. The timeout is fixed and
always-on. There is no flag and no configuration key to change it or to disable
it. The daemon stamps the last activity time on every read and write of the
connection. A per-connection watcher polls for silence, at a quarter of the
timeout, bounded to at most 30 s. Once the idle span reaches the timeout, the
watcher closes the socket and logs
`[Server] closing connection idle <d> in both directions: <addr>`. On a Linux VM
(rows HC12a and HC12b), `89cb6289` logged that text with `5m0s` and `@`. `5fd08069`
closes at the same times as claustrum (cells I1 to I4 of round `task034-01`, Linux
and macOS VMs). The watcher stops as
soon as the connection closes for any other reason, and as soon as the daemon
shuts down. It therefore never outlives its connection. This is off the JSON-RPC wire, because it is connection lifecycle and
sends no frame. It closes only an idle *connection*, never the daemon. On Linux
and macOS a client-less orphan daemon is retired by the separate orphan-exit
self-probe below.

### Daemon log (`remote-server.log`)

The launcher creates `remote-server.log` in the socket's directory with mode
`0600`. The launcher rotates any existing log to
`remote-server.log.old` and creates a fresh one. A log that cannot be renamed gets
no fresh file (see below). This matches `4534d86`, which
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

A start with `CLAUDE_SSH_PEER_CHECK=1` writes one more line before the banner.
On Linux it is at INFO level and names the namespaces of the daemon:

```
[Server] serving only callers in "user:[4026531837] net:1 pidfd"
```

`5fd08069` writes it after its open-files line (round `task011-01`, Linux VM).
On macOS and Windows the line is at WARN level, and `5fd08069` writes this text
last before its banner (macOS and Windows VMs):

```
[Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all
```

A Linux kernel that names no namespace gets that line too (claustrum's own, not
measured). The place of the line beside the lines of a reap is not measured.

The banner ends with the pid of the daemon and its instance. The instance is the
`instanceId` of `server.capabilities`. `f6010b97` and `89cb6289` print the same
suffix (Linux, macOS and Windows VMs). The name `Claustrum` is claustrum's own.
The reference prints `Claude` there. The banner beside the lines of
`-listen-pipe` and `-metrics-addr` is not measured.

If the rename fails, claustrum's launcher judges the entry at the path by `Lstat`
and opens no FIFO. An own regular file is truncated, keeps its mode and gets the
lines. So does the own regular file behind an own link. A folder fails the start.
The launcher then prints one line on stderr and exits 1, with no daemon, socket,
`daemon.lock` or `daemon.token`:

```
claustrum: open log: open <path>: is a directory
```

A create that fails with nothing at the path fails the start the same way, with the
error of the create. `5fd08069` does all four, with its own prefix. Rounds
`task015-03` and `task015-04` show cells E1 and E3 on the Linux and macOS VMs, and E5
and E6 on the Linux VM. Every other
entry fails the start with `permission denied` and stays unchanged. That is
[D8](DIVERGENCES.md#d8). On Windows the rename moves a folder onto a regular `.old`,
as on `5fd08069` (cell W1 of round `task015-04`, Windows VM).

On Windows a second daemon can start on a live socket. The first daemon holds
`remote-server.log` open, so the launcher cannot rotate it. The launcher then
truncates that file, and the second daemon logs there. Each daemon writes at the
offset of its own handle. So the earlier lines of the first daemon are lost, and
its later lines sit behind a block of NUL bytes. A log that another process holds
with no delete share is truncated too, and `.old` stays. `5fd08069` does both
(cells W1 and W3 of round `task015-01`, Windows VM).

claustrum does not remove the log on graceful shutdown. This differs from the
socket and from `daemon.token`. The log outlives the daemon, so a post-mortem
stays readable. The fixed name and location are the deployment contract, and they
are not configurable.

#### Lines of child processes

These lines tell of [process.spawn](protocol/process-spawn.md) children and of their [records and the reap](#child-records-and-the-reap-linux-and-macos). claustrum prints a level tag before each line. The reference prints none. The levels are claustrum's own, and `CLAUSTRUM_LOG_LEVEL=warn` drops the INFO lines. Unless a row says otherwise, each text is the text of `89cb6289`, measured on Linux. The group-ended, outlived and dropped-record lines are measured on macOS too.

| Event | Level | Line |
|---|---|---|
| A start | INFO | `[process.Manager] Process <id> started, PID=<n>, command=<command>` (from the code). With a launcher, ` via launcher <argv0>` follows. |
| A failed start | ERROR | `[process.Manager] Failed to start process <id>: <message>`, with the text of the error frame |
| An exit | INFO | `[process.Manager] Process <id> exited with code <n>`, then `, terminated by <signal>` for a signal and `, signalled at client request` or `, signalled at shutdown request` for a kill by the daemon |
| The drain limit | WARN | `[process.Manager] Process <id>: pipe drain grace expired (grandchild holding stdio?); force-closing` |
| A supersede | INFO | `[process.Manager] process <new id> supersedes <old id> for session <key>: died=<bool> escalated=<bool> alreadyExited=<bool>` (from the code), after the exit line of the old process |
| `children` not a folder | ERROR | `[process.Registry] <run dir>/children is not a directory; recording nothing`, or `reaping nothing` at a start |
| A record write fails | ERROR | `[process.Registry] cannot record child <pid>: <error>` |
| A record remove fails | WARN | `[process.Registry] cannot remove record of child <pid>: <error>` |
| Over 128 records | WARN | `[process.Registry] more than 128 records in <run dir>/children; leaving the rest for the next start` |
| Over 64 groups | WARN | `[process.Registry] <n> more orphaned group(s) recorded in <run dir>/children than the 64 one start ends; leaving those for the next start` |
| A record dropped | INFO | `[process.Registry] record <pid>.json: pid <pid> is not a child to end (<reason>); dropping the record, signalling nothing` |
| A record kept | INFO | `[process.Registry] record <pid>.json: pid <pid> cannot be inspected now (<reason>); keeping the record, signalling nothing` (`5fd08069`, Linux VM) |
| A group ended | INFO | `[process.Registry] ending orphaned process group <pgid> recorded by daemon instance "<instance>" (pid <daemon pid>): SIGTERM` |
| The grace ran out | WARN | `[process.Registry] group <pgid> outlived SIGTERM for 2s: SIGKILL` |
| A group fails the test | INFO | `[process.Registry] group <pgid> no longer verifies (<reason>); nothing more is sent to it` |
| The reap summary | INFO | `[daemon] serve: predecessor's children — <n> orphaned group(s): <a> ended on SIGTERM, <b> on SIGKILL, <c> survived; <d> stale record(s) dropped, <e> kept` |

The exit line is a race at a shutdown, on the reference and on claustrum. It can be missing. A reap that sent no `SIGTERM` and kept no record writes no summary.

The main `<reason>` texts of a dropped record and of a group that fails the test are these:

- `it does not run the recorded program "<argv0>"`, with `and neither runs nor was handed "<program>"` after it for a record with `program`
- `it does not lead its own process group`
- `it did not start at the recorded "<start>": the pid has been reused`
- `it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it`
- `it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one`
- `it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)`
- `environment unreadable: <error>`, and on `5fd08069` `pid namespace unreadable: <error>` and `stat unreadable: <error>`
- `the record names no program to verify against`
- claustrum's own: `pid is out of range`, `pid is this daemon`, `pid is this daemon's parent or process group` and `record has no start time to verify against`

### Orphan-exit self-probe

On Linux and macOS a `-serve` daemon whose socket path no longer leads back to it
shuts down, as on 4534d86. The
idle timeout closes only a *connection*. The probe is off the JSON-RPC wire. Its
effects are log lines and, once orphaned, a self-directed authed
`server.capabilities` request.

Every 60 seconds the daemon compares the file identity of its socket path
(`os.SameFile`) to the inode it bound. If the path is gone or another inode, a
10-minute grace clock starts. A path that still matches resets the clock. A check
that sees a client starts the clock again and writes no line. So the first line
below does not come after that client leaves, as on `5fd08069` (cell K2 of round
tier1-01, Linux and macOS VMs). After the grace with nobody connected, the daemon
dials its socket and compares the `instanceId` of the reply to its own. A probe
that reaches itself resets the clock. Two failed probes, 60 seconds apart, start a
graceful shutdown. It closes listeners, drops clients, and stops child process
groups unless `-keep-children` is set.

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

A check with no client also ends a daemon after 30 days with no counted request,
on the monotonic clock. Each request past auth counts, except
`server.capabilities`. This idle exit is not measured on a reference build.

The checks run on Linux and macOS only. On Windows a daemon whose socket file was
deleted writes no line. `5fd08069` wrote no line and was still alive after 14 min
(cell K3 of round tier1-01, Windows VM).

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
| `-32006` | `Refused: this server could not confirm that the caller is in its own namespaces`, on Linux (see server.*) |
| `-32001` | `Unauthorized: invalid or missing auth token` |

### Error-string catalogue

Every method-level error string, verbatim, in one place. The per-method sections
below give the trigger and the result shape. Codes are `-32602` unless noted. A
cell C-… is of round `tier0-20` against `5fd08069` on a Linux VM.

| namespace / context | error string | code / notes |
|---|---|---|
| protocol | `Invalid JSON-RPC version` | -32600 |
| protocol | `Invalid method format: <m>` / `Unknown namespace: <ns>` / `Unknown method: <ns>.<m>` | -32601 |
| protocol | `Invalid params` | -32602 (absent/mistyped `params`) |
| protocol | `Unauthorized: invalid or missing auth token` | -32001 |
| protocol | `recovered panic: <v>` | -32603 (claustrum-only, see below) |
| files.stat | `stat <path>: <reason>` | -32603 (any stat failure other than ENOENT) |
| files.read | `open <path>: <reason>` | -32603 (any open failure other than ENOENT) |
| files.read | `files.read: path is a directory` | |
| files.read | `files.read: file exceeds maxBytes` | |
| files.read | `files.read: not a regular file` | -32602. The path opens and is not a regular file. See [files.read](protocol/files-read.md) |
| files.list | `open …: no such file or directory` | -32603 (missing dir) |
| files.list | `open <p>: not a directory` | -32603. The path is not a directory. Since `7d193f89` the open itself fails. Before `7d193f89` the message was `readdirent …` |
| files.list | `open <p>: permission denied` | -32603 (an unreadable directory) |
| files.validate | `Path does not exist` | in `error` field, `valid:false` |
| files.extract_tar | `archivePath and destDir are required` | |
| files.extract_tar | `destDir must be an absolute, non-root path: …` | in `error` field |
| files.extract_tar | `destDir must not be or contain the home directory: …` | D2, in `error` field |
| files.extract_tar | `open archive: …` | in `error` field (the archive cannot be opened) |
| files.extract_tar | `gzip: …` | in `error` field (a bad gzip header) |
| files.extract_tar | `tar read: …` | in `error` field (an entry that the tar reader cannot read) |
| files.extract_tar | `write <entry>: …` | in `error` field (the content of a file cannot be copied) |
| files.extract_tar | `unsafe path in archive: <entry>` | in `error` field (zip slip) |
| files.extract_tar | `unsupported tar entry type <c>: <entry>` | in `error` field |
| files.extract_tar | `extraction size limit exceeded` | D3 opt-in, in `error` field |
| files.extract_tar | `clean destDir: RemoveAll <name>: …` | in `error` field. The daemon cannot delete the old `destDir` or an entry in it. `<name>` is the last name of `destDir` |
| files.extract_tar | `mkdir parent: mkdir <path>: …` | in `error` field. The daemon cannot create a folder above `destDir`. `<path>` is the whole path of that folder. The text `mkdir parent <entry>: …` below is for an archive entry |
| files.extract_tar | `mkdir destDir: mkdirat <name>: …` | in `error` field. The daemon cannot create `destDir` |
| files.extract_tar | `clean destDir: "<name>" is not the entry that the path names` / `mkdir destDir: "<name>" is not …` | in `error` field. claustrum's own text: the path of `destDir` and its last name in the parent folder do not name one entry. See [D2](DIVERGENCES.md#d2) |
| files.extract_tar | `write .synced: …` | in `error` field |
| files.extract_tar | `open parent: open <parent>: …` | in `error` field. The daemon cannot open the folder that holds `destDir` |
| files.extract_tar | `open destDir: openat <name>: …` | in `error` field. The daemon cannot open the new `destDir` |
| files.extract_tar | `open destDir: "<name>" could not be examined (statat .: permission denied)` | in `error` field. The daemon cannot search the new `destDir`. See [files.extract_tar](protocol/files-extract-tar.md) |
| files.extract_tar | `open destDir: "<name>" changed while it was opened` | in `error` field. claustrum's own text: another process replaced `destDir`. See [D2](DIVERGENCES.md#d2) |
| files.extract_tar | `create <entry>: openat <place>: file exists` | in `error` field. A directory is at the place of a file entry. On Windows the text ends with `is a directory`. `<place>` is relative to `destDir` |
| files.extract_tar | `mkdir parent <entry>: mkdirat <place>: file exists` | in `error` field. A file is at the place of the directory of a file entry (prefix is contract) |
| files.extract_tar | `mkdir <entry>: mkdirat <place>: file exists` | in `error` field. A file is at the place of a directory entry |
| git.status | `baseRepo is required` | -32602. `baseRepo` is now required, since `7d193f89` |
| git.status / git.list_branches | `<go error>`, for example `exit status 128` | -32603 (git failed, stdout parse) |
| git.list_branches / git.info / git.status / git.worktree_create | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | -32603. Create: after the text of the stopped step, as `git worktree add failed: ` or the `mkdir_failed` text of the parent step. Alone, with `worktree_add_failed`, for a stopped repository test |
| the four methods | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again` | -32603. Create: `worktree_add_failed`, after `git worktree add failed (checkout): ` for the checkout listing |
| the four methods | `this repository's git config cannot be used: git did not finish reading "<path>" within 30 seconds; try again` | -32603. Create: `worktree_add_failed` |
| git.worktree_create | `git worktree add failed (completing the new worktree's git files): this repository's git config cannot be used: git did not finish reading it within 30 seconds; try again` | in `error`, `worktree_add_failed`. The listing of `config.worktree` after the add ran 30 s, and the rollback of a failed checkout runs (cell TW) |
| git.status | `<file>: not a regular file` | -32603. claustrum's own text, from the copy into the temporary git folder after the gate passed. `<file>` is `info/sparse-checkout` when that entry is a folder or a FIFO. The reference is not measured there |
| git.status | `<file> is larger than <n> bytes` | -32603. claustrum's own text, for a copied file over its bound, also one that grew after the gate. `<n>` is 1073741824 for `index`, a `sharedindex.<40 or 64 hex>` file, `info/sparse-checkout` and a reftable file, and 1048576 for `HEAD` and `config.worktree`. The reference is not measured there |
| git.status | `<os error>` of the copy or of the temporary folder, for example `openat index: permission denied` or `stat <dir>: no such file or directory` | -32603. The raw Go error when a file of the entry cannot be copied, as an `index` of mode 000, or when a file of the temporary git folder cannot be made. With `"shadowDirOff":true`, and on a server with no run folder, a temporary folder that cannot be made answers the raw Go error too. The reference is not measured there. In the states `shadow` and `fallback-git` a `.gitdir-<n>` that cannot be made answers `{"isRepo":false,"clean":false}`: see [git.status → Errors](protocol/git-status.md#errors) |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "<value>" is not a count` | in the frame of each method, when the daemon's own `GIT_CONFIG_COUNT` does not parse. See "The daemon's own git environment" |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair <n> is incomplete` | in the frame of each method, when a pair below the daemon's `GIT_CONFIG_COUNT` is not set. See "The daemon's own git environment" |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: <git text>` | in the frame of each method, for a `GIT_CONFIG_KEY_<n>` of the daemon that git cannot read. A folder that holds no repository gets it too. `89cb6289` answers so in rows A-N1 to A-N6, A-N5b, A-S1 and A-X2 on Linux and macOS VMs, except row A-N5 on macOS. On a Windows VM the cells are P3-infoN, P3-infoX, P3-lbN, P3-statN, P3-crN, P3-rmN and Wf-P3-crNroot. See "The daemon's own git environment" |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` | Windows only. In the frame of `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`. The `path` of the first two, or the `baseRepo` of the last two, exists and git cannot start in it. That is a regular file, a file symlink (measured for `git.info` only) or a folder with a path of 296 characters (measured for `git.info` only). `89cb6289` answers so on a Windows VM: in cells A-01 to A-04, A-04b2 and A-06 for a regular file, in cell A-10 for the symlink and in cell A-13 for the folder. The path `NUL` and a file name with one more dot at its end get it too (measured for `git.info` only, cells Wc). See "Git-directory trust check" |
| git.* | `git cannot run on this host; git not run: <exec error>: <git text>` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`), when the configuration listing fails and `git version` fails too. The exec error and the text are those of `git version`. Row A-T1 and cells C-d and C-g show that on Linux and macOS VMs, and cell C-h on a Linux VM. Cells P11-infoN, P11-infoT, A-14, Wa-infoF, Wa-lbN, Wa-statN and Wa-crN show it on a Windows VM. See "Git-directory trust check" (`89cb6289`). On `5fd08069` also after a failed call 7 of [the start test](#the-start-test) and see [the call about the repository](#the-call-about-the-repository) |
| git.* | `this repository's git config cannot be used: <detail>` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. Since `5fd08069`. The details are in [Repository config refusals](#repository-config-refusals) |
| git.* | `a file in this repository's .git directory cannot be used: <detail>` | The same frames. See [Repository config refusals](#repository-config-refusals) |
| git.* | `Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. Since `5fd08069`. See [The settings copy](#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>` | The same frames. The daemon cannot make the folder of the copy |
| git.* | `Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again` | The same frames. See [The settings copy](#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again` | The same frames. Measured on `git.status` only. From the code: a checkout puts it after `git worktree add failed (checkout): `. See [The settings copy](#the-settings-copy) |
| git.* | `git could not check this repository on the remote machine: at "<root>": exit status <n>` | The same frames, for a failed call about the repository. See [The call about the repository](#the-call-about-the-repository) |
| git.* | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` | The same frames, for a call about the repository that the daemon ended |
| git.* | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again` | The same frames, for a call about the repository that names another path |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`), for a configuration key over 1048576 bytes. `89cb6289` measured `git.info`. See "Hardened git calls" |
| git.* | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` / `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`). It answers more than 1024 hook names, or names over 65536 bytes in sum. `89cb6289` measured `git.info`. See "Hardened git calls" |
| git.* | `the repository's git directory could not be trusted; git not run: commondir not as git writes it: <S1, S2 or S3>` | in the frame of each method (see the table in "Git-directory trust check" for `git.worktree_remove`), for a stray `commondir`. "Git-directory trust check" gives the three texts (`89cb6289`) |
| git.worktree_create / git.worktree_remove | `baseRepo is inside a managed worktrees directory (beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees marker), or could not be validated as a trust root` | in `error`. Create adds `errorCode:"nested_base_repo"`, and remove has none. It answers a `baseRepo` under a managed worktrees tree, or one that fails claustrum's own trust-root test. No git runs. See [git.worktree_create](protocol/git-worktree-create.md) and [git.worktree_remove](protocol/git-worktree-remove.md#where-the-paths-can-be) |
| git.worktree_create | `branchName is required` | |
| git.worktree_create | `not a git repository` | in `error`, `errorCode:"not_a_repo"` |
| git.worktree_create | `refusing to create worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; … / already exists, …}` | in `error`, `errorCode:"unsafe_path"` (`7d193f89` containment). The spelling refusal is Windows-only and comes before containment. On Linux and macOS, in the `<p>` of the `already exists` text the parent folder of the leaf has its symlinks resolved, the last name is kept, and the path is cleaned. `89cb6289` and claustrum name the path so on Linux and macOS VMs: with a symlink at `baseRepo` (cell T9), a leaf that is a symlink or a file (cells U1a to U1d), a path that is not clean (cells U2a to U2c) and a `worktreeRoot` behind a symlink (cell U3a2). `<repo>` is `baseRepo` as sent, so an absent `baseRepo` gives the empty string (`f6010b97`, Linux and macOS VMs). With `worktreeRoot`, the first two texts also refuse a relative, absent or `..` `baseRepo`. `<p>` is then `baseRepo` as sent (`f6010b97`, Linux and macOS VMs) |
| git.worktree_create | `refusing to create worktree: <root> is a filesystem root; choose the worktree location by its absolute path, without "..", beneath the filesystem root` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`. A `worktreeRoot` of `/` is refused. `89cb6289` sends this frame for a `worktreePath` of `/<name>`, after the excludes read and the repo test, 3 git calls. The frame is the same with the folder present or gone, and for a daemon that runs as root. Nothing is created (Linux VM, cells R1n, R1u and R1r, and macOS VM, cell R1a). A root of `//` or `/.` gets the same text, and `<root>` is `worktreeRoot` as sent (cells R2 and R3). A `worktreePath` of `/<directory>/<name>` under a root of `/` gets it too (cell R6). A `worktreeRoot` that is a mount point other than `/` passes: the create and the remove succeed (cell R4). With a root of `/tmp`, a `worktreePath` of `/x1/x2` gets the two-level text (cell R5). `89cb6289` and claustrum sent the same frames in cells R2 to R6 on Linux and macOS VMs. A relative `worktreePath` gets its own text first (cell C-W1). A root with a `..` component that cleans to `/` gets the `..` text (from the code, not measured). Windows refuses every `worktreeRoot` first, so a drive root such as `C:\` is not measured |
| git.worktree_create | `refusing to create worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, `errorCode:"symlinked_component"`, for a symlinked ancestor component under the repo (`7d193f89`) |
| git.worktree_create | `failed to create parent directory: "" does not name a directory` | in `error`, `errorCode:"mkdir_failed"` (empty `worktreePath`, without `worktreeRoot`) |
| git.worktree_create | `failed to create parent directory: <repo>\<component> is not a directory` | in `error`, `errorCode:"mkdir_failed"`, Windows, when a directory between `baseRepo` and the leaf is a junction. `<repo>\<component>` is the path of the first junction. Nothing is created: no directory, no entry, no branch. Measured for `.claude` and `.claude\worktrees` (`f6010b97`, Windows VM, rows JCR1 and JCR2). Measured for a junction `<P>\J` above them, with the paths `<P>\J\sub\.claude\worktrees\w1` and `<P>\J\.claude\worktrees\w1`: `89cb6289` and the claustrum build of this change answer equal frames (Windows VM, cells J-b-wt-pj and J-b-wt-pJ). claustrum also refuses a non-symlink reparse point that is not a junction the same way (not measured) |
| git.worktree_create | `refusing to create worktree: <root> is the repository <repo> or inside it; a worktree location must be outside the repository` | in `error`, `errorCode:"unsafe_path"`, with `worktreeRoot`. A root that is `baseRepo` or lies beneath it by whole components is refused. `<B>/Tx` beside `<B>/T` passes. `<root>` is `worktreeRoot` as sent, and `<repo>` is `baseRepo` as sent. claustrum cleans both paths first, as on remove (not measured on create). It comes after the repo test and before the root-chain tests. claustrum runs it after the two-level test, as on remove (not measured on create). Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_create | `refusing to create worktree: <root> passes through <dir>, which is writable by <who> without the sticky bit (mode <mode>), so they could replace what is beneath it; choose a location under directories only you (or the system) control, or remove the extra write permission (chmod go-w)` / `refusing to create worktree: <root> passes through <dir>, which is owned by uid <uid>, neither you nor the system; choose a location you reach through your own directories` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`. This is the ancestor test. It judges each directory above the root, top first. The root itself is not judged. The daemon resolves the symlinks of each level, then judges each directory from `/` down to that resolved level. Last it resolves the root and judges each directory above the resolved root. So when the root is a symlink, the chain above its target is judged. `<root>` stays the symlink as sent (row G42, Linux and macOS VMs, `89cb6289` only). A missing level or a failed resolve ends the walk with no refusal. The first failing directory reached is named. When the failing directories lie on one chain, that is the highest one. The kind of failure plays no part. `<dir>` is that directory with its symlinks resolved, and `<root>` is `worktreeRoot` as sent. The owner test comes first. uid 0 and the daemon's user pass. Any other owner gives the second text, even with the sticky bit set. `<uid>` prints unsigned, for example 4294967294 on macOS. Then the sticky bit clears the write tests. The group-write bit refuses unless the group is the private group of the daemon's user. The four tests of the private group are those of the root's own write test (`rootGroupIsPrivate`). The other-write bit always refuses. An owner of uid 0 gets no exemption from the write tests. `<who>` is "its group", "every user on this host" or "its group and every user on this host", as for the root. `<mode>` is the permission bits as four octal digits, so a mode of 2775 prints `0775`. Only the chain of the root is walked, not the chain of `baseRepo`. A create without `worktreeRoot` and `git.worktree_remove` run no ancestor test. It comes after the test above and the 9 git calls (see "Hardened git calls"). After its owner and mode, each directory gets the checkout tests and the `.git` test below. The first failing directory from the top decides (cells C-W15 and C-W21). `5fd08069` refuses a link above the root that another user owns (cell C-W16), and answers "not a directory" for a regular file (cell C-W14). The order of these tests inside one directory is not measured. So a failing directory above the root gets this text even when its owner has no search access to it (row G36a). The `lstat` text does not apply there. Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs (rows K1, G1 to G41b, Y11d, T7 and T7c). In macOS rows G28 and Y11d the root is a firmlink spelling, and `<dir>` is `/System/Volumes/Data`. Only Linux VMs measured rows G1, G32, G33a, G33c, G34b and G36b. A macOS VM measured the other round 2 rows and G42 against `89cb6289` only. Other cases are claustrum's choice and are not measured. The comment of `worktreeRootAncestorRefusal` lists them. The walk also tests the folders above a link in the root (cells X1, X2 and M3 of round `tier0-22`, Linux and macOS VMs). |
| git.worktree_create | `refusing to create worktree: <root> leads into the repository <repo> (at <at>); a worktree location must be outside the repository` | in `error`, `errorCode:"unsafe_path"`, with `worktreeRoot`. The root passes the in-repo test and the ancestor test above but leads into the git top level of `baseRepo`, the main checkout or the git directory, also a separate one (cell C-W7). The remove row below gives the test. claustrum compares file identity as on remove (not measured on create). In macOS row Y11d and Linux row T7 the root also leads into the repository, and both references send the ancestor text. `<repo>` is `baseRepo` as sent. It comes after 9 git calls (see "Hardened git calls"). Nothing is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_create | `refusing to create worktree: <root> leads into <wt>, a worktree of the repository <repo> (at <wt>); a worktree location must be outside the repository's checkouts` | in `error`, `errorCode:"unsafe_path"`, with `worktreeRoot`. The root passes the tests above but lies in a linked worktree `<wt>` of the repository. The remove row below gives the test. `<repo>` is `baseRepo` as sent. A listed worktree whose folder is gone refuses too. The create makes that folder first, with mode 0700 (cell C-W8). Nothing else is created. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_create | `refusing to create worktree: <root> is inside a git checkout (<dir> has a .git entry); a worktree location must be outside every checkout, so that no session working in one can reach it — choose a directory that is not part of any repository` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`, when a directory from `/` down to the root holds a `.git` entry of any kind. A missing directory is skipped. The ancestor test answers first at the same directory (rows G18, G40a and G40b). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs, with a `.git` entry up to five levels above the root. `<root>` is `worktreeRoot` as sent, with a trailing slash or `//` kept. `<dir>` is that directory, resolved and cleaned. When several directories hold a `.git` entry, the highest one is named. Linux and macOS VMs measured these spellings and that choice |
| git.worktree_create | `refusing to create worktree: <dir> is itself a git checkout (it has a .git entry); a worktree location must be outside every checkout` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`, when the `<directory>` level holds a `.git` entry. Nothing is created. Measured against `f6010b97` on Linux and macOS VMs, also under a symlinked root, where `<dir>` has the root resolved |
| git.worktree_create | `refusing to create worktree: <root> passes through too many symbolic links (a loop?)` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, with `worktreeRoot`. A symlink loop or too many links lie in the path of the root. claustrum uses the kernel limit and tests this before the ancestor test (neither measured). `5fd08069` sends it for a chain of 45 links, and the remove sends it too (cells C-W13, C-S5 and C-W23). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. `<root>` is `worktreeRoot` without a trailing slash. claustrum also drops a `//` here (not measured) |
| git.worktree_create | `refusing to create worktree: <p> carries a .git file that does not name this repository's own worktree admin directory` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, after a successful add. `<p>` is spelled as in the `already exists` text (`89cb6289` and claustrum, cells T1, T5, U2f and A12c g, Linux and macOS VMs). The `gitdir:` path in the `.git` file of the new worktree lies in a folder that is not named `worktrees`. Or `baseRepo` has a `worktrees` folder with no entry of that name (`89cb6289` in cell P-p, Linux and macOS VMs). Or that entry is a symbolic link (`5fd08069`, cell V7 of round `tier0-10`, Linux VM). Or the `commondir` file of the registration does not name the git directory. `5fd08069` answers it for an absolute path that reaches that directory through a symbolic link (cell V6 of round `tier0-10`, Linux VM). claustrum compares by text. Or the `commondir` file or the `gitdir` record of the registration cannot be read as a file (`89cb6289`, cells P-g to P-j, P-n, P-o and P-q, Linux and macOS VMs). No checkout runs and nothing is rolled back. |
| git.worktree_create | `refusing to create worktree: <p> was not populated by git worktree add` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, after a successful add, when the git directory of `baseRepo` has no `worktrees` folder. Or, in the state `shadow`, `.git` no longer names the held git directory right after the add, and is no real directory that passes the gate (`5fd08069`, round `val-s3p2b2-10`, Linux and macOS VMs). No checkout runs and nothing is rolled back. `89cb6289` answers it with a daemon `GIT_COMMON_DIR` of another repository (rows B-E1 and B-E3 and cell D8e, Linux and macOS VMs). |
| git.worktree_create | `refusing to create worktree: <p> carries a .git file naming an admin directory whose own record is of a different worktree` | in `error`, `errorCode:"unsafe_path"`, after a successful add. The `gitdir` record of the registration in the git directory of `baseRepo` was read and does not name `<p>/.git`. A record with a slash or a blank at its end, a `/./` part or a double slash names it (`5fd08069`, cell V4 of round `tier0-10`, cells R1 to R3 of round `tier0-11`, Linux VM). No checkout runs and nothing is rolled back. If `timeoutMs` expired during the add, the answer is the same (cell P-f). `89cb6289` answers it in cells P-c and P-k to P-m (Linux and macOS VMs), and `5fd08069` in cell V5c of round `tier0-10` (Linux VM). |
| git.worktree_create | `git worktree add failed (verification): could not verify the worktree's registration: git did not report its spelling of the path (entry <name>); retry` | in `error`, `errorCode:"worktree_add_failed"`, Linux and macOS. The state of the row above, and git fails the `rev-parse --show-toplevel` call that follows. `<name>` is the entry that the `.git` file names. The leaf, the one new entry of the `worktrees` folder and the new branch go (`5fd08069`, cell V5 of round `tier0-10`, cell P1 of round `tier0-11`, Linux VM). claustrum's own, not measured: the folder has one new entry, and no time limit stopped the call. |
| git.worktree_create | `refusing to create worktree: <p> carries a .git file naming an admin entry other than the one just created for it` | in `error`, `errorCode:"unsafe_path"`, after a successful add. On Windows a leaf `.git` file of another staged entry gets it before the move, and only the leaf and branch go (cell S2, round `task087-01`). The registration is not the one new name of the `worktrees` folder since the listing before the add. With two new entries `5fd08069` answers it (cell V2 of round `tier0-10`, Linux VM). With no new entry `89cb6289` answers it (cells A9 g, A10 g and A13 g, Linux and macOS VMs). claustrum's own, not measured: a listing that fails gets it, and a refusal of the four rows above comes first. Otherwise no checkout runs and nothing is rolled back. |
| git.worktree_create | `refusing to create worktree: <p> has no .git file` / `refusing to create worktree: <p>/.git is not a regular file` / `refusing to create worktree: <p>/.git does not name a git dir` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, after a successful add. The `.git` entry of the new worktree is gone, is no regular file, or does not start with `gitdir: `. The registration and the branch stay, and an empty leaf goes (`5fd08069`, cells V3a, V3b and V3c of round `tier0-10`, Linux VM). |
| git.worktree_create | `refusing to create worktree: <p> is no longer the directory that was created for the worktree` | in `error`, `errorCode:"unsafe_path"`, Linux and macOS, before the checkout. Another folder is at the path of the leaf. Nothing is checked out or rolled back (`5fd08069`, cell V8 of round `tier0-10`, Linux VM). |
| git.worktree_create | `git worktree add failed: cannot prepare the repository's worktrees directory: "<git dir>/worktrees" is not a directory` / `git worktree add failed: cannot prepare the repository's worktrees directory: openat worktrees: permission denied` / `git worktree add failed: cannot list the repository's worktree entries: "worktrees" is a link or a file where Claude needs a real directory; put the directory itself in its place` | in `error`, `errorCode:"worktree_add_failed"`. In the state `shadow` on Linux and macOS, a symbolic link or a file at `worktrees` gets the first text. In that state a folder there that cannot be opened (mode 0300) gets the second. With `"shadowDirOff":true` a link or a file there gets the third (cell C2offF of round `tier3-02` for a file, Linux VM). On Windows a junction or a directory symbolic link gets it too (cells C2off and C2offJ, Windows VM). A file there on Windows is claustrum's own, not measured. No `worktree add` runs, and no leaf or branch stays (`5fd08069`, cell V1 of round `tier0-10` and cells C1, C2, C4 and C5 of round `tier0-24`, Linux VM, cells C2 and C5 of round `tier0-25m`, macOS VM). |
| git.worktree_create | `failed to create parent directory: lstat <path>: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`. There are two cases. First, a root symlink names a missing target, and `<path>` is that target. Second, a directory from `/` down to the root cannot be searched. Then `<path>` is `<dir>/.git` for the highest such directory. When that directory is above the root and fails the ancestor test itself, it gets the ancestor text instead (row G36a). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. Other errors from resolving the root are sent as Go prints them (not measured) |
| git.worktree_create | `failed to create parent directory: <path>: not a directory` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`, when the deepest existing part of the root is not a directory after its symlinks are resolved. `<path>` is that resolved path. It can be the root, a path above the root, or the file that a root symlink names. A regular file above the root gets this text, not the ancestor text (cell C-W14). Nothing is created. Measured against `f6010b97` on Linux and macOS VMs |
| git.worktree_create | `failed to create parent directory: <path> is not a directory` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS. Without `worktreeRoot`, an existing component above the leaf is not a directory. `<path>` is its full path, with the symlinks of the repo resolved and with `//` and `/./` removed. With `worktreeRoot`, the `<directory>` level exists and is not a directory, and `<path>` is its path with the root resolved. A `<directory>` that is a symlink gets the symlink refusal first. Nothing is created. Measured against `f6010b97` on Linux and macOS VMs. Linux and macOS VMs measured the symlinked `baseRepo`, the `/./` and the symlinked root. A macOS VM measured the `/tmp` repo spelling. On Windows, without `worktreeRoot`, a file in the path keeps the `os.MkdirAll` text, `mkdir <path>: <OS error>` (not measured) |
| git.worktree_create | `failed to create parent directory: statat .: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, without `worktreeRoot`, when an existing `.claude` or `.claude/worktrees` cannot be searched. This holds also when nothing is left to create. Measured against `f6010b97` on Linux and macOS VMs |
| git.worktree_create | `failed to create parent directory: statat <name>/.git: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`, when the `<directory>` level exists and cannot be searched. `<name>` is its last component. Measured against `f6010b97` on Linux and macOS VMs |
| git.worktree_create | `failed to create parent directory: mkdirat <name>: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, when a missing directory above the leaf cannot be created. Without `worktreeRoot`, `<name>` is that directory's last component. With `worktreeRoot`, `<name>` is its path relative to the deepest directory that existed before the call, for example `a/b`. A directory that the call made before the failure stays. Measured with `permission denied` and `file name too long` against `f6010b97` on Linux and macOS VMs. Other errno texts take the same shape (not measured). A root made in the same call, then a failed `<directory>`, reads `mkdirat R/cp` by the same rule (not measured) |
| git.worktree_create | `failed to create parent directory: cannot mark <dir> as a worktree location: openat .claude-managed-worktrees: <errno text>` | in `error`, `errorCode:"mkdir_failed"`, Linux and macOS, with `worktreeRoot`, when the marker cannot be created and no entry of that name exists. `<dir>` is the `<directory>` level with the symlinks of the root resolved. The leaf is not made. Measured against `f6010b97` on Linux and macOS VMs, also under a symlinked root |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <p> is not <worktree location>/<directory>/<name> beneath <root>` | `<p>` and `<root>` as sent (cell C-W2). Create adds `errorCode:"unsafe_path"` in each row below that names no other code |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <p> uses the reserved name <name> for a directory under the worktree location` | `<name>` is `.git` or `.claude-managed-worktrees`, in any letter case (cells C-W3 and C-S1) |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> has a component named <name>, which is reserved; …` | cells C-W4, C-W4e and C-S2. Not measured: `.claude-managed-worktrees` there, and its order against the `worktreePath` tests |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> would place worktrees in <dir>, which contains the repository <repo>; choose a location the repository is not inside` | cells C-W5, C-W6, C-S3 and C-S8 |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> would place worktrees in <dir>, which is (or is inside) <wt>, a worktree of the repository <repo>; …` | cells C-W19 and C-S7 |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> resolves to the filesystem root; …` | cells C-W20 and C-S6 |
| git.worktree_create | `refusing to create worktree: <root> leads into a session worktree (at <p>; beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees marker); a worktree location must be outside every session's worktree` | cell C-W9. claustrum tests it before the write tests of the root (not measured) |
| git.worktree_create | `refusing to create worktree: <root> would be made inside a session worktree or its per-repository directory (at <p>; …` | cell C-W10 |
| git.worktree_create | `refusing to create worktree: <root> is itself a per-repository worktree directory (it holds a .claude-managed-worktrees marker); choose the directory above it, or another location` | cell C-W11 |
| git.worktree_create | `refusing to create worktree: <dir> is writable by <who> (mode <mode>); choose a directory only you can write to, or remove the extra write permission (chmod go-w)` | for the root, and for the `<directory>` level (cell C-W18). claustrum tests that level before its `.git` test (not measured) |
| git.worktree_create | `failed to create parent directory: cannot determine the repository's work tree: <exec error>` | `errorCode:"mkdir_failed"`, cell C-W17b |
| git.worktree_create | `failed to create parent directory: cannot list the repository's worktrees: <exec error>` | `errorCode:"mkdir_failed"`, cell C-W17 |
| git.worktree_create | `git worktree add failed: <text>` | in `error`, `errorCode:"worktree_add_failed"`. `<text>` is made from git's stderr by the [text rule](#git-directory-trust-check). The text of claustrum does not start with git's graft-file `hint:` lines. The rollback runs no git call and removes the leaf only if it is empty. A pre-existing branch is not deleted (`4534d86`). A failed add answers this frame even when the caller `timeoutMs` expired during the add (measured against `f6010b97` and `90fca6e6` on a macOS VM) |
| git.worktree_create | `git worktree add failed (completing the new worktree's git files): <cause>` | in `error`, `errorCode:"worktree_add_failed"`, when the `config.worktree` step after the add fails. On Windows a failed staging step gives it, such as `git registered <n> worktrees where one was expected; try again` or `the repository already has a hundred worktrees called "<name>"; remove some (git worktree prune, git worktree remove)` (rounds `task009-01`, `task087-01`). No checkout runs, and the rollback is that of a failed checkout. It wins over an expired `timeoutMs` (cell R8 of round `task007-03`, Linux VM). `5fd08069`, rounds `task007-01` and `task007-03` (Linux and macOS VMs), and cell C1 on a Windows VM |
| git.worktree_create | `git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)` | in `error`, `errorCode:"worktree_add_failed"`, when the attach add for `existingBranch` fails and the fallback `-b <branchName>` add fails too. The text rule makes each text on its own, each with its own 512-byte cap. Measured against `f6010b97` and `90fca6e6` on a macOS VM |
| git.worktree_create | `git worktree add failed (checkout): <text>` | in `error`, `errorCode:"worktree_add_failed"`, when the `read-tree` checkout fails. Same text rule, and the same rule for the `hint:` lines. With a settings copy a `<text>` that holds `smudge filter lfs failed` after the cut gets `. Claude doesn't use ssh, credential, or proxy settings from this repository's own git config. If the download needs one and you trust that setting, copy it into ~/.gitconfig on the remote machine, then try again.` after it (`5fd08069`, round `task081-01`, Linux VM). The new directory is removed. The branch the call created goes only when another ref reaches its tip (see [The branch step](#the-branch-step)). In attach mode the attached branch is kept (measured against `f6010b97`). A failed placement of the index answers this frame too. The OS error is `openat <registration name>/index: permission denied` (`89cb6289`, rows A14, A14f and A14b, Linux and macOS VMs) or `open <temporary directory>/index: no such file or directory` (cell X1, the same VMs). On Windows it is `openat index: <cause>` (`5fd08069`, cells S11 and S12 of round `task087-01`), or claustrum's own `the registration <p> is not the folder that was moved into place` (D24, not measured) |
| git.worktree_create | `Claude doesn't run the "<name>" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter "<name>"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.` | in `error`, `errorCode:"repository_only_filter"`, when, in the state `shadow`, a path of the checkout uses a git filter that only the git config of the repository defines, or, with a settings copy, one whose outside definition is only `process = cat`. The rollback is the one of a failed checkout. Measured against `5fd08069` on Linux and Windows VMs. In the state `fallback-git` git runs the filter (see [Git filters](protocol/git-worktree-create.md#git-filters)). A quote mark in `<name>` has a backslash before it (Linux VM). `89cb6289` runs the filter there. |
| git.worktree_create | `git worktree add failed (checkout): this repository's git config cannot be used: a filter defined only there has a name longer than 200 bytes (it starts "<first 40 bytes>"); define it in ~/.gitconfig on the remote machine, or rename it` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow`, for such a filter with a name of 201 or 300 bytes. The rollback is the one of a failed checkout. Measured against `5fd08069` on Linux and Windows VMs |
| git.worktree_create | `git worktree add failed (checkout): this repository's git config cannot be used: it defines more than 64 filters` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow` with a settings copy, when the repository config has more than 64 filter names. A name with a `smudge` key counts (Linux, macOS and Windows VMs), and so does one with only a `clean` key (Linux and macOS VMs). A name with only other keys counts too (claustrum's own). No read-tree call runs, and the rollback is the one of a failed checkout. Measured against `5fd08069` in round `task008-01`. The text wins over the texts of a name with `=` and of a name over 200 bytes (`task008-03`, cells R3 and R4, Linux VM). A failed config listing wins over it (cell R5) |
| git.worktree_create | `git worktree add failed (checkout): Claude could not read your own git config on the remote machine (<cause>). Run git config --list there to see the problem.` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow` with a settings copy, when the config listing right before the checkout fails. No read-tree call runs, and the rollback is the one of a failed checkout. On Windows a `.git` that became a regular file during the first listing does not stop the checkout. In cell X2 (a file `gitdir: <another git directory>`) the second listing then fails with `exit status 128` (`5fd08069`, round `task077-03`, Windows VM). `<cause>` is `exit status 1` for a listing that exits 1 (Linux, macOS and Windows VMs), also while a child holds stdout past 30 s (claustrum's own, not measured). A git that still runs at 30 seconds gets SIGTERM, then SIGKILL 2 s later. Unless it exits 0, `<cause>` is `git took too long: Claude stopped git config after 30 seconds; try again` (cells A4x and A4x-t, rounds `task111-05` and `task111-06`, Linux VM). On Windows git ends at once (A4x, round `task111-08`). An exit 0 counts, also with stdout held past 30 s (A4-35, round `task111-03`). The `timeoutMs` of the caller does not end the listing (round `task008-05`, cells P1 and P2, Linux VM). Measured against `5fd08069` in round `task008-01` |
| git.worktree_create | `git worktree add failed (checkout): config-defined hooks could not be pinned off; git not run: listing the configuration in force: <detail>` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow`, when the first config listing after the add fails. One `git version` call runs first, and the rollback follows. `5fd08069` answers so (cell L6 of round `task084-02`, Linux VM, and cell Q4bp, macOS VM). If that `git version` fails too, the text after `(checkout): ` is `git cannot run on this host; git not run: <exec error>: <git text>` (cell E13 of round `task083-01`, Linux VM). |
| git.worktree_create | `git worktree add failed (checkout): this repository's git config cannot be used: the filter "<name>" is defined only there, and Claude cannot handle "=" in a filter's name; define it in ~/.gitconfig on the remote machine, or rename it` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow`, for such a filter with `=` in its name. The rollback is the one of a failed checkout. Measured against `5fd08069` on a Linux VM |
| git.worktree_create | `git worktree add failed (checkout): this repository's git config cannot be used: it names a program for git-lfs ("<key>"); remove that setting, or put it into ~/.gitconfig on the remote machine` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow` and only with no settings copy, when a path uses the filter `lfs` of the user or the system config and the config of the repository has the key `<key>`, a program key of git-lfs. The rollback is the one of a failed checkout. The text is claustrum's own. |
| git.worktree_create | `git worktree add failed (checkout): the git filter "<name>" has values in this repository's git config that no option of git can replace for the checkout` | in `error`, `errorCode:"worktree_add_failed"`, in the state `shadow` (claustrum's own, not measured). With no settings copy it answers a repository `process` key beside an outside filter with none. It also answers a shared name with `=` and a name that is not valid UTF-8 there. With a copy it answers a shared name with `=` or one that is not valid UTF-8 where the repository adds only `required = true`. With a copy `5fd08069` answers success for the `process` key (cell F1 of round `task073-02`, Linux VM), and claustrum follows it. |
| git.worktree_create | `git worktree add timed out after <n>ms (deadline expired {before the checkout started / during the checkout): <text> / after the checkout finished})` | in `error`, `errorCode:"timeout"`, from the caller-supplied `timeoutMs` (`4534d86`). An absent `timeoutMs`, or 0, arms no deadline. `<text>` comes from the stderr of the killed git, by the same text rule |
| git.worktree_create | `<frame>; and the undo could not finish for <leaf>: {the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll <entry>: <OS error>) / the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat <leaf base name>: <OS error>)}` | appended to the checkout-failure frame and to each `timeout` frame when a step of the rollback fails. The `errorCode` stays as it was. Measured against `f6010b97` and `90fca6e6` on a Windows VM. In attach mode the first text reads `the worktree directory and its registration both remain` instead, because the call made no branch (row C08b, `f6010b97` and `89cb6289`, Linux VM). On Linux and macOS a registration that cannot be deleted gives `the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll <registration name>: <OS error>)` instead, with the leaf removed (`89cb6289`, rows A14 and A14f on a Linux VM, A14 and A14b on a macOS VM). The text in the parentheses can be claustrum's own, see step 2 of the rollback. In attach mode it reads `the worktree registration remains; remove it by hand before retrying (RemoveAll <registration name>: <OS error>)` (cell X5, Linux and macOS VMs). No rmdir text joins either one (cell X11, Linux VM) |
| git.worktree_create | `<frame>; and the undo could not finish for <leaf>: <branch part>` | appended when the branch step of the rollback keeps the branch that the call made. See [The branch step](#the-branch-step) for each `<branch part>`. After a failed leaf rmdir, the branch part follows the rmdir text after `; ` (row C05). Some cases also add `"branchKept":true` after `errorCode`. The table there gives each case with its rows and VMs |
| git.worktree_remove | `refusing to remove worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; …}` | in `error`, with no `errorCode`. This is `7d193f89` containment. The spelling refusal is Windows-only and comes before containment. `<repo>` is `baseRepo` as sent. An absent or empty `baseRepo` gives the empty string (`f6010b97`, Linux and macOS VMs). A Windows VM measured an absent one. With `worktreeRoot`, the first two texts also refuse a relative, absent or `..` `baseRepo`, and `<p>` is then `baseRepo` as sent. An empty `worktreePath` gets the first text there (`f6010b97`, Linux and macOS VMs) |
| git.worktree_remove | `refusing to remove worktree: <root> is a filesystem root; choose the worktree location by its absolute path, without "..", beneath the filesystem root` | in `error`, with no `errorCode`, Linux and macOS, with `worktreeRoot`. A `worktreeRoot` of `/` is refused. `89cb6289` sends this frame for a `worktreePath` of `/<name>`, with no git call. The frame is the same with the folder present or gone, and for a daemon that runs as root. Nothing is deleted (Linux VM, cells R1n, R1u and R1r, and macOS VM, cells R1a and R1b). Cells R2 to R6 of the create row hold here too. claustrum tests a relative `worktreePath` first, as on create (not measured on remove). A root with a `..` component that cleans to `/` gets the `..` text (from the code, not measured). Windows refuses every `worktreeRoot` first, so a drive root such as `C:\` is not measured |
| git.worktree_remove | `refusing to remove worktree: <root> is the repository <repo> or inside it; a worktree location must be outside the repository` | in `error`, with no `errorCode`, with `worktreeRoot`. The test cleans the root and `baseRepo` and compares whole components. A root that is `baseRepo` or lies beneath it is refused, and `<B>/Tx` beside `<B>/T` passes. `<root>` is `worktreeRoot` as sent, so a trailing slash stays. `<repo>` is `baseRepo` as sent, so `<T>/` and `<T>/.` stay too. It comes after the spelling, `baseRepo` and two-level tests, and before any git call. The worktree is not looked at. Nothing is deleted. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_remove | `refusing to remove worktree: <root> leads into the repository <repo> (at <at>); a worktree location must be outside the repository` | in `error`, with no `errorCode`, with `worktreeRoot`. The root passes the test above. The daemon resolves the symlinks of the longest existing part of the root and appends the missing rest. Then it walks from that path up to `/`. `<at>` is the outermost path on that walk that is the same file as one of three folders. These are the git top level of `baseRepo`, the main checkout and the git directory. The main checkout is the first entry of `worktree list`. `5fd08069` refuses a separate git directory so (cell C-S4). So `<at>` is a prefix of the resolved root, for example `/private/tmp/…` for a root sent as `/tmp/…` on macOS. A bind mount (Linux), a firmlink spelling and a case variant (macOS) match too, and `<at>` keeps their spelling. With `baseRepo` in a linked worktree, a bind mount (Linux row T1) or a case variant (macOS row T1m) of the main checkout matches too. `<repo>` is `baseRepo` as sent. It is below `<at>` in row Q19, and a linked worktree in row W1. The test of the row below runs on each folder too. The folder nearest to `/` decides (from the code, as in create cell C-W21). If both checkouts hold the root, the main checkout is named (row T2b). There it is also the outer one, and claustrum names the outer match. It comes after the work-tree step and `worktree list`. Nothing is deleted. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_remove | `refusing to remove worktree: <root> leads into <wt>, a worktree of the repository <repo> (at <wt>); a worktree location must be outside the repository's checkouts` | in `error`, with no `errorCode`, with `worktreeRoot`. The root passes both tests above. `<wt>` is a linked worktree in `worktree list` that is the resolved root or a parent of it by whole components. The test compares `<wt>` as listed, as a string. It resolves no symlink in `<wt>`, folds no case and compares no file identity. It skips a listed worktree whose path does not exist, also a locked one (rows Y9 and T3). Of two nested worktrees, the outer one is named. A root that holds a linked worktree passes. claustrum skips only a path that does not exist. Another error of the lstat keeps the entry (not measured). `<repo>` is `baseRepo` as sent. Nothing is deleted. Measured against `f6010b97` and `89cb6289` on Linux and macOS VMs |
| git.worktree_remove | `failed to remove worktree: the worktree location <root> is not reachable (<reason>); nothing was removed — retry once it is available, or remove the worktree by hand` | in `error`, with no `errorCode`, with `worktreeRoot`. If the root passes the three tests above but does not exist or does not resolve, this refusal comes right after them. `<root>` is `worktreeRoot` as sent. For a root that does not exist, `<reason>` is `<path> does not exist`. `<path>` is the root with the links of its existing part followed and a final slash dropped. `5fd08069` names it so in cells C-W22 and X3 (Linux VM) and M1 and M2 (macOS VM) of rounds `tier0-20` and `tier0-22`. A root below a regular file gets `<file>: not a directory` (cell C-W24). For a symlink that does not resolve, `<reason>` is the error of resolving it, for example `lstat <target>: no such file or directory` (row T5). claustrum sends other resolve errors the same way (not measured). Another error of the lstat keeps the old answer. For `permission denied` both references and claustrum answer `failed to remove worktree: lstat <root>: permission denied` (row T6). Nothing is deleted, and the branch stays. Rows T5 and T6 ran against `f6010b97` and `89cb6289`, row T6 on a Linux VM only |
| git.worktree_remove | `refusing to remove worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, with no `errorCode`. It keeps the delete off a planted link (`7d193f89`) |
| git.worktree_remove | `refusing to remove worktree: <t> {is a symbolic link, not a worktree directory / is not a directory}` | in `error`, with no `errorCode`, for a leaf that is a symbolic link or not a directory. `<t>` has the symbolic links of `baseRepo` resolved. With `worktreeRoot`, the links of the root are resolved instead. Nothing is deleted (`f6010b97`, macOS VM) |
| git.worktree_remove | `refusing to remove worktree: <t> changed while the removal was checking it` | in `error`, with no `errorCode`. claustrum's own text for a leaf that another directory replaced between two looks. Only a race reaches it, and no run has measured the reference there. Nothing is deleted. On Linux and macOS, with `worktreeRoot`, a `<directory>` that turns into a symlink out of the root between two looks answers `failed to remove worktree: openat <directory>: path escapes from parent`. That text is claustrum's own and is not a parity claim |
| git.worktree_remove | `refusing to remove worktree: <p> is locked (git worktree lock); unlock it to remove it` | in `error`, with no `errorCode`. `7d193f89` refuses a LOCKED worktree (`success:false`) and leaves it in place. The message is fixed whatever the lock reason is. Before `7d193f89` the reference deleted it and answered `success:true`. In four states `89cb6289` deletes the folder or answers success, and claustrum refuses (D22). With `worktreeRoot`, the frames differ in rows q2 to q4 (Linux VM) too. |
| git.worktree_remove | `refusing to remove worktree: <p> is gone but its registration is locked (git worktree lock); unlock it to remove the registration and branch` | in `error`, with no `errorCode`, for a gone worktree with a locked registration. Nothing is deleted (`f6010b97`, macOS VM). On Linux and macOS a gone worktree whose locked entry sits behind a linked `<git dir>/worktrees` gets success, and its branch goes. That follows `5fd08069` (cell L1 of round `tier0-26`, Linux VM), by the maintainer's decision of 2026-10-14. |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (<reason>); retry` | in `error`, with no `errorCode`, for a gone worktree. Without `worktreeRoot`, `<reason>` is the hooks refusal or the trust refusal text (`f6010b97`, macOS VM). Since `89cb6289` it is also the `git cannot run` text (row L11). claustrum puts a limit refusal of the listing there too. `89cb6289` measured the limits on `git.info` only. `<reason>` is also the refusal of the daemon's `GIT_CONFIG_COUNT` (Linux, macOS and Windows VMs). With `worktreeRoot` and a missing absolute `baseRepo` without a `..` component, `<reason>` is the refusal of the daemon's `GIT_CONFIG_COUNT` (Linux and macOS VMs) |
| git.worktree_remove | `failed to remove worktree: "" does not name a directory` | in `error` (empty `worktreePath`, without `worktreeRoot`) |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked: <text>` | in `error`, without `worktreeRoot`. `<text>` is the text of the refused step |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (its registrations could not be examined); retry` | in `error`, with no `errorCode`. Without `worktreeRoot`, for a worktree directory that exists: the listing of `baseRepo` cannot be used, `baseRepo` holds no repository, or the trust check refuses its git directory. When the daemon's `GIT_CONFIG_COUNT` is refused, the same holds. Linux and macOS VMs measured that also for a `baseRepo` of `<T>/missing/..`, which does not resolve. In both modes, for a worktree directory that exists: the worktree registry exists but cannot be read, or, on Linux and macOS, is a symbolic link (`5fd08069`, cell C3 of rounds `tier0-24` and `tier0-25m`, Linux and macOS VMs, cells L2 and L3 of round `tier0-26`, Linux VM). Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: could not check whether <p> is locked (the repository at <baseRepo> could not be read); retry` | in `error`, with no `errorCode`, with `worktreeRoot`, when the daemon's `GIT_CONFIG_COUNT` is refused, `baseRepo` is missing and the worktree directory exists. This row covers an absolute `baseRepo` without a `..` component. Nothing is deleted (`f6010b97`, Linux and macOS VMs). With no `git` on `PATH`, the same text answers a missing `baseRepo` in three rows (`f6010b97` and `89cb6289`, Linux VM). The worktree directory holds a file (row L13wa), is empty (row L13we), or holds a `.git` file that names its entry (row DG1) |
| git.worktree_remove | `failed to remove worktree: cannot determine the repository's work tree: <reason>` | in `error`, with no `errorCode`, with `worktreeRoot` only. `<reason>` is a trust refusal text, `exit status 128`, the hooks refusal, or the refusal of the daemon's `GIT_CONFIG_COUNT` (`f6010b97`, Linux VM). Since `89cb6289` it is also `git finds no repository here: <git text>` (row N04). With git 2.56 also the text of a failed call about the repository (round `tier0-27`). With no `git` on `PATH`, both references answer the exec error alone in nine rows where `baseRepo` exists (Linux VM, see "Git-directory trust check"). claustrum also puts the `git cannot run` text and a limit refusal of the listing there. Neither is measured with `worktreeRoot`. Nothing is deleted. See [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks) |
| git.worktree_remove | `failed to remove worktree: cannot list the repository's worktrees: <exec error>` | in `error`, with no `errorCode`, with `worktreeRoot` only. `git worktree list --porcelain -z` and then `git worktree list --porcelain` both fail in `baseRepo`. Nothing is deleted. `89cb6289` answers `exit status 128` for a repository with a valid `HEAD` whose `.git/commondir` is a dangling relative symlink (row DG2s-g, Linux VM) |
| git.worktree_remove | `failed to remove worktree: statat .claude: permission denied` / `failed to remove worktree: open <baseRepo>: <error>` | in `error`, with no `errorCode`, without `worktreeRoot` only. `baseRepo` cannot be searched (mode 0600), or cannot be opened (mode 0000, or a regular file). On Linux and macOS the first text names the first component of `worktreePath` below `baseRepo`, for example `statat wt` or `statat a` (`89cb6289`, cells U19, N4, N5, N6a, N6b and N7a). Nothing is deleted |
| git.worktree_remove | `failed to remove worktree: open <root>: permission denied` / `failed to remove worktree: openat <name>: permission denied` / `failed to remove worktree: statat .: permission denied` | in `error`, with no `errorCode`, Linux and macOS, for a folder that the daemon cannot read or search. With `worktreeRoot`, that is a folder from the root down to the worktree folder. Without it, that is a folder between `baseRepo` and the worktree folder. `<root>` is `worktreeRoot` with its symbolic links resolved. `<name>` is one path component. Nothing is deleted. Measured against `89cb6289` on Linux and macOS VMs (rows B1 to B6, B8 to B11b, B13a to B14c and B17a to B17d, row B18 on the macOS VM only, cells U1 to U11h and U13a to U15b). Windows is not measured. See [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks) |
| git.worktree_remove | `failed to remove worktree: RemoveAll <entry>: <errno text>` | in `error`, when the delete of the worktree fails part-way. If an entry other than `.git` fails, `.git` stays. The entry of the worktree and the branch stay in every case (`f6010b97`, macOS VM) |
| git.worktree_remove | `removed the worktree but could not drop its registration (RemoveAll <name>: <errno text>)` | in `error`, `success:false`, when the verified entry cannot be deleted after the tree. The branch step still runs. On `f6010b97` the branch is deleted (macOS VM). Since `89cb6289` a kept branch adds `"branchKept":true` after `error` (row R19, Linux VM) |
| git.worktree_remove | `failed to remove worktree: openat <path from baseRepo>: path escapes from parent` | D19, Windows, in `error`, with no `errorCode`, when a directory between `baseRepo` and the leaf is a junction. `<path from baseRepo>` is the folder that holds the leaf, relative to `baseRepo`. It is `.claude\worktrees` in the default layout, and `J\sub\.claude\worktrees` and `J\.claude\worktrees` in cells J-b-wt-pj and J-b-wt-pJ (Windows VM). claustrum's own text. Nothing is deleted, and the branch stays |
| git.worktree_remove | `worktreePath must not be or contain the home directory: …` | D2, in `error`. It sits behind `7d193f89` containment on the default branch, where it fires only if a repo is an ancestor of home. It is the active home guard on the `external_root` branch |
| process.spawn | `Process ID is required` / `Command is required` | |
| process.spawn | `command must be an absolute path when a launcher is given` | with a `launcher` param |
| process.spawn | `the managed launcher cannot be used: <reason>` | -32004. The reasons are the argv[0] and no-launcher [texts of launcher.resolve](protocol/launcher-resolve.md#unusable-reasons), and `a launcher cannot be applied on a Windows host` |
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
- Mistyped `params` gives `-32602 Invalid params`. This covers a wrong type in a
  member that the method reads (`"maxBytes":"4"`, `"path":123`) and a non-object value (`"params":"x"` or
  `[…]`). The daemon does not coerce the value, and it does not ignore the decode
  error.
- The daemon ignores a member that another method of the namespace reads,
  whatever its type.
  So `files.stat {"maxBytes":"{"}` answers as `files.stat` with no `maxBytes`.
  `5fd08069` does the same (round `task015-01`, Linux, macOS and Windows VMs).

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
from a path the daemon cannot examine. `files.read` opens the path where the
other two stat it:

- A genuine `ENOENT` is the "does not exist" answer in each method's own shape.
  The three shapes are `exists:false`, `content:"" exists:false`, and
  `valid:false` with `error:"Path does not exist"`.
- The daemon reports any other failure with the underlying message.
  `files.stat` returns `-32603 stat <path>: <reason>`. `files.read` returns
  `-32603 open <path>: <reason>`.
  `files.validate` keeps its result shape and puts that text in its `error` field
  instead. Reachable reasons include `not a directory` (a path component is a
  regular file), `file name too long`, and `invalid argument` (a NUL byte in the
  path). For `files.read`, `5fd08069` gives these three with `open` on Linux and
  macOS VMs.

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
| `server.capabilities` | none | `{"version":"<id>","methods":[…20…],"instanceId":"<32-hex>","startedAt":<unix-ms>,"features":["process.stdin.offset","files.extract_tar.execBit","git.status.baseRepo","git.info.discovered_root","git.worktree_create.timeoutMs","git.worktree_create.existingBranch","git.worktree_remove.unpushedGuard","process.spawn.shellAgentSocket","launcher.managed","server.peer_check","git.shadow_dir","git.worktree.external_root","server.instance_id"],"peerCheck":"off"}`. `plugins.prune` is the last method on every OS. `git.worktree.external_root` is omitted on Windows. `files.extract_tar.execBit`, `git.worktree_create.timeoutMs`, `git.worktree_create.existingBranch`, `git.worktree_remove.unpushedGuard`, `process.spawn.shellAgentSocket`, `launcher.managed`, `server.peer_check`, `git.shadow_dir`, `instanceId`, `startedAt` and `peerCheck` are present on every OS |
| `server.shutdown` | none | `{"ok":true}`, when the reply gets out. The handler returns the reply as soon as the stop has started. The frame arrives only when its write wins the race with the close. See below |

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
  `git.status.baseRepo` and `git.worktree.external_root`. That build drops the
  latter on Windows, and claustrum matches. `4534d86` inserted
  `git.worktree_create.timeoutMs` before `git.worktree.external_root` and appended
  `server.instance_id`, which is always last. `19f30c46` inserted
  `git.worktree_create.existingBranch` after `git.worktree_create.timeoutMs`.
  `f6010b97` inserted `process.spawn.shellAgentSocket` after it. `89cb6289`
  inserted `git.worktree_remove.unpushedGuard` after
  `git.worktree_create.existingBranch`, `git.info.discovered_root` after
  `git.status.baseRepo` and `launcher.managed` after
  `process.spawn.shellAgentSocket`. The `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not
  change the capabilities frame (measured on macOS). `5fd08069` inserted
  `server.peer_check` after `launcher.managed` and `files.extract_tar.execBit`
  after `process.stdin.offset`. The Linux, macOS and Windows VMs saw the features of
  `89cb6289` and `5fd08069` at their places. `git.shadow_dir` follows
  `server.peer_check` (rounds `task011-01` and `s12`).
- `peerCheck` comes directly after `features`, in every answer. The daemon reads
  `CLAUDE_SSH_PEER_CHECK` once, at its start, and then removes it from its
  environment. On Windows that covers any letter case. Unless the removal fails, no
  child of the daemon inherits it: no `process.spawn` child, no git call, no
  launcher run and no login-shell read. Only the exact value `1` turns the check on. Each other value, and
  no variable, answer `"off"`. On `5fd08069` each of these gave `"off"`:
    - Linux VM: no variable, `0`, `off`, the empty value, `true`, `yes`, `on`,
      `2`, `01` and ` 1` (a space before the digit).
    - macOS VM: no variable, `0`, `off`, the empty value and `true`.
    - Windows VM: no variable, `0` and `off`.

  With `1`, macOS and Windows answer `"unavailable"`, as `5fd08069` does (macOS
  and Windows VMs), and serve every caller. Linux answers
  `"peerCheck":"on","peerCheckBy":["network","pidfd"]` (round `task011-01`, cell
  P1, Linux VM). A kernel that lacks a part gets another state from claustrum
  (not measured). The start line is in
  [Daemon log](#daemon-log-remote-serverlog).
- On Linux with `1` the daemon checks each new connection before it reads from
  it. It refuses a caller whose text, in the form of the start line, differs
  from its own (cell P2). How claustrum reads each part is its own (not
  measured). A refused caller gets one frame, then the connection closes:

  ```
  {"jsonrpc":"2.0","id":null,"error":{"code":-32006,"message":"Refused: this server could not confirm that the caller is in its own namespaces","data":{"reason":"other_namespace"}}}
  ```

  The reason is `other_namespace` for other namespaces (cell P2) and
  `unreadable` for a link that the daemon cannot read (cell P3, a root caller).
  An exited caller gets `lookup_failed` (cell P7). claustrum gives every other
  failure `lookup_failed` (not measured).
  The daemon reads no request, so an unauthenticated `server.shutdown` does
  nothing (cell P4). A caller that wrote first reads the frame and then a reset
  (cell P5). A refused connection has no `New connection` line. Refusals 1 to 10
  each log one WARN line, and refusals 11 and 12 log none (cell P6):

  ```
  [Server] refused caller number 1 (other_namespace): "user:[4026532385] net:1 pidfd"; this server's namespaces are "user:[4026531837] net:1 pidfd"
  ```

  A caller that exited gets no line (cell P7). claustrum gives the frames and
  check lines of `5fd08069` in cells P0 to P8, but for a level tag (round
  `task011-02`, Linux VM).
- `server.shutdown` is not authenticated. See [Authentication](#authentication).
- On the reference, `server.shutdown` often closes the connection with no reply.
  In round `task034-01` the `{"ok":true}` frame of `5fd08069` came before the close
  in 21 of 30 runs on macOS and in 4 to 9 of 30 on Windows (cell R1). On Linux it
  came in 0 of 30 (R1), and in 6 of 30 with a second idle connection (R2). Under
  `-stop` the daemon wrote it in 5 of 30 runs on macOS and 1 of 30 on Linux (R4).
  In claustrum the handler signals the teardown and returns the reply at once. It
  does not wait for the close of the connections, so the reply write races that
  close. The teardown closes the listener and tests the socket first. Then it
  closes every connection, before its other cleanup. A connection whose writer is
  busy is not marked closed.
  When the frame arrives, it is `{"jsonrpc":"2.0","id":<id>,"result":{"ok":true}}`
  followed by EOF. An idle connection gets a clean EOF with no bytes.
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
  request (`5fd08069`, cell W-S2 of round `tier3-01`, Windows VM only).
  [UPSTREAM-TRACKING.md](UPSTREAM-TRACKING.md) has a `89cb6289` run with `closed 0`. On Linux and Windows VMs, `f6010b97` and `89cb6289` logged that line at
  every clean shutdown (rows RP11, RP12a, RP12b, ST03, ST09, WN03, WJ03). The
  counts for a child that ended just before the shutdown are not measured.
- Log lines on a shutdown by `SIGTERM`. The daemon logs
  `[daemon] received terminated; shutting down (children will be killed)`, then
  `[Server] shutdown requested`, then the `cleanup:` line. On a Linux VM,
  `f6010b97` and `89cb6289` logged these three lines (rows RP11, RP12b, ST03).
  The line for `SIGINT` names that signal (`5fd08069`, round `task003-01`, Linux and macOS). With
  `-keep-children` claustrum writes `kept` in place of `killed` in the first line.
  The `cleanup:` line then says `killed 0 child process group(s)`.
- On `5fd08069` reply delivery is a timing race. In round `task034-02` claustrum answered
  in 30 of 30 runs of cell R1 on Linux, macOS and Windows. `5fd08069` answered in 1
  of 30 on Linux, 22 of 30 on macOS, and 8 and 6 of 30 in two Windows passes. In
  cell R3, a take-over on macOS, `5fd08069` answered in 5 of 30 and claustrum in 30
  of 30. The frame bytes are equal.

### files.* (param: `path`)

#### files.stat
`{path}` → `{"exists","isDir","size","mode":"-rw-r--r--"}`

Says whether a path exists, and gives its kind, size and mode. The page [files.stat](protocol/files-stat.md) has the parameters, the response and the errors.

#### files.list
`{path}` → `{"entries":[{"name","path","isDir"},…]}` (name-sorted)

Lists the entries of one directory, without the hidden entries. The page [files.list](protocol/files-list.md) has the parameters, the response and the errors.

#### files.read
`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`

Returns the content of one file as text, up to a size limit. The page [files.read](protocol/files-read.md) has the parameters, the response and the errors.

#### files.validate
`{path}` → `{"valid":bool,"isDir":bool[,"error"]}`

Says whether a path exists and whether it is a directory. The page [files.validate](protocol/files-validate.md) has the parameters, the response and the errors.

#### files.extract_tar
`{archivePath,destDir}` → extracts a gzip tar → `{"success":true,"fileCount":<n>}`

Unpacks a gzip tar archive into a directory. It deletes the old content of the directory and the archive. The page [files.extract_tar](protocol/files-extract-tar.md) has the parameters, the response and what stays after a failure.

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
- On Windows the walk of claustrum resolves no junction that comes before the last
  component of the path. It starts at the path as it is spelled, and it takes a
  `..` by text. The measured entries of `89cb6289` fit such a walk.
  The fixture is a repository P, a repository R and a junction `<P>\J` to
  `<R>\inner`. For `<P>\J\sub` that walk gives the `.git` of P. On `89cb6289` each
  git call of `git.info` after the excludes read carries
  `GIT_COMMON_DIR=<P>\.git` (Windows VM, cells B-01, B-02, B-07 to B-09 and B-16). The path
  `<P>\J\..\J\sub` gives the same entry (cell B-17d). With a second junction `<KP>`
  to P, `<KP>\J\sub` gives `GIT_COMMON_DIR=<KP>\.git` (cell B-17k). A junction
  outside any repository gives "no repository" and `GIT_DIR=NUL` on each call (cell
  B-03). A junction that is the last component gives the pin of P too (cell B-04).
  A folder symlink in the place of the junction resolves, and the pin is
  `<R>\.git` (cell B-06). The claustrum build of this change is equal to `89cb6289` in
  frame, disk and every git call in these cells, except B-03 (Windows VM). In cell
  B-03 `89cb6289` runs 3 git calls and claustrum runs 2. The third call is a
  `rev-parse --git-dir` that exits 128. Both sides carry `GIT_DIR=NUL` on each call
  there, and the frames are equal. From the code: claustrum starts at the spelled
  path when Go's `filepath.EvalSymlinks` fails for a path that is a directory. A
  missing name before `..` is another cause of that failure. The paths
  `<P>\missing\..` and `<P>\missing\..\J\sub` are equal in frame, disk and every
  git call (cells J-a1 and J-a2).
- From the code: only `git.info` reaches the spelled start. `git.list_branches`
  tests a `path` that is not empty with `filepath.EvalSymlinks` before its trust
  check, and `git.status` tests `baseRepo` so. `git.worktree_create` and
  `git.worktree_remove` test a `baseRepo` that exists so. If the resolve fails,
  each of the four answers there. `process.spawn` does not use the walk. On
  `git.info` an error frame depends on the spelled start too, not only `repoSlug`
  and `defaultBranch`. In cell B-15 the build before the spelled start answered
  the fields of R, and the build of this change answers the listing refusal of `89cb6289`
  (Windows VM). Since the read of the repository config, claustrum answers the
  class 1 text in the state of cell B-15 (from the code). The listing refusal stays
  for a config that git refuses and the read accepts, for example an `include.path`
  with no value.
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
  `89cb6289`. When a component of G is a symlink, the daemon resolves
  G. Else it keeps G as spelled. A G with a junction before its last component
  stays as spelled. For a junction `<KP>` to a repository, the pin of `89cb6289`
  and of the claustrum build of this change is `<KP>\.git` (Windows VM, cell B-17k).

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
  and pinned to itself, and without `"shadowDirOff":true` class 2 of
  [Repository config refusals](#repository-config-refusals) answers. With its entry
  gone that is not measured on `5fd08069`. Any other
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
| `git.worktree_remove` | without `worktreeRoot`, for a worktree directory that exists, `could not check whether <p> is locked: <text>`, and `could not check whether <p> is locked (<text>); retry` for a gone one. With it, the work-tree refusal and `<text>` | without `worktreeRoot`, the lock-check refusal for a worktree directory that exists, and `{"success":true}` for a gone one. With it, the work-tree refusal and `exit status 128` |

The lock-check refusal is
`{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`.
It has no `errorCode`, and the path is neither quoted nor cut. The work-tree
refusal is `{"success":false,"error":"failed to remove worktree: cannot determine the
repository's work tree: <reason>"}`, also with no `errorCode`. With `worktreeRoot`,
the listing classes below give their own reason. With `worktreeRoot` and no `git`
on `PATH`, the exec error replaces both texts of the table in the measured rows
(class 1 below).

Where the trust check finds no repository, the daemon still runs the configuration
listing in that folder. In a
plain folder it carries `GIT_DIR=<null>`. The
call log of `89cb6289` shows that listing with no `GIT_CONFIG_*` entry too, and the
frames are those of the "no repository" column there. A listing that fails by class
3 below gives the "refused" column.
`89cb6289` answers so for a plain folder and for a folder with an empty `.git`
folder. Rows A-N1 to A-N6, A-N5b, A-S1 and A-X2 show that on Linux and macOS VMs.
Row A-N5 shows it on the Linux VM only. On macOS `89cb6289` and claustrum answer the
`worktreePath` shape refusal there, with no git call.
Cells P3-infoN, P3-infoX, P3-lbN, P3-statN, P3-crN and P3-rmN show it on a Windows
VM. See "The daemon's own git environment".

A daemon `GIT_DIR` that names a regular file differs by system. On Linux and macOS
no listing runs, and the "no repository" column answers. `89cb6289` answers
`git.info` so on a plain folder and on a repository. That holds with and without an
empty `GIT_CONFIG_KEY_0` (cells CfP0 and CfP3, Linux and macOS VMs). Other methods
are not measured on Linux and macOS. On Windows a
file with no `gitdir:` line gets the refusal of a config on the five methods:
`this repository's git config cannot be used: open <GIT_DIR>: not a directory`
(`5fd08069` in V-Wd of round `task039-02`, V-Wd2 of `task039-03`, Windows VM).
With an older git, a `.git` file to a gone git dir keeps the "no repository" frame of
`git.info` (cells CbP0 and CbP3, Linux and macOS VMs).

Two more states are not measured. The first is a daemon `GIT_COMMON_DIR`, where a
listing gets no `GIT_DIR=<null>`. The second is a `.git` file that names a regular
file, on Linux and macOS. The rest is from the code. In both
states `git.worktree_create` runs no listing in a folder where the trust check
finds no repository. `git.worktree_create` keeps the "no repository" column, and `git.worktree_remove`
keeps it unless its listing fails (from the code). In the second state `git.info`
and `git.list_branches` run no listing either, and they keep that column. With a
daemon `GIT_COMMON_DIR` those two methods skip the trust check, and the listing
classes below answer. In both states the listing of `git.status` still carries
`GIT_DIR=<null>`. A listing that fails there is the refusal of `git.status`, as in
a plain folder.

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
   refusal (rows L13wa to L13wr, see [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks)). Not measured: a
   daemon-created worktree under `worktreeRoot`, and `commondir` contents other than
   `x`. Rows L13xa and L13xb ran one repository layout only.
2. The listing exits 128, and its stderr starts with `fatal: not a git repository`
   in every measured row.
   Leading white space is dropped first, and letter case does not count (rows L14a,
   L14b, L14d, L14e and N01 to N04). The answer is "no repository". With a
   `branchName`, `git.worktree_remove` without `worktreeRoot` runs a second listing
   for the branch step and keeps the branch. A gone worktree then
   answers `{"success":true,"branchKept":true}`. Exit 1 with that
   text is not this class (row L14c). For a `.git` file to a gone git dir, git 2.56
   says `gitfile does not point to a valid repository`. A gone entry under
   `.git/worktrees` answers "no repository" on each method but
   `git.worktree_remove` (cells c01 to c04, c26 and c27). Another gone git dir
   answers the error of the call about the repository in the folder that holds
   `.git` (cells c09 to c12 and c18). Rounds `task039-01` to `task039-04` ran these
   on Linux and Windows VMs. With `"shadowDirOff":true` that git dir gets the hooks refusal
   on every method (rounds `task039-02` to `task039-04`, Linux VM).
3. Any other failure, a failed start included. The daemon runs `git version` with no
   `-c` option. It runs in `/` on Linux and macOS, and in the daemon's working
   directory on Windows. The call gets
   no `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>` or `GIT_CONFIG_VALUE_<n>` of the
   daemon. The call logs of the A rows show that on Linux and macOS VMs. Those of
   the P3 and P11 cells show it on a Windows VM. It gets no `GIT_CONFIG_GLOBAL` of
   the daemon either (cell C-c on Linux and macOS VMs, cells Wb on a Windows VM).
   No log shows a `GIT_CONFIG_SYSTEM` or a `GIT_CONFIG_NOSYSTEM` of the daemon.
   claustrum passes both on (not measured). claustrum matches these names by their
   exact upper-case letters. On Windows a lower-case name, as `git_config_global`,
   stays on the call (from the code, not measured). If that call fails too, the text is `git
   cannot run on this host; git not run: <exec error>: <git text>` (rows L11, L12
   and L15). Else it is the hooks refusal (rows L10 and L14c). With `worktreeRoot`,
   when git cannot start in `baseRepo`, `git version` gets the light environment
   (rows L16a and L16b, Linux VM). With `"shadowDirOff":true`, the `git version`
   call of `5fd08069` equals the call of claustrum after a failed first listing.
   It has the six heavy values for `git.info` and the four light values for
   `git.worktree_create`. Cell A5 of round `val-s3p2b1` shows that on Linux, macOS
   and Windows VMs, with a syntax error in the config of the repository. Cell A7
   shows it on a Linux VM, also for `git.list_branches`, with a git that exits
   128 in the listing. Without the member the call of a create carries the six
   values of a create call on `5fd08069` (cell A7, Linux VM). From the code:
   claustrum sends those six in the state `shadow`.

On Linux and macOS, git sometimes cannot start in the directory itself. That is a
file, or a folder that the daemon cannot search (mode 0600 or 0000). The listing
fails at the chdir. That is no hostile configuration, and the method goes on.
`89cb6289` still runs `git version` there, and claustrum does too. The frames do not
change (rows L16a, L16b, L16d and L16f). For a regular file, cells CeP0 and CeP3 show
the "no repository" frames on Linux and macOS VMs, with and without an empty
`GIT_CONFIG_KEY_0`. The cells are `git.info`, `git.list_branches`, `git.status` and
`git.worktree_create`. For a folder of mode 0000, cell C-h in phase P3 shows the
same frames for the last three methods on a Linux VM. The folder is a plain folder
in one run and a repository in the other.

If that `git version` fails too, the method answers `git cannot run on this host;
git not run: <exec error>: <git text>` with the detail of the version call.
`89cb6289` answers so in these rows and cells:

- `git.info` on a regular file and on a folder of mode 0000 (rows A-M2 and A-X1 in
  phase P11, Linux and macOS VMs).
- `git.list_branches`, `git.status` and `git.worktree_create` on a regular file. The
  create has the text in `error`, with and without `worktreeRoot` (cell C-g, Linux
  and macOS VMs).
- The same three methods on a folder of mode 0000, a plain folder and a repository
  (cell C-h in phase P11, Linux VM).

A path that does not exist keeps its frames there (rows A-M1 and A-M3 to A-M5 in
P11, Linux and macOS VMs). In both phases of cell C-h, `git.worktree_remove` without
`worktreeRoot` answers `failed to remove worktree: open <baseRepo>: permission
denied` (Linux VM). Not measured there: `git.worktree_remove` on a regular file,
and cell C-h on macOS.

On Windows a `git version` that fails is measured in these cells. On a plain folder
they are `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`
(cells P11-infoN, Wa-lbN, Wa-statN and Wa-crN). On a repository and on a regular
file the cells are `git.info` (cells P11-infoT, A-14 and Wa-infoF). All of them
answer the "cannot run" text. `git.info` on a path that does not exist keeps its
frame (cell Wa-infoM).

On Windows a path that exists and that git cannot start in is a refusal by class 3.
The start error of the listing is `fork/exec <git.exe path>: The directory name is
invalid.`. `<text>` is the hooks refusal with that error, and `<git.exe path>` is
the git that `PATH` resolves. `89cb6289` answers these cells on a Windows VM:

| cell | request | frame of `89cb6289` and claustrum |
|---|---|---|
| A-01, A-02 | `git.info`, `path` is a regular file in a plain folder or in a repository | `{"error":{"code":-32603,"message":<text>}}` |
| A-10 | `git.info`, `path` is a file symlink | the same |
| Wc-infoNUL, Wc-infoDot | `git.info`, `path` is `NUL`, or the name of a regular file with one more dot at its end | the same |
| A-13 | `git.info`, `path` is a folder with a full path of 296 characters | the same |
| A-03 | `git.list_branches`, `path` is a regular file | the same |
| A-04, A-04b2 | `git.status`, `baseRepo` is a regular file, and `path` is that file or a repository | the same |
| A-06 | `git.worktree_create`, `baseRepo` is a regular file | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}` |
| A-14 | the request of A-01, with a `git version` that exits 1 and writes `boom` | `{"error":{"code":-32603,"message":"git cannot run on this host; git not run: exit status 1: boom"}}` |

Nothing changes on disk in these cells. Other frames stay as they are on a Windows
VM. `git.status` with the file as `path` and a repository as `baseRepo` answers
`{"isRepo":false,"clean":false}` (cell A-04b1). `git.worktree_remove` with a
regular-file `baseRepo` answers `failed to remove worktree: open <baseRepo>: not a
directory` (cell A-05). It answers the same with an empty `GIT_CONFIG_KEY_0` (cell
We-P3-rmF). `git.info` answers
`{"isRepo":false,"repoSlug":"","defaultBranch":""}` in three more cells. The first
is a path that does not exist (cell A-07m). The second is a path under a regular
file (cell A-07s). The third is a junction whose target is gone (cell A-11).
With `GIT_CONFIG_COUNT=x` the count refusal answers the regular file (cell A-09).
From the code: if the listing does not start in a path, and `stat` reads the path
as not a folder, claustrum refuses. In cell A-13 claustrum
reads the folder as one that holds no repository, and the listing does not start
there.

One folder has an access entry that denies read data and traverse to the user of
the daemon. It keeps the `git.info` frame of a plain folder (cells A-12 and A-12r).
With a daemon token that has no privilege, `89cb6289` and claustrum refuse there.
The text is the
hooks refusal with `exit status 128: fatal: unable to access 'NUL/config':
Permission denied` (cell A-12n). The folder beside it with no deny entry keeps its
frame under that token (cell A-12nc). Two paths are not measured on Windows: a path
on a drive that does not exist, and a UNC path.

Some points are not measured. claustrum tests the class-2 text as a prefix, not as a
substring. Exit codes other than 1 and 128 are not measured. `git version` does not
get the daemon's own `LC_ALL` and `LANGUAGE` back. On Windows the daemon's working
directory and its socket folder were the same folder, so the two are not told apart.

The "cannot run" text carries the exec error and the stderr of `git version`, not
those of the listing. Row A-T1 shows it on Linux and macOS VMs, and cells P11-infoN
and P11-infoT on a Windows VM. There the listing exits 128 on an empty
`GIT_CONFIG_KEY_0`, and a stub `git version` exits 1. `89cb6289` answers `git cannot
run on this host; git not run: exit status 1: <stderr of git version>`. The
plain-folder rows A-N1 to A-N4 and A-S1 carry the same text on Linux and macOS VMs.
So do the rows of `git.worktree_remove` with `worktreeRoot` there: rows A-N5b and
A-N5c, and cell C-d with the worktree folder present and gone. Row A-N5 carries it
on the Linux VM only. Cells Wa-lbN, Wa-statN and Wa-crN show the plain folder on a
Windows VM.
One stderr line was measured. claustrum makes `<git text>` with the text rule below.

From the code: each listing of a git method that answers this text takes it from
the version call. Three such listings have no row with a `git version` that fails.
Two are the later listings of `git.status`: the one in the common directory and
the one in the temporary folder. The third is the listing of `git.worktree_remove`
with `worktreeRoot` for a `baseRepo` that git cannot start in.

The hooks refusal is `config-defined hooks could not be pinned off; git not run:
listing the configuration in force: <exec error>: <git text>`. claustrum makes `<git text>`
with the text rule of `git.worktree_create`. The rule takes git's raw stderr, keeps
its first 512 bytes, drops invalid UTF-8, turns each non-printable rune into one
space, and then trims spaces. If nothing is left, the text is the error of the git
process, for example `exit status 128`. A stub git on a Windows VM measured this frame against
`f6010b97`. The payloads were multi-line, CRLF, over 512 bytes, invalid UTF-8 and
control bytes. 40 newlines and 40 spaces before a long text showed that the cap
comes before the trim. The leading white space counts toward the 512 bytes. A tab
becomes one space on `90fca6e6` too (Linux VM). With the `GIT_COMMON_DIR` pin, git
names a corrupt config by its absolute path. `f6010b97` does the same in the
`git.info`, `git.list_branches`, `git.status`, create and remove frames, measured on
Linux and macOS VMs. `90fca6e6` names `.git/config`. On claustrum a syntax error in
a config file of the repository now gets the class 1 text first: see
[Repository config refusals](#repository-config-refusals).

#### A FIFO where the daemon reads a git file

This section covers three reads of the daemon itself: a `.git` file, the `commondir`
file of a worktree entry and the `gitdir` record of a worktree entry. On Linux and
macOS it opens each of them without blocking. It reads a regular file only, and
it reads that file as before. Anything else there, a FIFO for example, is no usable
file, and the read does not wait. Windows has no FIFO in the file system, and its reads
are as before.

The rows ran side by side against `89cb6289` on Linux and macOS VMs. No row opened
a FIFO for writing. `89cb6289` answered every row except B-G1 in under 2 s. T is a
repository, and `T/a` is a folder in it.

| Row | State | Request | Answer |
|---|---|---|---|
| B-F1 | `T/a/.git` is a FIFO | create with `baseRepo` `T/a` | success. The entry and the branch are made in T |
| B-S2 | `T/a/.git` is a symlink to a FIFO | the same create | as B-F1 |
| B-F4 | T lies inside an outer repository O, and `T/.git` is a FIFO | create with `baseRepo` T | success. The entry and the branch are made in O |
| B-L1 | as B-F1 | the create of B-F1, then a create in T with another leaf | both succeed |
| B-F5 | `T/.git` is a FIFO, and no repository lies above T | create with `baseRepo` T | `not_a_repo`, and nothing is made |
| B-S1 | `T/a/.git` is a regular file of 0 bytes | create with `baseRepo` `T/a` | `not_a_repo`, and nothing is made |
| B-F2 | as B-F1 | `git.info` and `git.list_branches` with `path` `T/a` | the answers of T |
| B-F2 | as B-F1 | `git.status` with `path` `T/a` and `baseRepo` T | `{"isRepo":false,"clean":false}` |
| B-F3 | a worktree of T under `T/a`, then `T/a/.git` becomes a FIFO | remove with `baseRepo` `T/a` | `{"success":true}`. The folder, the entry and the branch go |
| B-G2 | the `gitdir` record of another entry is a FIFO | remove of a real worktree of T | `{"success":true}`. The other entry stays |
| B-G3 | the `commondir` file of the entry of the worktree is a FIFO | remove of that worktree, without `worktreeRoot` | `{"success":true}`. The folder, the entry and the branch go |
| B-G4 | the `commondir` file of the entry of a linked `baseRepo` is a FIFO | create | the trust refusal with `is not a small plain file (commondir is not a regular file)` |
| B-G1 | the `gitdir` record of another entry is a FIFO | create with `baseRepo` T | no answer in 40 s |

From the code: in row B-G3 claustrum does not read the `commondir`, so it does not
verify the entry.

Row B-G3 ran without `worktreeRoot`. Cell N2 is that state with `worktreeRoot`, on
Linux and macOS VMs. `89cb6289` and claustrum give no answer in 40 s there, and
nothing is deleted. Each waits in its child `git worktree list --porcelain -z`.

The cells below ran side by side against `89cb6289` on a Linux VM, and in part on a
macOS VM. The Answer column is the frame and the disk state of `89cb6289`. In each
cell the frame and the files on disk of claustrum are equal to it.

| Cell | State | Request | Answer |
|---|---|---|---|
| N1 | the `gitdir` record of the entry of the worktree is a FIFO | remove of that worktree, without `worktreeRoot` | `{"success":true}`. The folder and the branch go, and the entry stays (Linux and macOS) |
| N9 | as N1, and the worktree is locked | the same remove | `{"success":true}`. The folder and the branch go, and the entry and its `locked` file stay (Linux and macOS) |
| N10 | the worktree is locked, and the `commondir` file of its entry is a FIFO | the same remove | the locked refusal, and nothing is deleted (Linux and macOS) |
| N12 | a second entry names the same worktree, and its `gitdir` record is a FIFO | the same remove | `{"success":true}`. The folder, the first entry and the branch go, and the second entry stays (Linux and macOS) |
| N3 | the `.git` file of T names its git directory and is padded with newlines to 2 MiB | `git.info`, `git.status` and create in T | `isRepo` false, and `not_a_repo` for the create (Linux and macOS) |
| N4 | the `commondir` file of the entry of the worktree is a regular file of 2 MiB | remove of that worktree | `{"success":true}`. The folder, the entry and the branch go (Linux and macOS) |
| N5 | `T/a/.git` is a socket | `git.info` and create with `T/a` | the answers of T, and the create succeeds (Linux) |
| N5b | `T/.git` is a socket, and no repository lies above T | `git.info` and create with T | `isRepo` false, and `not_a_repo` (Linux) |
| N6 | `T/a/.git` is a symlink to `/dev/null` | `git.info` and create with `T/a` | as N5 (Linux and macOS) |
| N6b | `T/.git` is a symlink to `/dev/null`, and no repository lies above T | `git.info` and create with T | as N5b (Linux and macOS) |

In row B-G1 `89cb6289` waits in its child `git worktree add`. A plain `git worktree
add` on that fixture waits too. From the code and a unit test: claustrum passes over
the FIFO record and starts the same call. With `timeoutMs` 3000 in the state of row B-G1, `89cb6289` and claustrum give no
answer in 40 s (cell N7, Linux VM). From the code: the limit of 2 minutes ends
that call.

Not measured:

- A git that refuses a FIFO named `.git` itself. The rows ran with a git that walks
  past it.
- A FIFO as `HEAD`, `index` or a ref file in a place that git itself reads. A FIFO
  as the config is in [Repository config refusals](#repository-config-refusals).
- A device file or a socket as a `commondir` file or a `gitdir` record. From the
  code: claustrum takes each as no usable file.
- A FIFO that a writer opens during the request. From the code: claustrum reads
  nothing from it.
- The `commondir` file of the git directory of `baseRepo` is a FIFO, in the read
  of `git.worktree_create` after `git worktree add`. From the code: claustrum
  takes that git directory as its own common directory. No request was built for
  this state. A git 2.47 gives no answer in 5 s to `rev-parse --git-dir` there
  (Linux host, not a VM).

#### Repository config refusals

Since `5fd08069` the daemon reads the config of a repository itself. If it does not
accept the config, or a file of the git directory, the request gets a
refusal. claustrum follows that build here. In the state `shadow` it makes the read
after the start test and after the call about the repository (see
[The settings copy](#the-settings-copy)). In the state `fallback-git` it makes no
read and gives no such refusal, as `5fd08069` (cell O4 of round `s3p2-oldgit`, Linux
VM). The rules below ran on a Linux VM. A rule for
one system names that system.

The five methods are the `git.*` methods. `<text>` is `<class>: <detail>`.

| Method | Answer |
|---|---|
| `git.info`, `git.status`, `git.list_branches` | `{"jsonrpc":"2.0","id":N,"error":{"code":-32603,"message":"<text>"}}` |
| `git.worktree_create` | `{"result":{"success":false,"error":"<text>","errorCode":"worktree_add_failed"}}`. Nothing is made |
| `git.worktree_remove` | `{"result":{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked: <text>"}}`. The worktree stays, and no branch is deleted |
| `git.worktree_remove` with `worktreeRoot` | `{"result":{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}}`. Nothing is removed |

Four states ran on all five methods (cells W7, X1 and V9). They are format version
`2`, a config of 20 MiB, an `info/exclude` of 20 MiB and a `shallow` file with a
text line. Most other cells ran `git.info` and `git.status`.

On `git.worktree_remove` the refusal comes for a worktree folder that does not
exist too, and the branch `branchName` stays. `5fd08069` answers so on a Windows
VM (cells F1a, F1b, F2a, F2b, G1a to G1c, W6, W6x and X12). It answers so on
Linux and macOS VMs too (cells N1). The N1 states are format version `2`, a config
of 20 MiB and an `info/exclude` of 20 MiB. Linux and Windows VMs ran them in a
bare repository too. With the two files of 20 MiB, `89cb6289` answers success
there and deletes that branch.

With `worktreeRoot`, `5fd08069` answers the text of the last table row (cells N14,
Linux and macOS VMs). The states were format version `2` and a config of 20 MiB.
Each ran with a worktree that exists and with a folder that is missing. With the
config of 20 MiB, `89cb6289` removes the worktree folder and its registration
there. In cells P4 (macOS VM) the worktree exists. An `info/exclude` of 20 MiB gets
that frame with its class 2 text. A syntax error in the config gets it with the
class 1 text of the syntax error. Nothing is removed in the two cells.

A path in quotes is the path of the file with the separators of the system, quoted
as Go's `%q` quotes it. A path of more than 300 characters is cut to its first 300
characters. The text has no mark of the cut (cells X12 and V12, also with
characters of two bytes). On Windows each backslash is doubled inside the quotes,
and the JSON encoding doubles it again. The path of the `open <file>:` text is not
in quotes, and it is not cut (cell V12, a path of 400 bytes). The `<worktreePath>`
of the remove text is as sent, and it is not cut.

On Linux and macOS VMs the path of the config is the resolved path. A request
through a symbolic link gets the real path in the text (cells V11). On a Windows VM
a request through a junction keeps the spelling of the junction. A request in
another letter case gets the real spelling (cells V11).

**The switch.** `"shadowDirOff": true` in the request turns the read off for that
request, on the five methods (cells X1 and V2). A git request that starts while it
runs has the switch too (cell C9 of round `task073-01`, Linux VM). The request then gets the answer
that it had before this rule. A FIFO as the config then gets no answer in 10 s. The
member name matches without regard to letter case: `"SHADOWDIROFF": true` is the
switch too. The values `"true"`, `1`, `null` and `false` are as no member. Cells V3
measured both facts on `git.info` and `git.status`, with format version `2`. Cells
V2 and V3 ran on Linux, macOS and Windows VMs, and the FIFO on Linux and macOS VMs.
With `worktreeRoot`, the switch and a config of 20 MiB, `5fd08069` removes the
worktree (cell P4, macOS VM).

**The read.** claustrum gives each config file to git on standard input, with one
call for each file: `git config --no-includes --file - --list -z`. The call log of
`5fd08069` shows that call. git follows no include in it. claustrum follows each
`include.path` setting itself. If the request names a linked worktree, claustrum
reads the config of the repository of that worktree.

From the code: claustrum runs that call in the root directory on Linux and macOS,
and in the working directory of the daemon on Windows. The environment of the call
is that of the cleaned daemon without `GIT_CONFIG`, `GIT_CONFIG_PARAMETERS`, `LC_ALL` and
`LANGUAGE`. Then come `GIT_DIR=<null device>`, `LC_ALL=C` and `LANGUAGE=C`. On Linux and macOS claustrum opens each file
of the read with the flag that gives the daemon no controlling terminal. In cell
V17 the config is a link to a pty slave. If the pty master closes, `89cb6289` and
`5fd08069` stay up (Linux VM).

**The kept verdict.** On `5fd08069` a second `git.info` on a repository with an
accepted config makes no such call (cells B4 and V19). A request after a change of
the config makes one (cell B4). With a refused format version, a later request of
`5fd08069` makes the read again (cells N10 and P5, Linux, macOS and Windows VMs).
claustrum keeps the verdict of a read that accepted the config, for 256 git
directories at most. A verdict with a refusal is not used again: each later request
reads again, as on `5fd08069`. If each file that an accepted read used is
unchanged, a later request starts no such call. A file is unchanged if
its size, its modification time and its mode are the same, and its identity where
the system gives one. A file that was missing must still be missing. This rule and
the number 256 are claustrum's own. Cell B4 ran one repeat and one changed config.
The tests of `shallow`, `info/attributes` and `info/exclude` run in each request
and are no part of the kept read. On `5fd08069` a change of one of those files
gives no config read (cells 19a to 19c of round `val-s3p2b2-3`, Linux VM).

From the code: the identity test is Go's `os.SameFile`. On Linux and macOS that is
the device and the inode of the file. This page claims no identity test on Windows.
These tests do not see these changes:

- A change that keeps the size, the time, the mode and the identity. One example
  is a rewrite in place with the same size inside one step of the file time.
- A change during the read, after the daemon recorded the state of that file.
- A change of a file whose path the daemon cannot examine. Such a path has the
  state of a missing file.

claustrum does not keep a verdict if an open or a read of the daemon ended with an
error in that read. So a refusal with an `open <file>:` text is read again at the
next request. An error of the daemon itself, such as too many open files, then
answers one request only. That rule is claustrum's own.

**Class 1** is `this repository's git config cannot be used: <detail>`. The format
version and the extensions of an included file are not tested (cells X7 and X9).

| Detail | When |
|---|---|
| `the repository uses format version "<v>", which this version of Claude does not support` | The last value of `core.repositoryformatversion`, as git lists it, is not exactly the text `0` or `1`. A config with no such key is not refused. `<v>` is cut to its first 20 characters, with no mark. A value of 20 characters is whole, and one of 21 shows 20 (cells P1, Linux, macOS and Windows VMs) |
| `the repository uses git extension "<name>", or a value of it, that this version of Claude does not support` | The format version is `1`, and an `extensions.<name>` setting is refused. `<name>` is in lower case. It is cut to its first 60 characters, with no mark. A name of 60 characters is whole, and one of 61 shows 60 (cells P1, Linux, macOS and Windows VMs). Under version `0` none of the settings that ran is refused |
| `"<file>" includes "<value>", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full` | An include value of the form `~user/…` or `%(prefix)/…`, in an `include` section or in an `includeIf` section. The condition of the `includeIf` section does not have to match (cells N12, Linux VM). `<file>` is the file that holds the setting. `<value>` is cut to its first 100 characters (cells V12, Linux and macOS VMs) |
| `its includes go past what Claude follows (16 files, nested 3 deep)` | More than 16 included files, or an include chain of more than 3 levels below the config. A missing file counts, and so does the file of an `includeIf` section, also a missing one (cells V8 and N7). A file that includes itself gets this text too |
| `"<file>" is not a regular file` | The config or an included file is a FIFO or a directory. A config that is a symbolic link to the null device gets it. So does an included file that is a symbolic link to a FIFO. `include.path = ~` gets it with the path of the home folder (cell N13, Linux VM). So does a `config.worktree` of the git directory with `extensions.worktreeConfig` on (cells C8, V1, V2, R2 and R3 of rounds `task007-01` to `task007-03`, Linux and macOS VMs). The daemon's own read does not wait on a FIFO |
| `"<file>" is larger than 8 MiB` | The config, an included file or that `config.worktree` has more than 8388608 bytes (cells X6 and V7, and R7 of round `task007-03`) |
| `"<file>" has a setting in section "<section>" whose name is longer than 1024 bytes` | The full name of a setting, with its section and subsection, has more than 1024 bytes. The pair 1024 and 1025 is measured in `core`, `remote`, `lfs`, `url`, `filter`, `extensions`, `include` and `includeif`. Cells N6 measured it in `index`, `feature` and `checkout` too (Linux, macOS and Windows VMs). `<section>` is in lower case, without the subsection. The bound holds in an included file, with the path of that file. Cells V7 measured that one level below the config, and cells N8 two and three levels below it |
| `"<file>" has more settings than Claude reads (8 MiB); remove repeated ones` | See "The count of settings" below |
| `open <file>: <reason>` | The daemon cannot open the config, an included file or that `config.worktree` (cell R4 of round `task007-03`). A file of mode 000 gives `permission denied`. A socket gives `no such device or address` on a Linux VM. As the config, a socket gives `operation not supported on socket` on a macOS VM |
| `<error lines of git>` | The listing of the config exits non-zero while a child holds its stderr. The lines read until then are joined by one space. One `git version` call follows (cells B1e-x1 of rounds `task111-09`, `task111-10` and `task111-14`, Linux, macOS and Windows VMs) |
| `fatal: bad config line <n> in "<file>"` | The config or an included file has a syntax error. So does the `config.worktree` file of the git directory with `extensions.worktreeConfig` on (cell C3b of round `tier0-14`, Linux VM). It comes before an extension refusal (cell E3 of round `tier0-16`, Linux VM). The text is that of git, with the path in quotes |
| `"<included file>" is outside "<repo>", and on Windows Claude reads a repository's settings only from inside it; if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead` | Windows: a relative include that leaves the repository. For a bare repository `<repo>` is its git directory (cell W6) |
| `"<file>" includes "<value>", and on Windows Claude follows an include only if its path is relative and stays inside the repository; if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead` | Windows: an absolute include value, also to a missing file, `~/x` and `~root/x`. An absolute value and `~/x` get it in an `includeIf` section whose condition does not match too (cells N12w). `<value>` is cut to its first 300 characters. A value of 300 characters is whole, and one of 301 shows 300 (cells P7w, Windows VM) |
| `"<file>": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to` | Windows: the config is a file link. Windows, every git method that reads the config: the git directory is a junction or a directory symbolic link, and `<file>` is `<git dir>\config`. |

The path of an included file is the folder of the file that holds the setting, one
separator, and the value as written. On a Windows VM `path = <dir>/inc.cfg` gives
`<git dir>\<dir>/inc.cfg` in a text (cells V12c and V12d). The folder of the
holding file stays as written too. With `include.path = a/../b/inc.cfg`, a
`path = x.cfg` in that file names `<git dir>/a/../b/x.cfg` (cell N13-b, Linux and
macOS VMs).

Under format version `1` claustrum refuses an extension other than
`worktreeConfig` when [the copied settings file](#the-copied-settings-file) has
no rule for its key or does not accept its value. `5fd08069` refuses
`preciousObjects = 1234567890`, `objectFormat = sha512`, `refStorage` with no
value and `partialClone = a/-b` (cells R1 to R4 of round `tier0-14`, Linux VM).
Cells V6 (Linux, macOS and Windows VMs) measured these settings as not refused: `worktreeConfig = maybe`, `objectFormat = SHA256`, `refStorage = FILES`,
`preciousObjects = yes` and `preciousObjects = 1`. An extension in a config with no
format version key is not refused either. This rule does not refuse a sha256 repository or a reftable repository.

No refusal comes from a config that is missing, empty, a dangling symbolic link
or a symbolic link to a regular file. A NUL byte in a value, an empty
`include.path` and one with no value give none either. On Linux the daemon follows a relative include, also one that
leaves the git directory, an absolute include and `~/x` with a home folder.

Two kinds of file get the kind test and the size test, with their own path in the
text. A format version `2` in such a file is not refused. No other content of it
ran. The filter settings of an `includeIf` file count for the filter rule (cell
F1b of round `task084-02`).

- The file of an `includeIf` section, whatever its condition is. The conditions
  that ran are `gitdir:` that matches and that does not match, `gitdir/i:`,
  `onbranch:` and `hasconfig:` (cells X9 and V8, Linux, macOS and Windows VMs).
- The `config.worktree` file of a linked worktree. The config sets
  `extensions.worktreeConfig` to `true`, `yes`, `1` or `TRUE`, or has that key with
  no value (cells X2 and V14). `on` and `ON` start the test too, and `false`, `no`
  and `0` do not (cells N4, Linux, macOS and Windows VMs). `Yes`, `On` and `tRuE`
  start it too (cells P2, the same three systems). The trust check of the request finds that linked worktree. On `git.status` that is the worktree that `baseRepo` names. Cells V14
  ran `git.info`, `git.status`, `git.list_branches` and `git.worktree_create`
  (Linux, macOS and Windows VMs). On the macOS VM the values other than `true` ran
  `git.info`. Cell W14 ran `git.worktree_remove` on a Linux VM.

**The count of settings.** Each setting of a counted section costs the bytes of its
full name, the bytes of its value, and 96. The config has its own sum, and the
included files have one sum between them. The file in which a sum passes 8388608
is refused. Cells X5 found five counted sections: `core`,
`remote`, `extensions`, `include` and `branch`. Cells V5 found seven more: `lfs`,
`index`, `url`, `filter`, `feature`, `checkout` and `includeif`. The sections `x` and `user` do not count. This
arithmetic is fitted to the measured bounds. The exact pairs are in `core` and `remote`. For each other counted
section one cell of 200000 lines was refused (cells X5 and V5). An included file
of 200000 `core.filemode = true` lines is refused with its own path (cells V7).
Cells N5 ran files of 40000 `core.filemode = true` lines on Linux, macOS and
Windows VMs. One such included file passes. Two are refused, and the text names
the second one. The config with 40000 such lines and one such included file
passes. Cells P3 ran files of 30000 such lines on the same three systems. Two
included files pass, and three are refused. A file that includes a second one
passes. A chain of three, one below the other, is refused. In the two refused
cells the text names the third file.

**Class 2** is `a file in this repository's .git directory cannot be used: <detail>`.
The files are `shallow`, `info/attributes` and `info/exclude`, and the git
directory itself.

| Detail | When |
|---|---|
| `"<file>" is not a regular file` | `info/attributes` or `info/exclude` is a FIFO or a directory. A symbolic link there passes, also one to the null device. `shallow` is a FIFO, a directory or a symbolic link to the null device. The daemon does not wait on a FIFO |
| `"<file>": it is larger than 4 MiB` | `info/attributes` or `info/exclude` has more than 4194304 bytes |
| `"<file>" is larger than 4 MiB` | `shallow` has more than 4194304 bytes |
| `open <git dir>: not a directory` | On Windows a daemon `GIT_DIR=.git` names the `.git` file of a linked worktree, on the five methods (cells c19 to c22 of round `task039-01`, V-RM of round `task039-02`, Windows VM) |
| `open <file>: <reason>` | `shallow` has mode 000 (`permission denied`) or is a socket (`no such device or address`, cell V10, Linux VM). The path is not in quotes. An `info/attributes` or `info/exclude` of mode 000 is not refused |
| `"<file>" holds something other than object ids` | A line of `shallow` is not 40 or 64 hex characters in lower case. That rule is fitted to the cells. Refused are a text line, an id in upper case, and a blank line before or after an id. So are a line of 39, 41 or 63 hex characters and an id with a space after it. A file that holds one line end is refused too (cells V10). A line end of CRLF and a last line with no newline pass. An empty file passes |

**Two refused files.** A refused config comes before a refused file of the git
directory. In class 2, `shallow` comes before `info/attributes` (cells V16, Linux,
macOS and Windows VMs). `info/attributes` comes before `info/exclude`. Cells P6
ran that pair four ways on a Linux VM: a file of 20 MiB with a FIFO, two files of
20 MiB, two FIFOs, and a FIFO with a directory. Two more P6 cells have a refused
`shallow` with a FIFO as one of the two other files, and the text names `shallow`.

**No refusal.** A folder with no repository keeps its answers. `5fd08069` keeps
the "not a repository" answers for a `HEAD` that is no ref and no object id, also
with format version `2` (cells X3). It keeps them for a repository of another
owner with no `safe.directory` too. Cells V1 ran that owner on Linux, macOS and
Windows VMs. The refused states were format version `2`, a config of 20 MiB and an
`info/exclude` of 20 MiB. A FIFO as the config ran on Linux and macOS VMs. Which call of that build
decides is not measured.

In the first state claustrum finds no repository, so it reads nothing. The second
state is decided by the call about the repository, on `5fd08069` and on claustrum.
That call comes before the read. If git answers it with exit 128 and a no-repository
text, the method gives its "not a repository" answer and reads no config. git's text for
another owner is `detected dubious ownership in repository`. In cells X9 of round
`val-s3p2b2-5` a repository of another owner has format version `3` or a syntax
error. Each request of `5fd08069` there is that one call, and a remove makes it two
times (Linux and macOS VMs). An answer of git that names a problem is not kept. So
a new `safe.directory` line counts at the next request, as on `5fd08069` (cell
N15-a, Linux and macOS VMs).
[The settings copy](#the-settings-copy) has that call.

claustrum asks git no question about the owner beyond that call. With
`"shadowDirOff":true` and in the state `fallback-git` it makes no read, so no
refusal comes there.

**Where claustrum differs from `5fd08069`.** A line with "from the code" ran on no
VM. Rounds `val-s3p2b2-3` and `val-s3p2b2-5` ran a build with the settings copy.

- On git 2.11.4 and 2.12.5, `5fd08069` gives no refusal in the four fixtures of
  cells V18 (Linux VM). The fixtures are format version `2`, a config of 20 MiB, a
  FIFO as the config and an `info/exclude` of 20 MiB. Git 2.13.7 is the oldest git
  that ran with the refusal. Those two gits give the state `fallback-git`. From the
  code: claustrum makes no read in that state. No claustrum build with that state
  ran on those gits.
- In some states git itself refuses a value of the config, and `5fd08069` gives
  the answer of a repository, because its copy leaves the value out. A claustrum
  build with the copy answers as `5fd08069` in cells C2c, X5b, X9-03 and X9-04
  (round `val-s3p2b2-3`) and in the 48 X8 cells (round `val-s3p2b2-5`, Linux VM). Cell C2c has `extensions.objectformat = md5` under version `0`.
  Cells X9-03 and X9-04 have an `include.path` with an empty value or with none.
  Four V6 cells of the older rounds did not run on a build with the copy (not
  measured). Two are `worktreeConfig = maybe` and `objectFormat = SHA256` under
  version `1`. One is `refStorage = FILES`, and one has no version key and
  `objectFormat = md5`. For `objectFormat = SHA256` the `git.status` of `5fd08069`
  answers `exit status 128`.
- After a change of the owner inside the 5 seconds of a kept good answer,
  `5fd08069` and a claustrum build with the copy answer normally. The first
  request after that time answers "not a repository" (cell 31 of round
  `val-s3p2b2-5`, Linux VM).
- A config that is replaced 50 times each second (cell E4 of round
  `val-s3p2b2-5`). `5fd08069` makes 4 config reads for
  each `git.info` and 10 for each `git.status`. claustrum makes 1 for each (Linux
  VM, one run). The reply lines are equal.
- A config with a very long key, or one key 200000 times. On a Windows VM 15 config
  read calls of `5fd08069` have no end record in cells E1a, E2a and E2b. The same
  calls of claustrum end with exit 0 (rounds `val-s3p2b2-3` and `val-s3p2b2-5`).
  The reply lines are equal.

**claustrum's own choices** (from the code, not measured on `5fd08069`):

- `extensions.worktreeConfig` starts the `config.worktree` test with values
  that no cell ran, such as `oN` or `007`. The last such setting of the config counts.
- Under format version `1` the value rule refuses extension values that no cell
  ran, such as `preciousObjects = -1`.
- The cuts of the format version value and of the extension name count
  characters. The cells have ASCII text.
- The `config.worktree` open error and size bound hold on `git.status`,
  `git.list_branches` and `git.worktree_remove` too, and every kind on Windows.
- The name bound and the count of settings are tested on a whole file before an
  include of that file is followed. That order is fitted to two cells of 200000
  include lines of a missing file, which get the "more settings" text.
- The two include limits come before the open of an included file.
- An open error of the file of an `includeIf` section gives no refusal.
- An open error other than the measured ones gets the same `open <file>: <reason>`
  form.
- A `~/x` include is joined to the home folder and cleaned.
- On Windows a `%(prefix)/…` value gets the "expands neither" text, with the cut
  at 100 characters.
- The read runs after the trust check accepted the git directory, and after the
  test of the daemon's own `GIT_CONFIG_COUNT`.
- `~/x` with no home folder gets the "expands neither" refusal. No daemon starts
  with no home folder.
- On `git.worktree_remove` with `worktreeRoot`, the read comes after the trust
  check and before the configuration listing. Cells N14 and P4 have a repository
  that passes the checks before it.
- The listing that the read takes from git for one config file has a bound of
  64 MiB. A file with a long subsection name and many keys lists as far more
  bytes than it has. The read tests the name bound and the count of settings
  on the first 64 MiB of such a listing. If neither refuses, the read has no
  verdict, and the request gets no refusal.
- If the read gives no verdict, the method goes on as before. A read gives none
  if its git call fails with anything other than a syntax error text, the stop
  at 30 seconds ([Time limits of git calls](#time-limits-of-git-calls)) or the
  error lines above, such as a git that does not start. A failed call with no
  holder of stderr is claustrum's own case (not measured). So does a config file that the
  daemon opens and cannot read.
- With a daemon `GIT_COMMON_DIR`, `git.info` and `git.list_branches` read
  nothing: the trust check pins no git directory there.
- A bare repository whose config the daemon refuses, on a host where git refuses
  bare repositories (`safe.bareRepository = explicit`), gets the refusal.
- An `includeIf` line in a file three levels below the config gets the "includes
  go past" text.
- On Windows claustrum refuses a link at a config file and at an included file, and
  a junction or a directory symbolic link at the git directory itself. It tests no other folder above the file. It also refuses an include value that starts
  with a slash or a drive.

#### The settings copy

Since `5fd08069` the daemon keeps its own copy of the git settings of a repository.
The copy is in a folder `git-shadow` beside the socket. The git calls of the five
methods name that copy, so git reads the copy and not the config of the repository.
claustrum follows that build.

##### The states and `settingsControl`

`git.info` reports the state in its last member, `settingsControl`.
No other method sends it, and an error reply has none
(cell A1, round `s3`, Linux, macOS and Windows VMs).

| `settingsControl` | When | What it changes |
|---|---|---|
| no member | No start test of the daemon gave a state yet | Nothing. A folder with no repository, or a `HEAD` that is no ref, starts no test (cell E5, round `s3p2`, Linux VM). The `.git` file below is the exception |
| `"shadow"` | The start test passed | The git calls name the copy. Before them the daemon asks git about the repository |
| `"fallback-git"` | The git of the host does not work on such a copy. `5fd08069` reports it on git 2.11.4 and 2.12.5 (round `s3p2-oldgit`, Linux VM) | The git calls name the git directory. The daemon reads no repository config, gives no config refusal, makes no copy and does not ask git about it |
| `"off-switch"` | A request with `"shadowDirOff":true` runs (the hold) when the reply is made | A request that starts then names the git directory, with no test, folder or config read |

- After the test the "no repository" reply has it too:
  `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`.
  That reply waits for a running test and has its state, or no member after
  a failed test (cells T2, T3 and T8, round `task103-01`, Linux VM). So does
  a missing path (cell T7d, round `task103-06`). On a folder with no repository
  the other methods answer at once (cells T5 to T6b).
- As the first git request of a daemon, a `git.info` on a folder whose `.git`
  file names a missing path runs the test and gives that reply.
  A missing entry under `worktrees` of an existing git directory runs no test, and
  the reply has no member (`5fd08069`, cells V3 and V4, round `task083-01`,
  Linux, macOS and Windows VMs). A missing entry in a missing
  repository runs the test (cell V4g, round `task083-02`, Linux VM). After the
  test on such a file `5fd08069` runs a `rev-parse` against `.probe` in its run
  folder, and has it at the reply. claustrum runs a config listing in its place
  and makes no `.probe`, with equal frames (cell V3, round `task083-03`, macOS
  and Windows VMs).
- A `git.info` on a path that does not exist starts no test. As the first git
  request of a daemon it makes one call, the excludes read, and its reply has no
  member. A later one makes none. No process starts for it (cell T7s of round
  `task107-01`, Linux VM).
- With no git in the daemon's `PATH`, a `git.info` on a repository answers "no
  repository" with no test (`5fd08069`, cell C7, round `tier0-5`, Linux VM). Its
  member follows the daemon's state (not measured).
- A `git.info` on a regular file gets the gate of the repository of its folder.
  Outside any repository it starts no test. As the first git request of a daemon
  its reply has no member on Linux and macOS. On Windows it is the hooks
  refusal, with `fork/exec <git.exe>: The directory name is invalid.`
- A request with the member gets `"off-switch"` also as the first request of a
  daemon, in a "no repository" reply and while a failed test is kept (cells A1
  and B1 of round `s3`, cell 32 of round `s3p2b-edges`, Linux VM).
- A hold that starts during a `git.info` moves to the git directory each
  command after its first config listing and the next call, the Windows root
  pair too (cells O2 of `task103-02` and `task103-04`, Linux, macOS and Windows VMs).
  After a hold a later command runs the test and takes the copy (cell O1).
  `git.list_branches` (cells L0 to L2, Linux VM), `git.status` and the
  worktree methods (`task105-01` to `task105-10`, Linux and macOS) do the same.
  Windows ran C1, C2, C0, C4 and C5 (`task105-05`, `task105-07`). A
  call about the repository follows a later test (R2, C4, C5), except before a
  `--git-dir=` call (S2, C3). A create in a hold makes no copy, and `git.info` answers `"off-switch"` (cells H4 and H1, round `task073-02`).
- In that time `git.status`, `git.list_branches` and `git.worktree_remove` get no
  config refusal, as with the member. On a repository of format version 3 they
  answer `{"isRepo":false,"clean":false}`, `{"isRepo":false,"branches":[]}` and the
  lock-check text that ends `(its registrations could not be examined); retry`.
  The remove deletes nothing there because git itself fails (cells Q1 to Q3 of
  round `task073-05`, Linux VM). On a config that only the daemon refuses, the
  remove deletes, as on `5fd08069` (cells Q10 and Q11, round `task073-08`, Linux
  VM). macOS and Windows are not measured.
- A held request keeps no hold for later requests. No build stopped at 2
  minutes its `worktree add` of 130 s whose holder answered first (cell Q4 of
  round `task073-05`, Linux VM). A held call that starts after that answer has the
  2 minute limit (claustrum's own, not measured).
- Only the JSON value `true` counts for `shadowDirOff`. `"true"`, `1`, `null` and
  `false` are as no member (cell B3, round `s3`, Linux VM, and cell S1-seq of
  round `val-s3p2b2-1`, macOS VM).
- In the state `fallback-git` the test runs again on the first request more than 10
  minutes after the last test. On `5fd08069` 599.8 s gives no test and 605.5 s gives
  one (cell 28, round `s3p2b-edges`, Linux VM). In the state `shadow` no second
  test ran up to 720 s.
- The daemon logs one `[GitHandler]` line at the first decision of the state and
  one at a change.

##### The start test

The first request that reaches a repository, or the `.git` file above, without the member runs the test. It is
10 git calls in a folder `.check-<n>` of the daemon, and that folder is gone at the
reply. If the test fails, the request gets this text:

`Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.`

`git.info`, `git.status` and `git.list_branches` answer
it as a `-32603` error. `git.worktree_create` answers it in `error`, with the
`errorCode` `worktree_add_failed`. `git.worktree_remove` answers it after `failed
to remove worktree: could not check whether <worktreePath> is locked: `, with no
`errorCode`. `89cb6289` answers those requests normally.

A failed call 7 is followed by one `git version` call. If it fails too, the text is
`git cannot run on this host; git not run: <exec error>: <git text>` alone, in the same frames. The
exec error and the text are those of `git version`. A remove deletes nothing there.
`5fd08069` does so in cells E1 to E5 of round `task083-01` (Linux VM), and E1 and
E2 on macOS and Windows VMs. On Windows that `git version` runs in the working
folder of the daemon.

| `<cause>` | When |
|---|---|
| `git <verb> failed under "<folder of the daemon>": <error lines of git, space-joined>` | A test call other than call 7 failed. Go's `%q` quotes the folder |
| `git <verb> failed under "<folder>": signal: killed` | The daemon ended a test call other than call 7 after 30 seconds (Linux and macOS VMs). On a Windows VM the end is `exit status 1` |
| the same with ` (git version <v>)` at its end | The failed or ended call was one of calls 1 to 5 (measured for `init` and `mktree`) |
| `this repository's git config cannot be used: <error lines of git>` | Call 7 failed, and the plain `git version` after it works (cells E9 and E9c, round `task083-01`, Linux VM) |
| `mkdir <path>: <reason>` or `open <path>: <reason>`, with no quotes | A folder or the lock file of the daemon cannot be made |

A call 6 with exit 1 goes on (cell C3). `real` at call 6, or a wrong answer at calls 8
to 10, gives `fallback-git`, as does git's refusal when that call without the copy
works (cells C1, C2, C3b, C4 and C5). At call 10, `No space left on device` in the
error or an end by a signal fails the test (cells N1 to N3). Two failed calls
name the first (cell C9). All cells: round `tier0-5` on `5fd08069` (Linux
VM).

- The daemon keeps a failure for 10 seconds from the end of the failed test. A
  request in that time gets the kept text with no git call.
- The kept failure holds after its cause is gone. A request with
  `"shadowDirOff":true` works in that time.
- A test call that a wrapper holds 29 s passes. One held 32 s gives the error at
  about 31 s (cell 31, round `s3p2b-edges`, Linux VM).
- Call 7 with pipes held past 30 s ends near 31 s (cell B2 of
  rounds `task101-01` and `task101-02`, Linux VM). The cause is `this repository's git config cannot be
  used: git did not finish reading "<config of the test repository>" within 30
  seconds; try again`. With stderr alone held, a call 7 that exits 1 gives the
  lines read before the cut (not measured).

##### The folders

The folder of one daemon is `<socket folder>/git-shadow/<host form>-<pid>-<16 hex>`.
`<host form>` is the host name with each character outside `A-Z`, `a-z`, `0-9` and `.`
written as `_`. A `-` becoming `_` and a kept `.` are measured (cell C6, round `s3`,
and `.local` on a macOS VM in round `task075-01`). The other characters are not
measured. On Linux and macOS
that folder and each folder of a copy have mode `0700`, and the files of a copy
have `0600`. The daemon makes the folder at
the first request that runs the test. It holds an exclusive lock on the file `lock`
in it for its whole life. On Windows the hold is an open handle with no sharing.

- At `-stop` and at `server.shutdown` the daemon removes its folder. The empty
  `git-shadow` stays (round `s3p2`, Linux, macOS and Windows VMs).
- If the folder is removed while the daemon runs, the next request makes it again
  with another hex part (cell B6, round `s3`, Linux and macOS VMs).
- The folder of one repository is `<folder of the daemon>/<32 hex>`. The name is
  the first 32 hex digits of the sha256 of the path of the git directory. A linked
  worktree uses the folder of its main repository.
- That folder holds `HEAD`, `config`, the empty folders `objects` and `refs`,
  and `info/exclude` where the repository has it. On Linux and macOS it holds a link `worktrees` to the
  `worktrees` folder of the git directory. On Windows it has no such entry.
- More copies follow the state of the repository: `shallow`, `info/attributes`,
  `rebase-merge/head-name`, `rebase-apply/head-name` and `BISECT_START`.
- The daemon makes the folder at the first request that accepts the config of the
  repository. After a change of a file, a later request writes its copy again.
- In the state `fallback-git` the short-lived entries of a request are in the
  folder of the daemon too. The light calls of a create carry six values there
  (cell Q-S4c, round `val-s3p2b2-3`, Windows VM).
- One request also keeps short-lived entries in the folder of the daemon:
  `.gitdir-<n>` for a `git.status`, and `.index-<n>` and `.worktreeinclude-<n>` for
  a create. Also `.config-<n>` for a create in a repository with a
  `config.worktree` file, and `.s<n>` on Windows. With `"shadowDirOff":true` the
  first three are in the temporary folder, as `claude-ssh-gitdir-<n>`,
  `claude-ssh-index-<n>` and `claude-ssh-worktreeinclude-<n>`.
- On Windows the daemon makes the short-lived folders of a request by their
  full path (from the code). Cell X3 denies new folders in its folder, with the
  backup and restore privileges on, after a first `git.info`. There a create
  succeeds on both builds with byte-equal replies (round `task077-06`, Windows
  VM). With both privileges off both builds fail alike at `.s<n>` (cell X3c).
  The folders `.config-<n>`, `.worktreeinclude-<n>` and the `.gitdir-<n>` of
  `git.status` are not measured there (claustrum's own).

If the folder of a repository cannot be made, the request gets this text in the
three shapes of the start test:

`Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>`

The cause is the `mkdir` error of the folder.
The same request again gets the same line with no git call.
On Windows a held copy gets 2 s of retries, then
`rename <temp file> <copy>: Access is denied.` with full paths (`5fd08069`, round `task009-01`).

The request that makes the folder of the daemon also sweeps `git-shadow`. An
entry goes if its lock file can be taken and its name has the host form of this
host. An entry with another name goes only when it is old: 8 days goes and 6 days
stays (cells G7b and G7c). The bound of 7 days is not measured. A held
lock keeps the folder of a live daemon. `5fd08069` does the same in cells G1 to G12 of
round `task075-01` (Linux VM). Cells G1, G3 to G10 and G12 give the same outcomes on a macOS VM.
In round `task075-02` claustrum gives the outcomes of `5fd08069` in each cell that
ran, except at a link. Those are every cell on the Linux VM and the cells above on
the macOS VM. On the Windows VM they are G1, G3, G7, G7b, G7c, G8 and G12.
On Linux and macOS the age is read after the lock step, so a folder with no lock
file gets one and stays. On Windows the age is read before, and such a folder of
8 days goes (cell G7, Windows VM). On Windows the held lock keeps the folder of a
live daemon from a remove by hand. So no later request sweeps again (cell G12 of
round `task075-02`, both builds). A lock error other than a held lock keeps the
entry (claustrum's own, not measured). A folder with no lock file that is less than 1
minute old stays and gets none (claustrum's own, not measured). A planted link, or a junction on Windows, goes
as a link, and claustrum makes no lock file in its target
([D8](DIVERGENCES.md#d8)).

The folder of the daemon keeps 128 repository folders. A new repository removes
the folder with the oldest use that no git call holds (EL2 cell 45, Linux VM). On
`5fd08069` a held folder stays. With all 128 held, a request gets this text
and nothing goes (E2L cells 37 and 36, Linux VM):

`Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again`

On `5fd08069` a folder goes between git calls of a request, never during one.
It comes back without a new config read (E3L cell 54, Linux VM).
claustrum holds a call's folder until its pipe wait ends (not measured). A
call remakes a gone or partial folder, except right after its listing. Only the call fails
if its folder went, or its daemon shut down or replaced it. A failed remake
answers its own text. On `5fd08069` (cell V8W2, round `task013-01`, Linux VM)
a new repository gets the text above while a config listing waits for
its pipes, and no folder goes. A listing puts `listing the configuration in force: ` before it
(E3L cell 54 and EL2 cell 47, Linux VM, EW cell 46, Windows VM):

`Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again`

##### The copied settings file

The `config` of the copy is the daemon's own text. Each carried setting is one
block of two lines, `[<section>]` and `<key> = "<value>"`, in the order of the file
of the repository. The last `core.bare` of `config.worktree` follows, then the
`[url]` blocks of rewritten URLs. The file ends with
`[lfs]\n\trepositoryformatversion = 0\n`.

- `gitshadowconfig.go` gives 41 keys one rule of accepted values each. Cells C1
  to C5 of round `tier0-14` measured what they show (Linux VM). The rest is not
  measured.
- `remote.<name>.url`, `.lfsurl`, `.promisor` and `.partialclonefilter`,
  `lfs.<url>.access` and `branch.<name>.remote` are carried too. The length of a
  rewritten URL is not measured.
- Every other key is dropped, such as `core.hooksPath` and `user.*`.
- Outside the version and extension refusals, a value that its rule does not accept is left
  out with no refusal. One example is
  `core.filemode = maybe`. `89cb6289` answers "no repository" or the hooks refusal there (cell A3, round `s3p2`,
  Linux, macOS and Windows VMs).
- Where the copy carries a value that git refuses, `git.info` answers "no
  repository" with the member. One example is `extensions.objectformat = sha256`
  in a sha1 repository (cell A3, round `s3p2`, the three systems).
- A setting of an `[include]` file is carried at the place of the include line.
  Its format version, `extensions.*`, `core.bare` (cell C4, round `tier0-14`)
  and `filter.*` (not measured) are dropped.
  A setting of an `includeIf` file is not carried.

##### The call about the repository

In the state `shadow` the daemon asks git about the repository, before the
config read and the calls of the method. The call is `rev-parse --git-dir` with two
`-c` items, in the root of the work tree. It names a folder `.probe` of the daemon.
It drops the daemon's `GIT_NAMESPACE`, `GIT_INDEX_FILE`, `GIT_DISCOVERY_ACROSS_FILESYSTEM`
and `GIT_ALTERNATE_OBJECT_DIRECTORIES`, and keeps its `GIT_CONFIG_COUNT` pairs (round
`tier3-02`, the three systems). So a pair that git reads to find the repository, such as
`safe.directory`, changes these answers (from the code, not measured).
The daemon writes an emptied
`.probe/config` again (`5fd08069`, cell C10, round `tier0-5`, Linux VM). A good answer is kept 5 seconds from the end of the call.
A change of the root folder or of its `.git` entry makes the daemon ask again.

| Answer of git | Reply |
|---|---|
| exit 128 with a text that starts with `fatal: not a git repository` in any letter case, or that holds `detected dubious ownership` or `cannot use bare repository` | The "no repository" answer of each method. No config is read, and no copy is made. `5fd08069` answers so in cells P5 to P7, P9 and P10 of round `task073-03` (Linux VM). git 2.56's text for a `.git` file to nowhere, `unsafe repository`, and the two phrases at other places of the text get the same answer (claustrum's own, not measured) |
| exit 1, exit 128 with no text, a signal (`signal: terminated`, cell P13) or a git that cannot start (cell W2-280) | `git could not check this repository on the remote machine: at "<root>": exit status <n>` |
| exit 1 or exit 2 with a text, or exit 128 with another text | the same text with git's text in place of `exit status 1`. Two lines stand there with one space between them (cell P3, round `val-s3p2b2-7`, and cell N-f, round `val-s3p2b2-8`, Linux VM). `warning: w` before `fatal: not a git repository` gets this reply (cell P8, round `task073-03`). `5fd08069` answers so on all five methods (cells Q5 to Q7, round `task073-05`). So does `fatal: error reading '<repo>/.git'` (cell W2-268, round `task073-01`, Windows VM) |
| either of the two rows above | One `git version` call follows. If it fails, the reply is `git cannot run on this host; git not run: <exec error>: <git text>` in the frame of each method. A remove makes two such calls and deletes nothing. `5fd08069` answers so after `fatal: bad config line 3 in file <home>/.gitconfig` on `git.info` (cell E8, round `task083-01`, Linux VM). It answers so on the other four methods (cell E8m, round `task083-02`) and after an exit 1 (cell P14x, round `task073-03`). On `git.status`, `git.list_branches` and `git.worktree_remove` it answers so after an exit 128 with no text and after a SIGTERM (cells Q8 and Q9, round `task073-05`). Those two on `git.info` and `git.worktree_create`, a git that cannot start, and a remove with `worktreeRoot` are not measured |
| another path than the git directory, in two calls | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again`. `<path>` is the path as git printed it: with forward slashes on Windows (table P2 of round `val-s3p2b2-7`, Windows VM) |
| no answer in 30 seconds | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` |

The three texts have the shapes of the start test. A wrapper answers the call in
cells 37 to 39 of round `s3p2b-edges` (Linux VM). Cell 40 has the 30 seconds on
macOS and Windows VMs. A bad answer is not kept, and a
remove asks twice. For a repository of another owner
with no `safe.directory`, `git.worktree_remove` answers the lock-check refusal that
ends `(its registrations could not be examined); retry`.
With a child that holds the pipes, an exit 0 counts by its output so far (`git.status`,
cells P1 to P3 of round `task100-01`, Linux VM). The other four methods and macOS and
Windows are not measured.

One `git version` call also follows a config read that failed with a syntax error (cells 39 and
`x9-syntax-own` of round `val-s3p2b2-5`, Linux VM).

##### The git calls

In the state `shadow` each hardened call carries `GIT_COMMON_DIR=<copy>` and
`GIT_OBJECT_DIRECTORY=<git directory>/objects`. A call with no `--git-dir=` in its
arguments also carries `GIT_DIR` and `GIT_WORK_TREE`. The four come right after the
profile values, in that order (cell B3a, round `s3p2`, Linux, macOS and Windows
VMs. The order is from Linux and macOS VMs). [Hardened git calls](#hardened-git-calls)
has the other points of a call.

##### `config.worktree`

The copy has no `extensions.worktreeConfig` setting, so git reads no
`config.worktree` file in the state `shadow`. The daemon does two things in its
place, in a repository with that extension and a `config.worktree` file.
`worktreeConfig` values `2` and `10` turn that extension on (cells C3c and K1 of rounds
`tier0-14` and `tier0-16`, Linux VM).

- Each key of the copied settings file but the format version, `extensions.*` and
  `core.bare` goes before the option list of the git calls as one `-c` item, with
  the value of the copy, sorted by key. Cells C3a of `tier0-14` and W1 of
  `tier0-16` show six keys (Linux VM). Other keys and a repeated key are not
  measured. A call on a linked worktree gets the items of its own file.
- A new worktree of `git.worktree_create` gets its own `config.worktree` file. See
  [git.worktree_create](protocol/git-worktree-create.md).

##### Where claustrum differs from `5fd08069`

In the differences that stay the reply lines are equal, except in two Windows
rows of a create whose `.git` becomes a file.

#### Hardened git calls

The git methods run most git steps as hardened calls. A hardened call carries a fixed
set of `-c` options and a fixed set of environment variables. This shape reaches a
reply or the disk in at least four places. One is the `git.status` answer for a
replaced `HEAD` commit (see [git.status](protocol/git-status.md#what-the-status-reads)). The three others are in
`git.worktree_create`. Its failure texts have no graft-file hint. A tree path of
270 characters creates on Windows. The records of a new worktree hold absolute
paths.
A logging git wrapper measured
the points below. The option lists and the variables are those of `5fd08069`. In
the state `shadow` the folder variables name the settings copy (see
[The settings copy](#the-settings-copy)). With `"shadowDirOff":true` and in the
state `fallback-git` a call carries `GIT_COMMON_DIR=<git directory>` alone. See "Not
equal to `5fd08069`" below for what differs. Each point names its build and the VMs
that measured it.

- Working directory. Each hardened call runs with the repository directory as its
  working directory, and it passes no `-C`. The checkout of `git.worktree_create`
  runs in the new worktree. The `git status` call runs in the worktree. Both name
  their directories with `--git-dir` and `--work-tree`. Linux, macOS and Windows VMs.
- Profiles. A call uses the light profile or the heavy profile. Each profile has its
  own `-c` options and its own variables. The light variables are, in this order,
  `GIT_ALLOW_PROTOCOL=https:ssh`, `GIT_TERMINAL_PROMPT=0`, `GIT_NO_REPLACE_OBJECTS=1`
  and `GIT_GRAFT_FILE=<null>`. The daemon's own `GIT_ALLOW_PROTOCOL` changes the
  first value (see the next section). The heavy variables are, in this order,
  `GIT_NO_LAZY_FETCH=1`, `GIT_NO_REPLACE_OBJECTS=1`, `GIT_GRAFT_FILE=<null>`,
  `GIT_ALLOW_PROTOCOL=denied_by_claude_ssh`, an empty `GIT_ASKPASS` and
  `GIT_TERMINAL_PROMPT=0`. `5fd08069` sends the four and the six in these orders
  with `"shadowDirOff":true` (cell B1, round `s3-linux`, Linux VM). Cells A1 and
  A3 of round `val-s3p2b1` show the same two orders with the member on Linux and
  macOS VMs. The Windows logs of `5fd08069` are sorted by name. They show the same
  values and no order (cell B1, round `s3-windows` and cells A1 and A3 of round
  `val-s3p2b1`, with the member).
  `89cb6289` sends four heavy variables: it has no `GIT_NO_REPLACE_OBJECTS` and no
  `GIT_GRAFT_FILE` there. Without the member, in the states `shadow` and
  `fallback-git`, the light calls of a create and their listings carry six values.
  The three listings of the branch step of a remove carry them too.
  Cell Q-S4c of round `val-s3p2b2-3` shows the values in the state `fallback-git`
  (Windows VM). The checkout and its listings keep the four light values. Then comes
  `GIT_COMMON_DIR` when the
  trust check pins it. In the state `shadow` that is the copy, with
  `GIT_OBJECT_DIRECTORY`, `GIT_DIR` and `GIT_WORK_TREE` after it. Then come the hook pins, from `GIT_CONFIG_COUNT`: two base
  pins, and two for each hook name of the listing before the call (see below). The
  daemon's own `GIT_CONFIG_COUNT` moves them (see the next section). The
  `GIT_COMMON_DIR` pin and the hook pins were measured against `f6010b97` on Linux,
  macOS and Windows VMs.
- Heavy calls. The hardened calls of `git.info`, `git.list_branches` and
  `git.status` use the heavy profile. So does each `rev-parse --absolute-git-dir`,
  and the `--git-dir=<dir> --work-tree=<dir> rev-parse --show-toplevel` call with
  its listing. Cells B0 and B1 of round `s3-linux` show that on `5fd08069` for the
  three methods (Linux VM). B1 has `"shadowDirOff":true` and B0 has no such member.
  With the member, cell B1 of rounds `s3-macos` and `s3-windows` shows it for
  `git.info` and `git.list_branches` (macOS and Windows VMs). Cell G4-off-hook of
  round `s3p2-macos` shows it for the calls of `git.status` and for the pair (macOS
  VM). Cell B1 of round `s3-windows` shows it for the pair and for the `hash-object`
  and `status` calls of `git.status` (Windows VM). Without the member, cells B3d and
  B3e of round `s3p2-linux` show the origin verify and `rev-parse --short HEAD`
  (Linux VM). With the member, cell A2 of round `val-s3p2b1` shows both calls with
  the heavy profile (Linux, macOS and Windows VMs). Cell A6 of that round shows the
  pair in `git.worktree_remove`, with the member (Linux, macOS and Windows VMs).
  There the registration record names the worktree through a link, and the pair
  runs three times. `89cb6289` runs
  `git.info`, `git.list_branches` and that `rev-parse --show-toplevel` call with the
  light profile. The other hardened calls of claustrum use the light profile, except
  the calls of [the branch step](#the-branch-step), which have their own variables.
  Of the claustrum calls, only the `status`, `ls-files` and `diff-index` calls of
  `git.status` add `GIT_OPTIONAL_LOCKS=0`. The checkout adds `GIT_INDEX_FILE`.
- Option lists. The heavy profile has 28 `-c` items and the light profile has 20,
  as on `5fd08069`. Cells B0 and B1 of round `s3-linux` show both lists (Linux VM).
  Cell 50 of round `s3p2b-edges` and cell B1 of rounds `s3-macos` and `s3-windows`
  show them on macOS and Windows VMs. `89cb6289` sends 23 and 13. The heavy list is
  `core.fsmonitor=false`, `core.hooksPath=/dev/null`,
  `core.attributesFile=/dev/null`, `core.excludesFile=<file>`,
  `core.longpaths=true`, `protocol.allow=never`, the same for `ext`, `fd`, `file`,
  `git`, `ssh`, `http` and `https`, `credential.helper=`, `core.askPass=`,
  `fetch.bundleURI=`, `core.alternateRefsCommand=`, `alias.remote-https=`,
  `alias.remote-http=`, `alias.remote-ssh=`,
  `alias.remote-denied_by_claude_ssh.command=`, `help.autocorrect=0`,
  `submodule.recurse=false`, `fetch.recurseSubmodules=false`,
  `push.recurseSubmodules=false`, `gc.auto=0`, `maintenance.auto=false` and
  `core.commitGraph=false`. The light list is `core.hooksPath=/dev/null`,
  `core.fsmonitor=false`, `branch.autoSetupMerge=false`, `fetch.bundleURI=`,
  `http.saveCookies=false`, `core.alternateRefsCommand=`, the four `alias.remote-`
  items, `help.autocorrect=0`, `core.excludesFile=<file>`, the three submodule
  items, `gc.auto=0`, `maintenance.auto=false`, `advice.graftFileDeprecated=false`,
  `worktree.useRelativePaths=false` and `core.longpaths=true`. The checkout adds
  `core.splitIndex=false` and `core.commitGraph=false` after the light list. The
  `rev-list` call of the branch step adds `core.commitGraph=false`.
- Not equal to `5fd08069`. Two sets of rounds compared the calls of claustrum with
  those of `5fd08069`, request by request. Round `val-s3p2b1` ran the build of the
  option lists, in its cells A1 to A7 and B1 to B9. Rounds `val-s3p2b2-3` and
  `val-s3p2b2-5` ran a build with the settings copy, without the member. Not
  each cell ran on each system. The Windows log is sorted by name, so the order of
  the variables has rows on Linux and macOS VMs only. The first four points say
  what is equal. The points after them are the differences that stay. The reply
  lines are equal in each of those.
    - Equal with the member. A call that both builds make is equal in list,
      arguments, working directory and variables (Linux, macOS and Windows VMs).
      The order of the variables is equal too (Linux and macOS VMs). That holds
      for the calls of `existingBranch`, of `sourceBranch` and of the
      `.worktreeinclude` step (cell A3, Linux, macOS and Windows VMs). It holds
      for a create and a remove with `worktreeRoot` (cell A4, Linux and macOS
      VMs). Windows refuses `worktreeRoot` before any such call. Round
      `val-s3p2b1` measured this point. The rounds of the settings copy sent no
      member.
    - Equal without the member, in the state `shadow`. The folder variables, the
      six values of the light calls of a create, the `lfs.storage` item and
      `LANGUAGE=C` of the checkout, and its second listing. Also the ten test
      calls, the call about the repository and the names of the short-lived
      entries (round `val-s3p2b2-5`, Linux and macOS VMs).
    - Equal in the state `fallback-git`: the names of the short-lived entries and
      the variables (cell Q-S4c, round `val-s3p2b2-5`, Windows VM).
    - Equal: the top folder pair. After `rev-parse --absolute-git-dir`, a
      create runs the `--git-dir=<dir> --work-tree=<repo> rev-parse
      --show-toplevel` call with its listing, also with the member. It runs
      when `<git dir>/worktrees` is a real folder and no entry
      names the leaf. Cells: R-Q2b of `task084-01` (Linux, macOS, Windows
      VMs), A3 of `val-s3p2b1`, and the C6 and E cells of `task109-01/-02` (Linux
      VM). `fallback-git` runs none (not measured).
    - Stays, with `worktreeRoot`: `5fd08069` runs one round of six calls
      before the add, after the add and after the checkout. claustrum runs it
      before the add only. The round is `rev-parse --show-toplevel`, `rev-parse
      --absolute-git-dir` and `worktree list --porcelain -z`, each after its
      listing. That is cell A4 of round `val-s3p2b1` (Linux and macOS VMs). Without the
      member the create has 31 calls on `5fd08069` and 18 on claustrum (cell
      `n3-root` of round `val-s3p2b2-5`, Linux VM).
    - Stays, with `worktreeRoot`: a second remove of a worktree that is gone. It
      has 12 calls on `5fd08069` and 8 on claustrum. The four more are two
      `--git-dir=<dir>` pairs (cell `n3-root` of round `val-s3p2b2-5`, Linux VM).
    - Stays, with `existingBranch`: `5fd08069` runs two listings in a row
      before the attach add, and claustrum runs one (cell A3, Linux, macOS and
      Windows VMs). After a failed attach add, `5fd08069` runs the `-b` add with no
      listing before it. claustrum runs one listing there (cell B2, Linux, macOS
      and Windows VMs). Both rounds show it.
    - Stays: the read-tree of claustrum carries filter items before its list in
      some cells with a git filter or with git-lfs.
    - The names of the short-lived entries with the member are from the code and
      its test: `claude-ssh-gitdir-<n>`, `claude-ssh-index-<n>` and
      `claude-ssh-worktreeinclude-<n>/patterns`. `5fd08069` has those names in
      cells A1 and A3 of round `val-s3p2b1` (Linux, macOS and Windows VMs).
- Graft hint. With `GIT_GRAFT_FILE` set, git can print 8 `hint:` lines about
  `info/grafts` on standard error. The light list turns them
  off with `advice.graftFileDeprecated=false`. The heavy list does not. On
  `5fd08069` and on claustrum, three calls of `git.status` print the hint in a
  repository with no `info/grafts` file. They are `status`, `diff-index` and
  `rev-parse --verify -q HEAD^{commit}`. `89cb6289` prints no hint there (cell B8
  of round `val-s3p2b1`, Linux and Windows VMs). With an `info/grafts` file of one line, the
  three calls print the hint on each of the three builds (cell B5, Linux and
  Windows VMs). The `status`, `ls-files`, `rev-parse` and `diff-index` calls of
  `git.status` give stdout only to the reply. A failure of one of them answers
  the exec error. So no reply line of cells B5 and B8 holds a `hint:` line
  (Linux, macOS and Windows VMs).
  These cells ran with and without `"shadowDirOff":true`.
- Reply lines against `b027ede`, the build before the option lists. Among the
  requests of round `val-s3p2b1` that both builds ran, the replies differ in cells
  B1, B2, B4 and B9 only. `git.status` has cell B1. Cell B7 ran the cells of groups A and B of round `s3p2` on
  both builds. No reply line differs: 65 requests on a Linux VM, 76 lines on a
  macOS VM and 41 requests on a Windows VM.
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
  variables, and in the state `shadow` the six values of a create call. It carries
  no hook pins. One listing is the exception, in the state `shadow`: the second
  listing before the checkout. It carries the pins, `GIT_INDEX_FILE` and
  `LANGUAGE=C`, and no `LC_ALL`. `5fd08069` sends it so in cell A3 of round
  `val-s3p2b1` (Linux, macOS and Windows VMs). The listing before the checkout and the
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
  configuration key is longer than the listing reads` (row L08c). Row L08c had the
  key in the section `x`. In some sections a key over 1024 bytes is refused before
  the listing (see [Repository config refusals](#repository-config-refusals)). More than 1024
  names get `config-defined hooks could not be pinned off; git not run: too many
  configured hooks to pin` (row L06b). Names whose bytes add up to more than 65536
  get `…git not run: configured hook names exceed the aggregate pin byte bound` (row
  L07b). A value has no cap: 2 MiB passes (row L09). Linux, macOS and Windows VMs.
  Some points are not measured. Only `command` and `event` were used as `<var>`.
  claustrum pins two names that differ in letter case as two names. It counts a
  name under two keys once in the limits. It tests the count before the byte sum.
  A sum over 65536 made of names of at most 1024 bytes was not run. Nor was a value
  over 2 MiB. Every measured failure hit the first listing. No row has a listing
  over a limit in a folder that holds no repository. From the code, three rules
  follow. A later listing that runs right before a hardened call does not refuse
  that call. If it fails or exceeds a limit, the call gets the two base pins only.
  A failure of one of the two later listings of `git.status` refuses, and a limit
  does not refuse there (see [git.status](protocol/git-status.md#errors)). In a folder that holds no repository, the first listing follows
  "The daemon's own git environment": a limit refuses each method except
  `git.status`.
- Hooks refusal check. The first listing of a method is the hooks refusal check. It
  is not an extra call. If it fails, the method answers by the class of the failure
  (see [Git-directory trust check](#git-directory-trust-check)). If it exceeds a
  limit, the method answers the limit refusal (see Hook pins above).
  `git.worktree_create` runs it with the light profile. `5fd08069` does so with
  `"shadowDirOff":true` (cell B1 of rounds `s3-linux`, `s3-macos` and `s3-windows`,
  on Linux, macOS and Windows VMs). Without the member the listing of `5fd08069`
  carries six values (cell B0, round `s3-linux`, Linux VM). That of claustrum
  does too in the state `shadow` (round `val-s3p2b2-5`, Linux VM). `git.info`,
  `git.list_branches`, `git.status` and `git.worktree_remove` run it with the heavy
  profile. `5fd08069` does so in cells B0 and B1 of round `s3-linux` (Linux VM).
  With the member, cell B1 of round `s3-windows` shows the same for the four
  methods (Windows VM). With `worktreeRoot`, `git.worktree_remove` runs a light check.
  Later a heavy listing and `rev-parse --absolute-git-dir` follow. Linux VM.
- Excludes read. Before its first listing, an uncached request runs `git config
  --includes --path core.excludesFile`. Two concurrent requests run two (rounds
  `task103-01`, `task103-03`, Linux VM). It adds only `GIT_DIR=<null>`, in the
  temporary directory. Linux, macOS and Windows VMs.
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
  added value and place win. Linux and macOS VMs. On Windows the block of a git
  child of claustrum is in name order. `f6010b97` and `89cb6289` do not sort it
  (Windows VM, row V17h for `89cb6289`). That is
  [`DIVERGENCES.md`](DIVERGENCES.md) → D23.
- Temporary names. [The folders](#the-folders) of the settings copy names the
  short-lived folders of a request. With `"shadowDirOff":true` the git dir of
  `git status` starts with `claude-ssh-gitdir-`, and the folder of the include
  step with `claude-ssh-worktreeinclude-`. `5fd08069` has those two names in cells A1
  and A3 of round `val-s3p2b1` (Linux, macOS and Windows VMs). The temporary index
  directory of the
  checkout starts with `claude-ssh-index-`, as on `89cb6289`. Cell X1 shows that
  name on Linux and macOS VMs. It is in the frame text
  `open <tmp>/claude-ssh-index-<n>/index: no such file or directory` and in the
  `GIT_INDEX_FILE` of each read-tree. The suffix is decimal digits. The name of
  `89cb6289` on Windows is not measured, and claustrum uses the same prefix there.
- `git.worktree_remove` with `worktreeRoot`. A light `rev-parse --show-toplevel`
  follows the light check, with no listing of its own. Before the daemon decides
  whether `worktreePath` is a registered worktree, it runs a light
  `worktree list --porcelain -z` and a heavy `rev-parse --absolute-git-dir`. Each
  of them has its listing. claustrum runs the `rev-parse` after it located the
  worktree folder. It uses the answer only as the `--git-dir` of the two
  calls that follow. For a `baseRepo` that exists, the "leads into"
  and "not reachable" refusals come right after `worktree list`.
  With the excludes read that is 7 calls, as on `f6010b97` and `89cb6289`. For a
  missing `baseRepo` and a missing root, both references send "not reachable" with
  no git call, and claustrum makes none there too (row T9). Linux and macOS VMs.
- `git.worktree_create` with `worktreeRoot`. After the repo test and the in-repo
  root test, claustrum runs a light `rev-parse --show-toplevel`, a heavy
  `rev-parse --absolute-git-dir` and a light `worktree list --porcelain -z` in
  `baseRepo`. Each has its listing. With the excludes read and the repo test that
  is 9 calls. `f6010b97` makes the same 9 calls before it refuses a root in a
  checkout. `89cb6289` also makes 9 calls, in this order. claustrum does not use the
  answer of `rev-parse --absolute-git-dir`. Linux and macOS VMs. A failed `show-toplevel`,
  or two failed `worktree list` calls, stop the create with `mkdir_failed` (cells
  C-W17b and C-W17).

#### The daemon's own git environment

A logging git wrapper measured the rules below against `f6010b97`.

The excludes read and the `--attr-source` probe get the daemon's environment as it
is, plus their own additions (`5fd08069`, rounds task004-01 and task004-02, Linux,
macOS and Windows VMs). The repository calls are
the listings, the hardened calls, `worktree add`, the checkout and `git status`.

`5fd08069` and claustrum clean the daemon's `PATH`, `GIT_EXEC_PATH`, `GIT_SSH` and
loader variables for the repository and start-test calls. Start-test calls also
get no daemon `GIT_` name (rounds task004-01 and task004-02, Linux, macOS and
Windows VMs). On `5fd08069` the `git.info` branch and the repository test of a
create come from stdout (round task004-01, cells M1 and M4, Linux VM). claustrum
reads every parsed value so (from the code).

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
  `ls-files` and `diff-index` calls follow them. Values: Linux, macOS and Windows VMs. Order: Linux and macOS
  VMs.
- A hardened call gets no other `GIT_CONFIG_KEY_<digits>` or
  `GIT_CONFIG_VALUE_<digits>`. A leading-zero index such as `GIT_CONFIG_KEY_01` is
  removed too. Linux, macOS and Windows VMs. A listing keeps `GIT_CONFIG_KEY_01` in
  its place (Linux and macOS VMs). Other names, such as `GIT_CONFIGX` or
  `GIT_CONFIG_S4`, reach git. Linux, macOS and Windows VMs.
- The daemon's `GIT_ALLOW_PROTOCOL` sets the light value. Unset, it is
  `https:ssh`. Else the daemon splits it on `:` and keeps each
  entry that is exactly `https` or `ssh`, once, in its order. The match is
  case-sensitive, with no trim. With no entry left, it is
  `denied_by_claude_ssh`. The heavy value stays `denied_by_claude_ssh`. Linux,
  macOS and Windows VMs. On Windows a lower-case `git_allow_protocol` sets it
  too (Windows VM).

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
  Linux, macOS and Windows VMs. `f6010b97` and `89cb6289` also refuse `git.info` and
  `git.list_branches` on a plain-directory `path`, and `git.worktree_create` and
  `git.worktree_remove` on a plain-directory `baseRepo`, on the same VMs. So does
  claustrum.

With a key that git cannot read, git fails each configuration listing with exit
status 128. `<text>` is then `config-defined hooks could not be pinned off; git not
run: listing the configuration in force: exit status 128: <git text>`. The git of
the Linux, macOS and Windows VMs gave three texts. An empty key gives `error: empty config key
fatal: unable to parse command-line config`. The key `nodot` gives `error: key
does not contain a section: nodot fatal: unable to parse command-line config`.
The key `a.b c` gives `error: invalid key: a.b c fatal: unable to parse
command-line config`. A good pair before the broken pair changes nothing. `89cb6289`
answers these rows in a folder that holds no repository. The rows are from Linux
and macOS VMs. The cells are from a Windows VM, with an empty key. The same cells of
P5, P6 and P7 hold the other two keys and the good pair.

| row | Windows cell | request | frame of `89cb6289` and claustrum |
|---|---|---|---|
| A-N1 | P3-infoN | `git.info` on a plain folder | `{"error":{"code":-32603,"message":<text>}}` |
| A-X2 | P3-infoX | `git.info` on a folder with an empty `.git` folder | the same |
| A-N2 | P3-lbN | `git.list_branches` on a plain folder | the same |
| A-S1 | P3-statN | `git.status` with a plain-folder `baseRepo` | the same |
| A-N3, A-N6 | P3-crN, without `worktreeRoot` | `git.worktree_create` with a plain-folder `baseRepo`, without and with `worktreeRoot` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, and no folder is made |
| A-N4 | P3-rmN | `git.worktree_remove` without `worktreeRoot`, plain-folder `baseRepo`, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}` |
| A-N5 (Linux VM only), A-N5b, A-N5c | none | `git.worktree_remove` with `worktreeRoot`, plain-folder `baseRepo` | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}` |

A `GIT_CONFIG_GLOBAL` that names a file git cannot parse fails each listing too.
With the content `[broken`, `<git text>` is
`fatal: bad config line 1 in file <file>`. `89cb6289` answers the hooks refusal
there in three requests (cell C-c, Linux and macOS VMs). They are `git.info` on a
plain folder and on a repository, and `git.worktree_create` on a plain folder.
Cells Wb show the same three frames on a Windows VM, with a file that fails at
line 2. The `git version` of `89cb6289` gets no `GIT_CONFIG_GLOBAL`
there and exits 0.

Nothing changes on disk in these rows. Some frames stay as they are with nothing
set. A path that does not exist keeps its frames (rows A-M1 and A-M3 to A-M5, and cell
P3-infoM for `git.info` on a Windows VM). So does a plain folder of mode 0000, where
git cannot start (row A-X1). `GIT_CONFIG_COUNT=0`, an empty value and
`GIT_CONFIG_PARAMETERS='x` change no frame (Linux, macOS and Windows VMs). On
Windows, `git.worktree_create` with `worktreeRoot` answers as in row A-N6 (cell
Wf-P3-crNroot). With nothing set it answers `not_a_repo` (cell Wf-P0-crNroot).
`git.worktree_remove` with `worktreeRoot` is not measured on Windows for this
check. From the code: claustrum refuses a `worktreeRoot` there before the check.
Row A-N4 of `git.worktree_remove` is pinned in `gitlisting_norepo_test.go`.

No row measures a listing over a limit of "Hook pins" in a folder that holds no
repository. That is a listing that passes and holds too many hook names, or a key
over the size limit. From the code, the methods differ there:

- `git.info`, `git.list_branches` and `git.worktree_create` answer the limit
  refusal in their "refused" frames.
- `git.worktree_remove` answers the limit refusal as `<text>` of its two frames in
  the table above, without and with `worktreeRoot`.
- `git.status` answers `{"isRepo":false,"clean":false}`. It refuses in that folder
  for a listing that fails, and for no other listing.

A `.git` file with no `gitdir:` line is not measured either. From the code: its
listing carries `GIT_DIR=<null>`, so the folder answers as a plain folder does.

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
| `git.list_branches`, `path` not empty and does not resolve, or inside a managed worktrees directory | `{"isRepo":false,"branches":[]}`, before the check and with no git call. |
| `git.status` | the same, also for a `path` that is a plain dir, the repository, missing or a file, and for a plain-dir `baseRepo`. Linux, macOS and Windows VMs |
| `git.status`, `baseRepo` does not resolve | `{"isRepo":false,"clean":false}`, before the check and with no git call. See [git.status](protocol/git-status.md#baserepo). From the code: a `baseRepo` in a managed worktrees tree answers the same way. That case is not measured with a refused count |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, also for a plain-dir or missing `baseRepo` and a relative `worktreePath`. Also for a `worktreePath` with a `..` component, outside the repository or that exists. A missing `branchName` gets its `-32602` frame first. A `baseRepo` inside a managed worktrees directory gets its `nested_base_repo` frame first. Linux, macOS and Windows VMs. On Windows the check comes before the `worktreeRoot` refusal (Windows VM). A `baseRepo` that fails claustrum's own trust-root test gets that frame first too. Only the Windows VM measured that order (round 1 row C1 P1) |
| `git.worktree_remove` | Some refusals come first, with their usual frames. These are for a relative path, a path outside the repository, the repository itself and a `..` component (Linux, macOS and Windows VMs). They are also for a path outside `worktreeRoot`, and with `worktreeRoot` for a `..` `baseRepo` (Linux and macOS VMs). A `baseRepo` that fails claustrum's own trust-root test gets the managed-worktrees refusal first. Only the Windows VM measured that order (round 1 row A1 P1). Without `worktreeRoot`, `open <baseRepo>: not a directory` comes first for a regular-file `baseRepo` (Linux, macOS and Windows VMs). So does `statat .claude: permission denied` for a `baseRepo` with mode 0600 and a path under `.claude` (Linux and macOS VMs). With `worktreeRoot`, so does the refusal of a root in `baseRepo` (row E7, Linux and macOS VMs) |
| `git.worktree_remove`, no `worktreeRoot`, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`, also for a plain-dir or missing `baseRepo`. Linux, macOS and Windows VMs |
| `git.worktree_remove`, no `worktreeRoot`, worktree directory present | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`. Linux, macOS and Windows VMs. Linux and macOS VMs also measured a `baseRepo` of `<T>/missing/..`, which does not resolve. On Windows that `baseRepo` gets the managed-worktrees refusal first |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` a repository or a plain dir | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}`. A trust refusal puts its own text there instead. Linux and macOS VMs |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` missing, absolute and without a `..` component, worktree directory present | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (the repository at <baseRepo> could not be read); retry"}`. Linux and macOS VMs |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` missing, absolute and without a `..` component, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`. If the root is missing too, the "not reachable" refusal comes first (row T9). Linux and macOS VMs |

On Windows the `worktreeRoot` refusal of `git.worktree_remove` comes before the
check (Windows VM).

#### Time limits of git calls

On `git.list_branches`, `git.info`, `git.status` and `git.worktree_create` most
calls end at 2 minutes (`5fd08069`, round `task010-01`, Linux, macOS and Windows
VMs). Config listings before a git call have a 30 second limit (cells T10 and TA).
So do the repository config read (`git.status`, Linux VM) and each
`config.worktree` listing (cells TW and TS, round `task010-04`, Linux VM).
A create with the filter rule and no settings copy runs claustrum's own
`--show-scope` listing, with the 2 minute limit and the `timeoutMs`. A stopped call of the top folder pair stops the create with the stop
text (claustrum's own).
The scan of
ignored files ends at 10 minutes (Linux and macOS VMs). Read-tree checkouts have
no such limit.

- The stop is SIGTERM, then SIGKILL 2 s later. On Windows the process ends at
  once. Config file listings get SIGKILL alone, not the listing before the
  read-tree call (Linux VM, cell A4x).
- Before a good add no call follows a stopped call. A stopped add removes an
  empty leaf. A stopped repository test of a create answers the stop text alone,
  with `worktree_add_failed` (cell TI). After a good add the rollback runs its own
  calls (cells TA and TW). A stopped scan copies nothing, and the create succeeds.
- While a `"shadowDirOff":true` request runs, no daemon call stops at 2
  minutes (cells T8 and T9). Then claustrum's own limit is 30 minutes.
- `timeoutMs` does not end the add (cell T6), nor, with `"shadowDirOff":true`,
  the listing before the checkout (cell T12).
- With no `timeoutMs` a reply waits 2 s after git exits for a child that holds
  its output and gets no signal. Listings before a call and the `status` scan
  wait until stdout closes, A1 and A2 past the limit (A1 to A4 of round
  `task101-01`, both pipes held, Linux VM). Held past 30 s, the config read on standard input ends near 31 s (B1), and
  `config.worktree` reads too (not measured). A holder of stderr alone delays a
  config listing about 2 s and that read 1 to 1.5 s (A1-e and B1-e of rounds
  `task111-01` and `task111-02`, Linux and macOS VMs).
- `git.worktree_remove` gets none of these limits. Its first call has the 30 s bound
  of the call about the repository (cell TR, round `task010-02`, Linux VM).

#### git.info
`{path[,shadowDirOff]}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"[,"settingsControl":"<state>"]}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""[,"settingsControl":"<state>"]}`

- `settingsControl` is the last member, since `5fd08069`. Its values are
  `"shadow"`, `"fallback-git"` and `"off-switch"`. Before a start test of the
  daemon gave a state, there is no such member. See [The settings copy](#the-settings-copy).

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
- A repository config that the daemon does not accept answers `-32603` with a
  refusal text. See [Repository config refusals](#repository-config-refusals).
- A failed start test, a bad answer of the call about the repository, and a copy
  folder that cannot be made answer `-32603` with their text. See
  [The settings copy](#the-settings-copy).
- The daemon reads `branch` with `branch --show-current`, so it works on an unborn
  HEAD. An empty repo gives the init branch name, for example `master`. A detached
  HEAD gives `branch:"detached:<short-sha>"`.
- When `branch --show-current` fails, the result has no `branch` member at all.
  git's error text never appears there. An example is a HEAD that git fails to
  resolve (`ref: refs/heads/main` or 40 hex digits, each followed by junk). Measured
  side by side against `90fca6e6` and `f6010b97` on a Linux VM. If the `rev-parse
  --short HEAD` call of a detached HEAD fails, the result has no `branch` member
  either. `5fd08069` answers so in cell V6-13d of round `val-s3p2b2-3` (Windows
  VM).
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
  origin`, `symbolic-ref` and `branch --show-current` (row L01). Row L01 is of
  `89cb6289`. Without the member, the first `git.info` of a daemon of `5fd08069`
  has 21 calls there (cell 50a of round `s3p2b-edges`, Linux VM). The 12 more are
  the ten test calls, the call about the repository and one config read.
- On Windows git checks the root. After `rev-parse --git-dir` the daemon runs `git
  --git-dir=<pin> config -z --list` and `git -c … --git-dir=<pin> --work-tree=<walk
  root> rev-parse --show-toplevel`, both in `<pin>`, the `GIT_COMMON_DIR` pin (row
  W01). In the state `shadow` `<pin>` is the git directory, and `GIT_COMMON_DIR`
  names the copy (row D5 of round `val-s3p2b2-3`, Windows VM, `5fd08069`). When git's answer names the same directory as the walk root, the root is
  git's answer as printed. Else it is the walk root with forward slashes (rows W14 to
  W16). A directory symlink resolves in the walk and a junction does not (row D03).
  Some points are not measured. claustrum tests the same directory by file identity
  (`os.SameFile`). That is derived from rows W08 and W09. claustrum takes no
  relative answer. The spelling of the walk root is not measured. With no pin,
  claustrum runs the pair on the git directory of the walk. One state with no pin
  is measured. With a daemon `GIT_COMMON_DIR`, the pair and the root are equal on
  `89cb6289` and the claustrum build of this change in every call (Windows VM, cell B-13).
  The other states with no pin are not measured.
- On Windows a path through a junction can name two repositories. The fixture is
  that of the trust check: a junction `<P>\J` to `<R>\inner`, P on branch `main`
  with the origin `p/p`, and R on branch `rmain` with the origin `r/r`. For the
  path `<P>\J\sub` with no daemon `GIT_DIR` or `GIT_COMMON_DIR`, each call after
  the excludes read carries `GIT_COMMON_DIR=<P>\.git`, the entry of P (cells
  B-01, B-02, B-07 to B-09 and B-16). The other rows of the table have other
  entries. The calls of the method run in `<P>\J\sub`, and `rev-parse --git-dir`
  prints `<R>/.git` there. `89cb6289` answers these fields on a Windows VM:

    | Cell | State | `root`, `repo` | `branch` | `repoSlug` | `defaultBranch` |
    |---|---|---|---|---|---|
    | B-01, B-02 | `path` is `<P>\J\sub` | P | `rmain` | `p/p` | `""` |
    | B-04 | `path` is `<P>\J` | P | `rmain` | `p/p` | `""` |
    | B-07 | P has no origin | P | `rmain` | `""` | `""` |
    | B-08 | R has no origin | P | `rmain` | `p/p` | `""` |
    | B-09 | R is on a detached HEAD | P | `detached:<short sha of R>` | `p/p` | `""` |
    | B-16 | the config of R sets `core.hooksPath` and `core.fsmonitor` | P | `rmain` | `p/p` | `""` |
    | B-17k | `path` is `<KP>\J\sub`, `<KP>` a junction to P | P | `rmain` | `p/p` | `""` |
    | B-17d | `path` is `<P>\J\..\J\sub` | P | `rmain` | `p/p` | `""` |
    | B-05p | `path` is P, no junction | P | `main` | `p/p` | `main` |
    | B-05r | `path` is `<R>\inner\sub`, no junction | R | `rmain` | `r/r` | `rmain` |
    | B-06 | a folder symlink in the place of the junction | R | `rmain` | `r/r` | `rmain` |
    | B-13 | daemon `GIT_COMMON_DIR` of R | P | `rmain` | `r/r` | `rmain` |
    | B-14 | daemon `GIT_DIR` of R | P | `rmain` | `r/r` | `rmain` |

  In the mixed cells the listing prints `remote.origin.url` of P, `symbolic-ref
  refs/remotes/origin/HEAD` prints `refs/remotes/origin/rmain`, and the `rev-parse
  --verify` of that ref exits 1 (cell B-01). In cell B-15 the config file of P has
  a broken line. The listing in `<P>\J\sub` exits 128, and `89cb6289` answers
  `-32603` with the listing refusal that ends
  `fatal: bad config line 15 in file <P>\.git/config`, after 3 git calls. In cell B-03 the junction is outside any
  repository, and the answer is the non-repo body. The claustrum build of this change
  answers the same frame in each of these cells (Windows VM). For cell B-15 now,
  see "Git-directory trust check". Its calls carry the
  same entries in these cells, except B-14. In cell B-03 it runs one call less
  (see the trust check). The claustrum test on real junctions runs on Windows
  only. It has 11 cells: B-01, B-03, B-04, B-05p, B-05r, B-07, B-08, B-15, B-16,
  B-17k and B-17d. A test with simulated links runs on Linux and macOS. In cell
  B-14 calls 4 and 5 differ. They are the root pair. `89cb6289` runs them with
  `--git-dir=<P>\.git`, in the folder `<P>\.git`, with `GIT_COMMON_DIR=<P>\.git`.
  The claustrum build of this change runs them with `--git-dir=<R>\.git`, in the folder
  `<R>\.git`, with `GIT_COMMON_DIR=<R>\.git`. The frames are equal. Cell J-f-info
  repeats cell B-14 with the same result. Not measured: a junction to another
  drive, and a junction to a bare repository. Three more states have no cell of
  `89cb6289`. The answers of claustrum there are from the code:
    - The `path` goes through the junction, and `<P>\.git\commondir` is a stray
      file. claustrum judges `<P>\.git` by the stray-commondir rules, and a refusal
      names P. The build before the spelled start ran no trust check there.
    - A junction outside any repository leads into a repository whose config file
      has a broken line. claustrum answers the non-repo body. The build before the
      spelled start answered the listing refusal for the config file of that
      repository.
    - The `path` is `<P>\missing\..`, and `<P>\.git\commondir` is a stray file.
      The answer is that of the first state.
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
  starts with `-` gives `""` with no further call. Else a `rev-parse --verify --quiet
  refs/remotes/origin/<name>^{commit}` runs, with its listing, and only exit 0 keeps
  the name. A dangling `origin/HEAD` and one that names a blob give `""` too.
  `f6010b97` and `89cb6289` do so on Linux and macOS VMs (rows I10a to I10c). The
  call has the heavy profile, as on `5fd08069` (cell B3d of round `s3p2-linux`,
  Linux VM, with no `"shadowDirOff"` member). With the member, cell A2 of round
  `val-s3p2b1` shows the same on Linux, macOS and Windows VMs. In a partial clone
  no build starts a fetch for `git.info`. The object store, the refs and
  `packed-refs` are unchanged after 4 requests, and `defaultBranch` is equal. That
  is cell B3 of round `val-s3p2b1` on a Linux VM, for `89cb6289`, `5fd08069` and
  claustrum, with and without the member. Not
  measured: other invalid names, and a `symbolic-ref` answer outside
  `refs/remotes/origin/` (claustrum verifies it the same way).

#### git.status
`{path,baseRepo}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}`

Reports the changes in a linked git worktree of a session. The page [git.status](protocol/git-status.md) has the request, the response and the errors.

#### git.list_branches
`{path}` → `{"isRepo":true,"branches":[…sorted…]}`

Lists the local branches of a git repository. The page [git.list_branches](protocol/git-list-branches.md) has the parameters, the response and the errors.

#### git.worktree_create
`{baseRepo,branchName,worktreePath[,sourceBranch][,existingBranch][,worktreeRoot][,timeoutMs][,shadowDirOff]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}`

Makes a linked git worktree for a session, on a new branch or on a branch that exists. The page [git.worktree_create](protocol/git-worktree-create.md) has the parameters, the response and the error codes.

#### git.worktree_remove
`{baseRepo,worktreePath[,branchName][,worktreeRoot]}` → `{"success":true}` (lenient), or `{"success":true,"branchKept":true}` when the branch step keeps the branch

Deletes a linked git worktree of a session and its registration, and then runs the branch step. The page [git.worktree_remove](protocol/git-worktree-remove.md) has the request, the response and the refusals.

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
- The steps, in order. "Keep" means that the branch stays. After a failed
  `config.worktree` step of a create the config is read first, and a refusal
  keeps the branch (cells C9 and C11 of round `task007-01`, L M).
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
    40 s, and rev-list got the other 20 s. On W it ended at about 59.9 s with
    exit code 1 (row B2-02). On M it ended at about 61.2 s, in a run with one VM stall.
  - claustrum starts the bound right before the config listing of for-each-ref, and
    it also covers rev-parse (claustrum's choice, not measured).
  - update-ref gets SIGTERM 5 s after it starts, on L and M. A call that ignores it
    is killed 5 s later (rows R23a, R23b, R23a1 and R23b1). On W it is killed at
    about 5 s, with exit code 1 (row R23a). The 5 s count from the start of
    update-ref, after its config listing.
  - The same stop holds in the create rollback. `f6010b97` already stopped update-ref
    there, and the undo text ends `: signal: terminated` (row B2-09e, L M). A rollback
    update-ref that ignores the SIGTERM is killed 5 s later, as on remove (claustrum's
    choice, not measured).
  - Only the git process, not its children, gets the SIGTERM or the kill
    (claustrum's choice, not measured).
  - The caller's `timeoutMs` does not stop the step (rows C04 and B2-10, L).
- Within one daemon, the check and delete of a branch run one at a time per
  repository, also through a linked worktree. Two removes of worktrees whose
  branches share a unique commit then leave one of the branches. The second reply
  carries the member. This holds for one `baseRepo` (rows R21, L, and
  B2-12, M) and through a linked worktree (row R21b, L).
  The branch steps of different repositories run at the same time (row B2-08, L).
  claustrum keys the lock by the common dir, also in a rollback (not measured).
- claustrum runs two removes of one `baseRepo` one after the other.
  `89cb6289` deletes both worktree directories first (row R21, L).
- A remove deletes its leaf during the `worktree add` of a create in its git
  directory. Its entry, branch and reply wait for the add, not the checkout
  (`5fd08069`, round `tier3-02`, cell T3, L M, T3M and T3C, L). A create's add and a
  remove through another `baseRepo` also wait (not measured).

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
| after a failed `config.worktree` step, the config is refused at a second read | the same, with that refusal in parentheses | none | C9, C11 of round `task007-01` (L M) |
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
[launcher.resolve](protocol/launcher-resolve.md), the `launcher` param of
[process.spawn](protocol/process-spawn.md#the-managed-launcher), and the launcher runs of `-install` and
`-probe-cli`. Every rule of these surfaces is measured against `89cb6289` on a
Linux VM unless it says otherwise. A rule that names macOS or Windows was measured there too.

#### launcher.resolve
`{cliPath}` → one of four shapes.

Reads the managed settings and answers the managed launcher for a CLI, or why there is none. The page [launcher.resolve](protocol/launcher-resolve.md) has the request, the four response shapes and the reason texts.

### process.* (the agent/MCP-hosting core)

The client supplies its own `id`, which is any string. The daemon delivers output
as id-less stream notifications, and it buffers them for a later replay.

#### process.spawn
`{id,command[,args][,cwd][,env][,disableShellAgentSocket][,launcher][,wantPid]}` → `{"success":true}`, then stream frames

Starts a child process as the user of the daemon and streams its output as stream frames. The page [process.spawn](protocol/process-spawn.md) has the parameters, the environment of the child, the launcher checks and the errors.

#### process.stdin
`{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}`

Sends bytes to the standard input of a child process, with an optional `offset` that makes a repeated send safe. The page [process.stdin](protocol/process-stdin.md) has the parameters, the response and the errors.

#### process.kill
`{id[,signal]}` → `{"success":true}`

Sends a signal to a child process and answers at once, without a wait for its end. The page [process.kill](protocol/process-kill.md) has the parameters, the signals, the Windows child trees and the errors.

#### process.killAndWait
`{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}`

Sends a signal to a child process, waits for it to end, and can end it by force. The page [process.killAndWait](protocol/process-killandwait.md) has the parameters, the results and the errors.

#### process.reattach
`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`

Moves the output stream of a child process to the connection that sends the request, and sends the stored frames that the client missed. The page [process.reattach](protocol/process-reattach.md) has the parameters, the response and the errors.

### plugins.* (added `19f30c46`)

#### plugins.prune
`{[keep],[minAgeDays]}` → `{"root":"<per-install root>","pruned":[…],"prunedLegacy":[…],"staleArchives":0,"kept":0,"live":<n>,"young":<n>,"legacy":"absent|swept|skipped: …","minAgeDays":<clamped>}`

Deletes the cached plugin directories that no session used for some days. The page [plugins.prune](protocol/plugins-prune.md) has the parameters, the response and the errors.

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
  closed pipe discarded. The acknowledgement caveat of `stdinApplied` (see the page
  [process.reattach](protocol/process-reattach.md)) still
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
| `-metrics-addr <a>` | `metrics-addr` | `""` | Prometheus `/metrics` (claustrum-only) | -serve |
| `-wire-log <p>` | `wire-log` | `""` | append every JSON-RPC frame to `<p>` as JSONL (claustrum-only) | -serve |
| `-wire-log-max-string <n>` | `wire-log-max-string` | `512` | bytes kept per string value. `0` keeps whole payloads | -serve |
| `-keep-children` | `keep-children` | off | survive restart (CT-2) | -serve |
| `-listen-pipe` | `listen-pipe` | off | named-pipe transport, Windows-only (CT-5) | -serve |
| `-max-extract-bytes <n>` | `max-extract-bytes` | `0` | cap `files.extract_tar` bytes (D3) | -serve |
| `-git-timeout <dur>` | `git-timeout` | none | deprecated. It sets nothing and logs one warning. The git calls keep their time limits (retired D5) | -serve |
| `-files-read-regular-only` | `files-read-regular-only` | none | deprecated. It sets nothing and logs one warning. `files.read` refuses a file that is not regular with or without it (retired D4) | -serve |
| `-max-cli-bytes <n>` | `max-cli-bytes` | `0` | cap CLI decompress + download (D10) | -install |
| `-cli-probe-timeout <dur>` | `cli-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The direct `<cli> --version` run always has its 30 s and 120 s bounds (retired D11) | -install |
| `-cli-download-timeout <dur>` | `cli-download-timeout` | `0` | download deadline (D12) | -install |
| `-libc-probe-timeout <dur>` | `libc-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The `ldd` probe always has its 5 s bound (retired D14) | -install |
| `-cli-keep <n>` | none | `3` | version files of the cli-dir to retain on prune, the new CLI among them. The new CLI always stays. `0` and a negative value remove every older version file | -install |
| none (config only) | `version-override` | none | `-version` stdout rebrand (CT-3) | -version |

Config-value parsing: bool keys accept `true/1/yes/on` and `false/0/no/off`. The
parser ignores a negative or unparseable numeric value, so a typo can never
silently enable a cap. An unrecognised bool value leaves the key unset, and the
flag value, or the default, stands.

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] \
          [-keep-children] [-listen-pipe] [-wire-log <p> [-wire-log-max-string <n>]] \
          [-max-extract-bytes <n>]
```

The binary self-daemonizes. It re-execs itself as a detached process, which
reparents to init, and that process runs the RPC server.
[Daemon startup](#daemon-startup-serve) has what the start does, the ready line,
and the exit codes and texts of the launcher. Unless a rule here says otherwise,
it is measured against `89cb6289` on Linux and macOS VMs.

##### The token source

A token source is required. The detached daemon tests for it, not the launcher.
The daemon writes its reason to its own stderr, which is the
[daemon log](#daemon-log-remote-serverlog), and exits. The launcher then reports
`claustrum: timeout waiting for daemon to accept on <socket>` after 12 s, with exit
`1`. This is deliberate parity.

- With neither flag the reason is
  `claustrum: daemonized child requires --token-file or --token-fd`. That case is
  not measured on `89cb6289`.
- A zero-byte `-token-file` behaves the same way.
- A `-token-file` that cannot be read gives `claustrum: read --token-file: <err>`.
- The daemon reads the token as a line. It strips one trailing `\n` or `\r\n`, and
  preserves other surrounding whitespace verbatim. It then removes the
  `-token-file`.
- `-token-fd <n>` is claustrum-only. The launcher reads the token from an
  already-open fd, where `0` is stdin, and forwards it to the daemon over an
  inherited pipe. So this handoff never touches disk. A failed read there gives
  `claustrum: read --token-fd: <err>` on the stderr of the launcher, with exit `1`
  (from the code).

##### The login-shell PATH (Linux and macOS)

The daemon runs no login shell at its start. The first `process.spawn` reads the
login-shell PATH, and that spawn waits for the read. The daemon keeps the result
for its life, and it keeps a failed read too. On Windows the first spawn reads the
daemon's own PATH and runs no shell (from the code). See
[process.spawn](protocol/process-spawn.md#the-environment-of-the-child).

- If `$SHELL` is an executable file, the read runs `$SHELL -l -i -c …`. Otherwise
  it runs the first usable of `/bin/zsh`, `/bin/bash` and `/bin/sh`.
- The value reaches `process.spawn` children as their `PATH` only. It never
  reaches the daemon's own environment, so it never changes how the daemon
  resolves a `command`.
- The read has a cap of 4 s. At the cap the daemon kills the shell and discards
  whatever it printed, even a valid PATH. Children then get the inherited PATH,
  and the log holds
  `[shellenv] Failed to extract PATH from login shell: shell PATH extraction timed out (<shell>)`.
- The cap also holds after the shell ends while another process holds the output
  pipe of the shell. The daemon sends that process no signal. claustrum closes its
  end of the pipe two caps after the shell ended (claustrum's own, not measured).
- The [SSH agent hand-off](protocol/process-spawn.md#the-ssh-agent-hand-off-linux-and-macos)
  runs the login shell again, with its own cap of 4 s. So a login shell that hangs
  delays the first spawn by about 8 s. A later spawn answers at once.

The shell of the PATH read gets seven entries. Five come from the daemon: `HOME`,
`PATH`, `SHELL`, `TERM` and `USER`. A name that the daemon does not have gets an
empty value. The other two are `DISABLE_AUTO_UPDATE=true` and
`ZSH_DISABLE_COMPFIX=true`. No other entry of the
daemon reaches it, such as `LANG` or `CLAUDE_SSH_DAEMON_CHILD`. The order of the
entries is not measured. A daemon that has a `TERM`, or that has no `HOME`, `PATH`
or `USER`, is not measured either. The shell of the SSH agent read gets another
environment.

##### The re-exec marker

The re-exec marker is `CLAUSTRUM_DAEMON_CHILD`, not the reference's
`CLAUDE_SSH_DAEMON_CHILD`. [ARCHITECTURE.md](ARCHITECTURE.md) has the reason.
claustrum unsets its own marker before it spawns. The daemon still sets
`CLAUDE_SSH_DAEMON_CHILD=1` in its environment, so `process.spawn` children
inherit it as on the reference.

##### Flags of claustrum alone

These flags are off the wire. `-keep-children` and `-listen-pipe` have an entry in
[`DIVERGENCES.md`](DIVERGENCES.md). `-metrics-addr` and `-wire-log` have none, and
this section holds their detail:
- `-metrics-addr <a>` serves Prometheus counters at
  `http://<a>/metrics`, covering connections, spawns and exits, reattaches, and
  stream and stdin bytes. It is off by default, with no listener. It counts only,
  and it has no auth, so bind it to loopback. The daemon logs a bind failure
  (`[Server] metrics: …`), which is non-fatal.
- `-wire-log <p>` appends every JSON-RPC frame, in both directions, to
  `<p>` as JSONL. It is a diagnostic side channel that observes already-marshaled
  bytes, so a daemon that logs emits frames byte-identical to one that does not. It
  is off by default, with no file and no work. `-wire-log-max-string <n>` bounds
  each string value. The default is 512, and `0` keeps whole payloads, which is
  needed to reconstruct a session from stream frames. Credentials are redacted by
  key, which covers the `auth` member and token-like env keys. A secret a client
  embeds inside a payload string is not caught, so redaction is best-effort, not a
  guarantee. A capture holds the frames of both directions, such as the `content`
  of a `files.read` reply, `process.stdin` and the spawn env. It is therefore
  forced to `0600` on every open, appends included, and it belongs somewhere
  private. Each record carries the
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
  limits: a process whose `id` a later spawn took, and the host cleaner. The new
  daemon does not re-adopt them, and the survivors lose their stdio. Their stdin
  reaches EOF, and a stdout or stderr write gets SIGPIPE, or EPIPE for a child that
  ignores SIGPIPE, such as Node. It therefore suits only children that tolerate
  dead stdio. On Windows the flag keeps the whole tree alive after `-stop` and
  after `server.shutdown`, the direct child too. The stdio of a survivor is not
  measured there. The reference has no such flag, and `89cb6289` exits `2` on it.
- `-listen-pipe` is CT-5 and Windows-only. See [Named-pipe
  transport](#named-pipe-transport-windows-opt-in). The daemon logs a setup failure
  (`[Server] named-pipe transport: …`), which is non-fatal. The socket still serves.

The opt-in divergence on this mode is `-max-extract-bytes` (D3). Off is parity.
Its frame is in [files.extract_tar](protocol/files-extract-tar.md). See the flags
table and [`DIVERGENCES.md`](DIVERGENCES.md).

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
frame to stdout. A live daemon's reply does not always arrive (see `server.*`
above). On `5fd08069` -stop prints `stopped` with rc 0 both when the reply arrives
and when it gets EOF (cell R4 of round `task034-01`).

After the read, on Linux and macOS, `-stop` tries `daemon.lock` every 50 ms for up
to 3 s. A lock that comes free gives `stopped` and no signal. A lock still held
after 3 s gets the signal steps of a failed connect, below. `5fd08069` does this
in cells S1 to S5 of round `task034-01` (Linux and macOS VMs). There `terminated`
came at about 3.1 s and `killed` at about 7.1 s. A holder whose record names no
daemon role got no signal and the word `survivor`, at about 3.6 s. In cell S4 of
round `task034-02` it came at 3.54 to 3.65 s, after 72 lock tries (Linux and macOS
VMs). So claustrum tries the lock for 0.5 s more for that holder. A lock that comes free then gives `stopped` (claustrum's
own, not measured). A lock that a new holder took during the `SIGTERM` wait gives
`survivor` and no signal to it (cell S2b, Linux VM). Its line is below.
On Windows `-stop` prints `stopped` at once, as
`5fd08069` does (cell W-S1, Windows VM).

A lock that comes free gets no write (cells S1 and S5 of round `task034-02`, Linux
and macOS VMs). Only after `terminated` or
`killed` does `-stop` write `{"pid":<pid>,"role":"stop","node":"<node>"}` into
`daemon.lock`, and empties the file before it prints the word (Linux cells S2 and
S3, macOS cell S3). After `terminated` or `killed`, `-stop` also removes the
socket and `daemon.token`. In those cells the daemon had already removed both.

`-stop` prints one word on stdout, then a newline, and exits `0` in every state
in the table below. The words and the rules below are measured against
`f6010b97` and `90fca6e6` on a Linux VM, except the rules marked as claustrum's
own. The 3 s wait is measured on `5fd08069` (round `task034-01`). After `SIGKILL`, `-stop` polls the lock for 1.5 s. The word `killed` needs
the lock to become free in that wait. The macOS results are below.

| word | state |
|---|---|
| `stopped` | The connect succeeded. On Linux and macOS the lock also came free within 3 s, or there is no usable `daemon.lock` (claustrum's own). |
| `none` | The connect failed, and `daemon.lock` is free or missing. claustrum also prints `none` when the lock cannot be opened or locked. |
| `terminated` | The connect failed, or the lock was still held 3 s after it, and a live serve daemon of this binary for this socket holds `daemon.lock`. `-stop` sent `SIGTERM`, and the lock became free. |
| `killed` | As `terminated`, but the lock was still held 4 s after `SIGTERM`. `-stop` sent `SIGKILL`. |
| `survivor` | As `terminated`, but a live process that is not that daemon holds `daemon.lock`. `-stop` does not signal it. The word is also `survivor` if the lock is still held 1.5 s after `SIGKILL`. `-stop` does not wait longer. claustrum prints it too when a signal cannot reach a holder that is still there. |

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

After a successful connect, a holder whose record names no daemon role gives this
line. It is the text of `5fd08069` in cell S4 (Linux VM):

```text
<ts> WARN  [daemon] stop: WARNING <dir>/daemon.lock is held by a live process we will not signal (its holder is not a daemon (role "")); leaving it alone
```

A lock that another daemon took during the `SIGTERM` wait gives this line, with
the pid and instance of the new holder. It is the text of
`5fd08069` in cell S2b of round `task034-02` (Linux VM):

```text
<ts> WARN  [daemon] stop: <dir>/daemon.lock changed hands during eviction (now pid <N> (instance "<id>")); leaving the new holder alone
```

This line is claustrum's own. It comes from the second holder check:

```text
<ts> WARN  [daemon] stop: pid <N> is no longer our serve holder before SIGKILL (<reason>); leaving it alone
```

After a successful connect, `-stop` removes nothing, except after `terminated` or
`killed`. A live daemon removes its own
socket and `daemon.token` when it stops. After a failed connect, `-stop` removes
the socket path and the `daemon.token` beside it, stale or not. It does this after
the lock check. If the word is `survivor`, `-stop` removes neither file. This is
measured for a holder that `-stop` does not signal and for a lock that outlives
`SIGKILL`. The other `survivor` cases follow the same rule. `-stop` never creates
or removes `daemon.lock`. After a failed connect it writes no record into it, as
on `f6010b97` and `90fca6e6`. The socket unlink removes
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
| `opening input: <err>` | `-cli-zst` open error |
| `decompressing: <err>` | bad zstd blob or body, for example `invalid input: magic number mismatch`. A `-cli-zst` folder gives `read <p>: is a directory` (`5fd08069`, cell L13, Linux VM) |
| `decompressing: decompressed CLI exceeds <n> bytes` | D10 cap, opt-in |
| `download failed: response exceeds <n> bytes` | D10 cap on the download body, opt-in |
| `download failed: <transport err>` | a transport error before the body, for example a refused connection |
| `download failed: Get "<url>": net/http: timeout awaiting response headers` | no response headers for 60 s (always-on, parity, see below) |
| `download interrupted after <got>/<total> bytes: <transport err>` | a body read that ended with a transport error, for example `read tcp …: read: connection reset by peer` (parity, see below) |
| `download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)` | D12 download deadline, opt-in |
| `download stalled: no data for 60s after <got>/<total> bytes` | read-idle abort, meaning no bytes for 60 s on the `-cli-url` body (always-on, `4534d86` parity, VM-measured) |
| `mkdir cli dir: <err>` | cli-dir uncreatable. `cliPath` is then empty. A file or a FIFO at the cli-dir path is replaced first (see Staging and cleanup) |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli path must not be or contain the home directory: "<path>"` | a folder at the final path that is the home folder or one of its parent folders (D2 hardening) |
| `clearing stale dir at <path>: <err>` | an occupied `cliPath` directory that claustrum cannot remove |
| `rename: <err>` | the rename of the new CLI failed, for example `rename: rename <tmp> <cli>: Access is denied.` on Windows (`5fd08069`, cells W1 and W6). The prefix on Linux and macOS is claustrum's own |
| `rename: staging file vanished before install: <err>` | a concurrent sweep took the staging file |
| `cli unresponsive: the installed Claude Code binary was started through the host's managed launcher <argv0> and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish` | a managed launcher run stopped at 33 s (123 s after a fresh install), with `CLAUDE_SSH_MANAGED_LAUNCHER=1` (see below) |

The managed launcher (`89cb6289` parity):
- `-install` uses the [managed launcher](#launcher-added-89cb6289) only when
  `CLAUDE_SSH_MANAGED_LAUNCHER` is exactly `1`. The values `true` and `1 ` (a
  trailing space) and an unset variable add no field and run no launcher, even with
  a usable launcher. claustrum treats every value other than `1` as off. That is
  claustrum's choice (not measured for other values).
- With the gate and a CLI path, `-install` resolves the launcher for that path, as
  [launcher.resolve](protocol/launcher-resolve.md) does. The facts line then gains launcher fields after
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
- On a cache hit on Linux the CLI file must be a regular file with an exec bit.
  It must start with `#!` or an ELF header of this machine. Another file does
  not run, and the install runs over it. `5fd08069` does that for a text file and
  an arm64 ELF on amd64, then answers `"launcherStatus":"usable"` (cells L14a
  and L14b, Linux VM). The test beyond those two files is claustrum's own.
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
- The prune counts only `<version>.exe` files (`5fd08069`, cell W3).
- A folder at the bare name stays. A folder at the `.exe` name is replaced by
  the CLI file.
- A CLI that an older claustrum installed at the bare name counts for nothing.
  `-install` answers `missing` for it until a source flag installs the `.exe`
  file beside it.
- Not measured: a version that ends in `.EXE`. A version that
  the sweep claims by its end, such as `1.0.0.zst`, installs as `1.0.0.zst.exe`. The
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
- The source is decompressed into the staging file first, and the checksum
  comes after it. A body that is no zstd archive answers
  `decompressing: invalid input: magic number mismatch` with a right and with a
  wrong checksum. `fetch` then counts the bytes that the decoder read:
  `"bytes":4` for a body of 4096 bytes. Only the leading progress line is
  printed (`5fd08069`, cells L15a and L15b, Linux VM). The retired D13 holds the old
  order. No chmod, clear or rename runs before the checksum passes
  (claustrum's own reading).
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
- On Linux and macOS the folder step uses the `-cli-dir` text as given, also on
  a cache hit (a Linux VM row). So `-cli-dir <dir>/sub/..` makes `<dir>/sub` (mode `0700`).
  Windows makes nothing there. If the step fails, `cliPath` is empty and
  `cliError` is the mkdir error, such as `mkdir cli dir: mkdir <dir>/sub: not a
  directory`. `5fd08069` does the same (round `tier3-01`, Linux, macOS and
  Windows VMs).

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
runnability test. claustrum streams the blob and never buffers it. It keeps a
`-cli-zst` path, not a `[]byte`, so the staging retry can re-read it. "Cap off" therefore does not
mean unbounded memory. See [`DIVERGENCES.md`](DIVERGENCES.md) → D10.

`-cli-version` hardening is claustrum-only:
- D6 requires a single path component. The clearing step is an `os.RemoveAll`
  on the CLI path, `<cli-dir>/<version>` (on Windows `<version>.exe`). A version
  that escapes the cli-dir
  therefore deletes unrelated data. Measured, the reference destroys the target on
  `../victim`. `link/1.0.0` through an intermediate symlink also escapes. claustrum answers
  `cli version "…" must be a single path component`. It answers before
  the install writes or removes anything at a path built from the version. The
  two sweeps of the cli-dir name only the cli-dir, and they are not behind this
  rule. claustrum answers the refusal, writes nothing and keeps the blob. That
  rule runs on the install path. A regular file that
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
- A version that the sweep claims, such as `.fetch-1` or `1.0.0.zst`, installs.
  The same run keeps it, because the sweep removes only old entries. A later
  install removes it once it is more than 10 minutes old. This matches the
  reference since `4534d86`. The retired D7 refused such a version. On Windows
  the file is `<version>.exe`, and the sweep keeps it.

Staging and cleanup:
- An install follows the order of the reference. The new CLI is put at its
  final path, the `-cli-zst` blob is removed and the sweep runs. Then
  `<final path> --version` runs. At that run the
  cli-dir holds no `.fetch-` temp of this install and no swept
  file more than 10 minutes old. On `5fd08069` the blob is gone at that run
  (cells L11, M4 and W7). The prune follows a good run only. A run that
  exits non-zero removes the new CLI again with one plain remove. A stopped run
  keeps the new CLI. Measured on `89cb6289` on Linux, macOS and Windows. On
  Windows claustrum can leave that file when the exited CLI holds it
  (claustrum's own code, not measured).
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
- The sweeps and the `-cli-keep` prune skip an entry of the cli-dir that is
  the home folder or holds it ([D2](DIVERGENCES.md#d2)). They remove regular
  files only, so a home folder stays there on `5fd08069` too (from those cells,
  no home cell on `5fd08069`).
- The volume of the macOS VM ignores letter case. There a `-cli-version` in
  another letter case names the folder on disk. With `-cli-version ALICE` and a
  folder `alice` that is not the home folder, both binaries replace the folder.
  The new file has the name `ALICE`, as the command gave it (cells HXKC and
  HXKC0). With `-cli-version USERS` and a folder `users` that contains the home
  folder, `89cb6289` removes the folder and names the new file `USERS`.
  claustrum refuses, and the folder keeps its name (cell HXK5).
- Not measured: a new CLI that exits non-zero over a file or a folder at the
  final path. claustrum then removes the new CLI as for an empty one.
- Not measured: how the reference writes the final file, and the order of its
  sweep against that write.
  claustrum decompresses to `<cli-dir>/.fetch-<random>`, renames that file to the
  final path and then sweeps.
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
  place. This is one code path for `-cli-url` and `-cli-zst` alike. A download
  is decompressed into that file as it arrives, and no other file is made.
- The `-cli-zst` blob goes once the new CLI is in place. Every earlier failure
  keeps it. On `5fd08069` it stays after a blob that is no zstd archive (cell
  L3) and a folder that cannot be cleared (cells L12 and M5). It stays after a
  denied rename too (cells W1 and W6). claustrum keeps it after a failed chmod too (claustrum's
  own). A CLI that then does not run still costs the blob. The sweeps skip the
  blob, so a blob `1.0.0.zst` in the cli-dir stays (cell L3).
- On Windows a rename that access denied stops is tried again for 0.2 s.
  `5fd08069` keeps the staging file 0.26 to 0.53 s there (cells W1). The
  staging file of claustrum lived 0.35 to 0.48 s there (round `task090-02`). If the
  target holds the bytes of the staging file, the install counts as done and
  the staging file goes, as on `5fd08069` (cells W2). The pauses are
  claustrum's own.
- The `-cli-keep` prune follows `5fd08069` (cells L4 to L6, M1, M2a, M2b, W3,
  W4a and W4b, Linux, macOS and Windows VMs):
    - Only a version file counts, and only a version file goes. On Linux and
      macOS that is a regular file whose name is a version such as `1.0.0`,
      with an exec bit. On Windows it is a regular file `<version>.exe`. A
      folder, a link, `README`, `.blob-planted`, a `1.1.0` with mode `0644`, a
      bare `1.1.0` on Windows and `README.exe` stay and take no keep place.
    - claustrum counts a name of three runs of digits with dots between them,
      such as `1.0.0`. Other names are not measured.
    - The order is the mtime, newest first. The new CLI takes its place in the
      order and always stays. `-cli-keep 0` removes the older version files.
    - A negative `-cli-keep` counts as 0. The result line follows, with exit
      `0` and an empty stderr.
    - Entries with one mtime stay in byte order of the name, so the later name
      goes. That order is measured on `89cb6289` (cells T1).
    - A failed install and a cache hit do not prune.
- Two sweeps remove old regular files of the cli-dir with one plain remove
  each (`5fd08069`, cells L8 to L10, M3a, M3b and W5):
    - The 10 minute sweep takes `.fetch-<digits>` and `<version>.zst`. A file
      `.fetch-d`, a file `x.zst`, an empty folder `.fetch-9` and a folder
      `.fetch-e` with content stay.
    - The 7 day sweep takes `<version>.zst.part`. A file `p.zst.part` stays at
      8 days. 7 days and 5 s go, and 7 days less 5 s stay.
    - The 7 day sweep runs once before any CLI runs. On a good cache hit only
      that sweep runs (cell L9). The 10 minute sweep runs before the run of a
      new CLI and after a failed attempt. It also runs after a cache hit whose
      run was stopped (cell P6 of `89cb6289`).
    - The names match the letter case, and a future mtime is not old
      (claustrum's own, not measured on `5fd08069`).
    - A concurrent install can reclaim a staging file only once it is more
      than 10 minutes old. claustrum retries a `-cli-zst` staging step once
      then.
- claustrum clears an occupied `cliPath`, and that is not fatal. `rename(2)`
  refuses to replace a non-empty directory, so claustrum removes it first, as a
  tree. It removes it only when `cliPath` is a directory. A regular file, which an
  installed CLI always is, is replaced atomically by the rename. If claustrum cannot remove it →
  `clearing stale dir at <path>: <err>`. On Windows the cleared name is
  `<version>.exe`. The D6 rule runs before this step, so `cliPath` is a direct
  child of the cli-dir.
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
