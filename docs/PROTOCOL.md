# claustrum protocol reference

claustrum uses newline-delimited **JSON-RPC 2.0** over an `AF_UNIX`
`SOCK_STREAM` socket. This document is the complete wire contract. The validation
battery checks that same contract byte-for-byte. All reference behaviour was
probed at `5db5e4a` unless a line says otherwise. The divergence catalog, its
rules, and the reopen triggers are in [`DIVERGENCES.md`](DIVERGENCES.md).

## Transport

- One JSON object per line (NDJSON). No length prefix, no binary framing.
- A single request line has a cap of **1 MiB** (`bufio` max token = `1024*1024`).
  The daemon serves a line up to 1048575 bytes. A line of 1048576 bytes or more
  closes the connection with no reply. Chunk large `process.stdin` payloads below
  this cap.
- `AF_UNIX` stream socket, created mode `0600` (owner only).
- The connection is **persistent**. It stays open after a response, and id-less
  stream notifications arrive on it asynchronously.
- The daemon dispatches a connection's requests **concurrently**. Responses can
  arrive out of request order. Match them by `id`.

### Named-pipe transport (Windows, opt-in)

A strictly additive claustrum extension (CT-5) — the reference daemon has no such
transport. It is off by default, and claustrum is byte-for-byte identical to the
reference when it is off. Set `-serve -listen-pipe` (or `listen-pipe = true` in
`claustrum.conf`), and claustrum *additionally* serves the **exact same** NDJSON
JSON-RPC dispatch over a **Windows named pipe**, concurrently with the socket. The
wire contract, field ordering, framing, and `"auth"` handshake are the same. It
exists so that a Windows client that cannot consume `AF_UNIX` can still connect
(notably Python `asyncio`, whose Unix transports are Unix-loop-only).

- **Windows-only.** Other platforms ignore it and log a warning.
- **Name + discovery.** claustrum chooses the name
  (`\\.\pipe\claustrum-<random-instance-id>`). It publishes that name to
  **`rpc.pipe`** in the socket's directory (beside `rpc.sock` / `daemon.token`).
  claustrum writes the file atomically before the pipe accepts and before the ready
  banner, and removes it on graceful shutdown. The client reads that file to learn
  the opaque name.
- **Stale-file invariant.** The name is random for each boot. Therefore `rpc.pipe`
  exists **if and only if** a pipe is actively served this boot. Startup removes any
  leftover file from an unclean crash, so a client can never dial a stale name.
- **Owner-only + local**, by two independent mechanisms: an owner-only DACL (SDDL
  `D:P(A;;GA;;;<current-user-SID>)`, the named-pipe analogue of the socket's
  `0600`) **and** remote-client rejection at creation
  (`FILE_PIPE_REJECT_REMOTE_CLIENTS`, set by go-winio's `ListenPipe`). See
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

See [`DIVERGENCES.md`](DIVERGENCES.md) → CT-5 for the full contract.

## Authentication

Every request carries a top-level `"auth":"<token>"` — **except `server.shutdown`,
which is not authenticated at all.** A shutdown frame stops the daemon whether its
`auth` member is absent, empty, wrong, or valid, and `-stop` sends no `auth`
member. This matches the reference and is load-bearing: the Desktop client stops
the daemon with `server --stop --socket <sock>` from a bare SSH command line, with
no `CLAUDE_RPC_TOKEN` in its environment. The exemption covers auth **only**. The
daemon still rejects a shutdown frame with a bad or absent `jsonrpc` version with
`-32600`, and the daemon stays up. Every other method rejects an unauthenticated
request with `-32001 Unauthorized: invalid or missing auth token`, and also logs
`[Server] Unauthorized request: method=…, id=…`.

The server's expected token comes from `-token-file` (read once at startup, then
**unlinked**) or `-token-fd` (read from an open descriptor, forwarded to the
detached child over a pipe — this handoff never touches disk).

**No claustrum mode reads `CLAUDE_RPC_TOKEN`** — not `-serve`, not `-bridge`, not
`-stop`. `-bridge` is a simple relay and does not add auth. The client that speaks
through it must include `"auth"` itself, from the `daemon.token` handshake below or
from its launcher. claustrum only *removes* the variable: it unsets the variable
before it daemonizes, and it strips the variable from every spawned child.
Therefore a token never reaches a child through the environment.

### Token persistence (`daemon.token`)

Once the socket is listenable, the daemon writes the token to **`daemon.token`** in
the socket's directory (mode `0600`, written atomically via a `daemon.token-*` temp
file + rename), and **unlinks it on graceful shutdown**. A client can therefore
reconnect to an already-running daemon and re-authenticate after the original
`-token-file` was unlinked / the `-token-fd` pipe closed. The write does not depend
on the token source, because it uses the in-memory token. The write is also
best-effort: the daemon logs a failure (`[daemon] failed to persist token: …`) and
continues. Reference build `5db5e4a` added this, and claustrum matches it. It is
off the JSON-RPC wire, because the file sits beside the socket, not on it. An
unclean kill (`SIGKILL`/crash) leaves the file behind, because the daemon removes
it only on the graceful `server.shutdown` / `SIGTERM` path.

The fixed name + socket-dir location are the reconnect contract, so they are not
configurable. On Windows `0600` is not an owner-only DACL (a Go `os.CreateTemp`
limitation — the per-user session dir is the confinement), a parity caveat claustrum
deliberately does not "fix". The former "two daemons in one directory collide on this
file" caveat is now bounded: on Linux and macOS the run-dir lock (below) evicts a
prior live daemon of the same socket before the new one binds, so two same-socket
daemons no longer coexist to collide on the honest path. A collision survives only
where eviction is refused (a foreign or cross-machine lock holder, or a holder that
survives `SIGKILL`) or on Windows, which ships no run-dir lock.

### Run-dir lock (`daemon.lock`)

Before it binds the socket, a `-serve` daemon takes an exclusive `flock` on
**`daemon.lock`** in the socket's directory (mode `0600`) and writes an owner record
into it — a JSON object `{pid, role, node, instanceId, startedAt}` (`role` = `serve`;
`node` omitted when the machine identity is unknown). Reference build `4534d86` added
this, and claustrum matches it. It is off the JSON-RPC wire (a file beside the
socket). On graceful shutdown the daemon **truncates the record and drops the lock but
leaves the file in place** — unlike `daemon.token`, which is unlinked.

When a prior **live** daemon still holds the lock, the newcomer evicts it (`SIGTERM`,
then `SIGKILL` after a grace) before taking over, so a restart deterministically
replaces its predecessor. The eviction only fires against a holder that is verifiably
one of our own `-serve` daemons for this socket, on this machine — see the guards in
`daemon_runlock_unix.go`. Claiming is best-effort: any failure logs a warning and the
daemon serves without run-dir ownership rather than aborting.

Platform split: the lock, the owner record, and the eviction run on **Linux and
macOS**. The machine identity (`node`) is the boot id joined to the pid-namespace
inode on Linux, and `sysctl kern.bootsessionuuid` on macOS. **Windows ships no run-dir
lock** (the reference does not compile one there); mutual exclusion on Windows stays
the socket remove-then-rebind handoff. On macOS claustrum verifies the holder via
`sysctl KERN_PROCARGS2` where the reference skips the check — see
[DIVERGENCES.md](DIVERGENCES.md) D15.

### Daemon startup (`-serve`)

The `-serve` launcher **creates the socket's parent directory** if it is missing
(mode `0700`). The launcher then **does not return until the socket path exists**.
It polls every 20 ms, up to a bound of **10 seconds**. To confirm readiness it
dials the socket and closes the connection again. A freshly started daemon's log
therefore opens with a `New connection from: @` / `Connection closed: @` pair from
the launcher's own probe.

It waits for the **path to exist**, not for a successful dial. It also does not
give up early when the child dies. Both behaviours are measured against `5db5e4a`:

| start | what the launcher sees | outcome |
|---|---|---|
| normal | path appears, confirming dial succeeds | exit `0` |
| socket path occupied by a directory | path exists **immediately** | exit `0` (~0.01 s; reference 0.08 s) |
| child can never bind (uncreatable parent dir) | path never appears | exit `1` at ~10.04 s (reference 10.06 s) |

On a timeout the launcher prints
`claustrum: timeout waiting for daemon to accept on <socket>` to **stderr** and
exits `1`. On success it prints the ready banner and exits `0`. After a successful
`-serve`, the socket accepts connections before `-serve` returns. (Measurement
detail condensed out of this reference.)

### Daemon log (`remote-server.log`)

The launcher creates **`remote-server.log`** in the socket's directory (mode
`0600`, a **fresh file on every start** — the launcher unlinks and recreates any
existing log, and does not truncate it in place). The launcher redirects the
daemonized child's stdout and stderr into that file, so the launcher's own streams
stay empty. The first line is the ready banner (no timestamp):

```
Claustrum remote server listening on /run/user/1000/claude/rpc.sock
2026/07/31 00:17:30 INFO  [Server] New connection from: @
```

If claustrum cannot replace the existing log (a sticky directory that holds another
user's file), it **declines the log entirely**. The daemon's output then falls back
to inherited stdio. claustrum does not write into a file another user can read.
This is **intentional divergence D8** (always-on): the reference truncates a
root-owned, world-writable log and writes into it, while claustrum leaves that file
untouched. The trigger is not reachable on the deployed path, because the socket
directory (`~/.claude/remote/`) is per-user and not world-writable. That is why D8
is always-on and not opt-in. See [`DIVERGENCES.md`](DIVERGENCES.md) → D8.

Unlike the socket and `daemon.token`, claustrum does **not remove the log on
graceful shutdown**. The log outlives the daemon, so a post-mortem stays readable.
The fixed name and location are the deployment contract, not configurable.

### Orphan-exit self-probe

A running `-serve` daemon periodically checks whether its socket path still leads
back to itself and shuts down if it does not, matching reference build 4534d86. It
exists so a predecessor whose socket a newer daemon took over retires promptly,
instead of lingering indefinitely — the 5-minute idle timeout closes only an idle
*connection*, never the daemon, so a client-less orphan would otherwise never exit.
The check is off the JSON-RPC wire: the only observable effects are a self-directed
`server.capabilities` request on the socket once orphaned (a normal authed RPC) and
stderr log lines. In normal operation there is no self-RPC — only an `os.Stat`.

Every 60 seconds the daemon `os.Stat`s its socket path and compares the file
identity (`os.SameFile`) to the inode it bound. If the path is gone or now a
different inode, and **no client is connected**, it starts a 10-minute grace clock.
Any connected client, or a path that still matches, resets the clock. After the
grace elapses with nobody connected it self-probes: it dials its own socket, sends
an authed `server.capabilities`, and compares the reply's `instanceId` to its own. A
probe that reaches itself resets (a changed file identity that still leads back is
not orphaned). **Two consecutive failed probes** (60 seconds apart) trigger a
**graceful** shutdown, closing listeners, dropping clients, and stopping child
process groups (unless `-keep-children`), never a bare exit.

The behavior is identical on every OS (`os.SameFile` gives the file-identity compare
portably). It is a no-op when the daemon has no captured socket identity or no
`instanceId`. This is parity with the reference, not a divergence.

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

The reply's `id` is the request's id **decoded and re-encoded**. It is not the
bytes the client sent. The daemon accepts any JSON value and returns it
canonicalized: a number round-trips through a float64
(`1.0` → `1`, `1e2` → `100`, `12345678901234567890` → `12345678901234567000`), and
an object comes back with its keys sorted (`{"b":1,"a":2}` → `{"a":2,"b":1}`).
Integers, strings, arrays and `null` are unchanged. A client that matches replies
by the id *text* must compare the decoded value instead.

Results are **ordered structs, never maps** — field order below is the wire
contract.

### Error codes

| code | meaning |
|---|---|
| `-32700` | parse error — malformed JSON line (response `id` is `null`) |
| `-32600` | `Invalid JSON-RPC version` — `jsonrpc` absent or != `"2.0"` |
| `-32601` | `Invalid method format: <m>` (method has no `.`), `Unknown namespace: <ns>` (well-formed but unknown namespace), or `Unknown method: <ns>.<m>` (known namespace, unknown method) |
| `-32602` | invalid params (see per-method messages) |
| `-32603` | internal error (e.g. `open <path>: no such file or directory`); also a **recovered handler panic** → `recovered panic: <v>` |
| `-32003` | `stdin offset gap: offset ahead of applied bytes` — `process.stdin` with an `offset` past the applied high-water (added in `7c2f88d`) |
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
| protocol | `recovered panic: <v>` | -32603 (claustrum-only; see below) |
| files.stat / files.read | `stat <path>: <reason>` | -32603 (any stat failure other than ENOENT) |
| files.read | `files.read: path is a directory` | |
| files.read | `files.read: file exceeds maxBytes` | |
| files.read | `files.read: not a regular file` | D4 opt-in only |
| files.list | `open …: no such file or directory` | -32603 (missing dir) |
| files.list | `open <p>: not a directory` | -32603 (a non-directory; `7d193f89` opens with `O_DIRECTORY`, was `readdirent …`) |
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
| git.status | `baseRepo is required` | -32602 (`7d193f89`; `baseRepo` is now required) |
| git.status / git.list_branches | `<go error>` e.g. `exit status 128` | -32603 (git failed, stdout parse) |
| git.status / git.list_branches | `signal: killed` | -32603, D5 opt-in only |
| git.worktree_create | `branchName is required` | |
| git.worktree_create | `not a git repository` | in `error`, `errorCode:"not_a_repo"` |
| git.worktree_create | `refusing to create worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; … / already exists, …}` | in `error`, `errorCode:"unsafe_path"` (`7d193f89` containment; the spelling refusal is Windows-only and precedes containment) |
| git.worktree_create | `refusing to create worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error`, `errorCode:"symlinked_component"` — a symlinked ancestor component under the repo (`7d193f89`) |
| git.worktree_create | `failed to create parent directory: "" does not name a directory` | in `error`, `errorCode:"mkdir_failed"` (empty `worktreePath`) |
| git.worktree_create | `git worktree add failed: <combined output>` | in `error`, `errorCode:"worktree_add_failed"` |
| git.worktree_create | `git worktree add timed out after <n>ms (deadline expired {before the checkout started / during the checkout): <git error> / after the checkout finished})` | in `error`, `errorCode:"timeout"` — caller-supplied `timeoutMs` (`4534d86`); absent/0 arms no deadline |
| git.worktree_remove | `refusing to remove worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; …}` | in `error` (no `errorCode`; `7d193f89` containment; the spelling refusal is Windows-only and precedes containment) |
| git.worktree_remove | `refusing to remove worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | in `error` (no `errorCode`) — gates the os.RemoveAll fallback off a planted link (`7d193f89`) |
| git.worktree_remove | `refusing to remove worktree: <p> is locked (git worktree lock); unlock it to remove it` | in `error` (no `errorCode`) — `7d193f89` refuses a LOCKED worktree (`success:false`) and leaves it in place; the message is fixed regardless of the lock reason. Pre-`7d193f89` the reference deleted it via the fallback and answered `success:true`. |
| git.worktree_remove | `failed to remove worktree: "" does not name a directory` | in `error` (empty `worktreePath`) |
| git.worktree_remove | `failed to remove worktree: <git output>; manual cleanup also failed: <err>` | in `error` (only if manual cleanup also fails) |
| git.worktree_remove | `worktreePath must not be or contain the home directory: …` | D2, in `error` — behind `7d193f89` containment on the default branch (fires only if a repo is an ancestor of home); the active home guard on the `external_root` branch |
| git.worktree_remove | `git worktree remove timed out after <dur>; no cleanup was attempted, and git may have partially removed the worktree` | D5 opt-in, in `error` |
| process.spawn | `Process ID is required` / `Command is required` | |
| process.stdin | `Invalid base64 data` / `Process not found` / `Process not running` | (checked in that order after decode) |
| process.stdin | `stdin offset gap: offset ahead of applied bytes` | -32003 |
| process.killAndWait / process.reattach | `Process ID is required` / `Invalid params` | |

`-install` reports failures inside the `__INSTALL_RESULT__` facts line as
`cliError` strings, not via exit code — catalogued in the `-install` section
below.

### Handler panic recovery

The per-request goroutine wraps dispatch in `recover()`. It therefore catches a
panic in any handler, and the daemon does not crash. The reply is
`{"error":{"code":-32603,"message":"recovered panic: <v>"}}`, and the daemon logs
`[Server] recovered panic: method=<m> id=<id>: <v>`.

**This frame is claustrum's own. It is not a statement about the wire.** No input
is known to reach a handler panic: extensive fuzzing found none, and each of
claustrum's own panic sites is an unreachable stdlib guard or an
already-bounds-guarded slice. `-32603` is the JSON-RPC 2.0 *Internal error* code.
The message prefix, log line, and id rendering are claustrum's own conventions.
They are documented so that an operator who sees the frame knows what it means.
They are not a compatibility guarantee.

### Validation precedence

The daemon checks a request in the order **parse → auth → version → method →
params**:

- The daemon validates auth *before* the `jsonrpc` version. A request that fails
  both (no `auth` *and* a missing/wrong `jsonrpc`) reports `-32001 Unauthorized`,
  not the version error.
- **`server.shutdown` is the exception.** The daemon skips auth for it entirely, so
  a shutdown frame that is missing both `auth` and `jsonrpc` gets `-32600 Invalid
  JSON-RPC version` (the version gate still applies), and the daemon stays up.

### Params presence and typing

Every `files.*` / `git.*` / `process.*` method requires a `params` object.
`server.*` methods take no `params`. The daemon ignores a mistyped `params` on a
`server.*` method, and the call succeeds.

- **Absent** `params` → `-32602 Invalid params`. The daemon checks this *after*
  method existence, so an unknown method is `-32601` regardless.
- The daemon accepts an **empty** `{}` and then runs the method's own validation.
- **Mistyped** `params` — a wrong field type (`"maxBytes":"4"`, `"path":123`) or a
  non-object value (`"params":"x"` / `[…]`) — is `-32602 Invalid params`. The daemon
  does not coerce the value, and it does not ignore the decode error.
- **The daemon ignores unknown extra fields**, with one divergence in *how strictly*
  (D9). claustrum binds `params` into one struct per namespace (`pathParams`,
  `gitParams`). A field that is valid for the *namespace* but unused by *this*
  method therefore still takes part in the decode: a **type-mismatched** value there
  → `-32602` (e.g. `files.stat {"maxBytes":"{"}`, `git.status
  {"baseRepo":[1,2]}`). The reference binds only the field the specific method
  reads and ignores the rest, so it runs with defaults. Both daemons ignore a
  genuinely unknown key (a key in neither struct). Accepted divergence D9; see
  [`DIVERGENCES.md`](DIVERGENCES.md).

## Path handling

### A path must be valid UTF-8 to be addressable at all

Before any expansion or method logic, the JSON decoder replaces bytes that are not
valid UTF-8 with `U+FFFD`. A file whose **name** contains such bytes therefore
cannot be named in any request. The daemon answers about a path that does not
exist: `exists:false`, or a `chdir`/`stat` error that quotes the substituted name.
This is **parity, not a divergence** — both daemons inherit it from the JSON
decoder. See
[ARCHITECTURE.md → Inherited wire bytes](ARCHITECTURE.md#inherited-wire-bytes).

### Tilde expansion in path params

claustrum expands a leading tilde in every path-bearing param before the method
runs: `files.*` `path`, `extract_tar`'s `archivePath` / `destDir`, `git.*` `path` /
`baseRepo` / `worktreePath`, and `process.spawn`'s `cwd`. Branch names are refs,
not paths, so claustrum never expands them. claustrum replaces a leading `~` with
the daemon user's home directory, and **then cleans the remainder lexically**. Bare
`~` is the exception: it returns home verbatim (uncleaned).

| sent | reference replies | absolute-form control |
|------|-------------------|-----------------------|
| `~` | `<home>` — **verbatim, not cleaned** | n/a |
| `~/` | `<home>` (trailing separator stripped) | unchanged |
| `~/f.txt` | `<home>/f.txt` | unchanged |
| `~//f.txt` | `<home>/f.txt` (doubled separator collapsed) | `<home>//f.txt` |
| `~/a/./b` | `<home>/a/b` (`.` resolved) | `<home>/a/./b` |
| `~/a/x/../b` | `<home>/a/b` (`..` resolved **lexically**) | `<home>/a/x/../b` |
| `~user/f`, `/tmp/~/f`, `$HOME/f` | unchanged — not expanded | n/a |

Two consequences:

- **Bare `~` is the exception.** A `HOME` of `/home/me/` echoes back with its
  trailing slash while `~/` under the same `HOME` does not.
- **The cleaning is lexical, and tilde-form only.** With `~/link -> b/c`, the
  reference reads `<home>/x.txt` for `~/link/../x.txt` while the absolute spelling
  walks the symlink and reads `<home>/b/x.txt`. Same request, different file.

**Windows behaves the same way in Windows separator terms** (home from
`USERPROFILE`, not `HOME`):

| sent | reference replies |
|------|-------------------|
| `~\a7` | `~\a7` — **not expanded**; `~\` is not a tilde form |
| `~/a1` | `<home>\a1` — `/` rewritten to `\` |
| `~/a4\x\..\w` | `<home>\a4\w` — `\` **is** a separator for `..` |
| `~/a5/` | `<home>\a5` |
| `~//a6` | `<home>\a6` |
| `~` | `<home>` verbatim — a home of `C:\h\` keeps its trailing `\` |

**The expanded spelling is wire-visible on eight frames**, so it is contract.
`git.worktree_create` reflects `worktreePath` into `result.path` and into git's
error text. The expanded string also appears in the error text of `files.stat`,
`files.read`, `files.list`, `files.validate`, `files.extract_tar` and
`process.spawn`. Two places do *not* carry the spelling: `files.list` entry paths
are re-joined, and `git.info`'s `root` comes from git's own output. On a
trailing-separator spelling the difference is a change of *verdict*. POSIX
`stat("f.txt/")` gives `ENOTDIR`, so when the daemon removes the separator, a
`-32603` error frame becomes a success frame. Pinned by ids 15, 16, 18 and 19 of
`testdata/socket_tilde_expansion.golden.json` (id 17 sends `~//` to `files.list`
and is documentary only).

### Stat failures other than "does not exist"

`files.stat`, `files.read` and `files.validate` distinguish a path that is
**absent** from one that could not be examined:

- A genuine `ENOENT` is the "does not exist" answer in each method's own shape —
  `exists:false`, `content:"" exists:false`, and `valid:false` with
  `error:"Path does not exist"` respectively.
- **The daemon reports any other stat failure** with the underlying message.
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
```

Reference `7c2f88d` added `process.killAndWait` between `process.kill` and
`process.reattach` (19 methods). `7d193f89` then removed `server.version`,
bringing the set to 18: calling `server.version` now answers
`-32601 "Unknown method: server.version"`. (The `-version` CLI flag is a separate
surface and still prints the daemon's version.)

### server.*

| method | params | result |
|---|---|---|
| `server.ping` | — | `{"pong":true}` |
| `server.capabilities` | — | `{"version":"<id>","methods":[…18…],"instanceId":"<32-hex>","startedAt":<unix-ms>,"features":["process.stdin.offset","git.status.baseRepo","git.worktree_create.timeoutMs","git.worktree.external_root","server.instance_id"]}` (`git.worktree.external_root` is omitted on Windows; `git.worktree_create.timeoutMs`, `instanceId`/`startedAt` are present on every OS) |
| `server.shutdown` | — | `{"ok":true}` — the daemon replies, then stops and the connection closes (delivery races the teardown, so the reply is best-effort on the wire; see below) |

- **`server.version` was removed in `7d193f89`** — it now answers
  `-32601 "Unknown method: server.version"` like any other unknown method.
- **`instanceId` and `startedAt`** (added `4534d86`) sit between `methods` and
  `features`. `instanceId` is a 32-hex string and `startedAt` is the daemon's boot
  time in unix milliseconds. Both are present on every OS. claustrum generates
  `instanceId` from 16 crypto/rand bytes at startup and stamps `startedAt` then,
  echoing both on the capabilities reply for parity.
- **`features` array** (added `7c2f88d`) follows `instanceId`/`startedAt` and
  advertises optional extensions. `process.stdin.offset` (the resumable/idempotent
  stdin contract) landed first; `7d193f89` added `git.status.baseRepo` and
  `git.worktree.external_root`; `4534d86` inserted `git.worktree_create.timeoutMs`
  before `git.worktree.external_root` and appended `server.instance_id` (always
  last, every OS). On unix `git.worktree.external_root` is present; **on Windows it
  is omitted** — the reference gates the external-worktree capability off on Windows
  (measured against `7d193f89`) and drops the feature from its Windows capabilities
  frame, so claustrum matches. `git.worktree_create.timeoutMs` is present on every OS.
- **`server.shutdown` is not authenticated** — see [Authentication](#authentication).

### files.* (param: `path`)

#### files.stat
`{path}` → `{"exists","isDir","size","mode":"-rw-r--r--"}`
- Missing path → `{exists:false,isDir:false,size:0,mode:""}`.

#### files.list
`{path}` → `{"entries":[{"name","path","isDir"},…]}` (name-sorted)
- **The daemon omits hidden entries.** It skips any name that begins with `.`
  (`.git`, `.env`), which matches the reference.
- The daemon resolves `isDir` with **`Stat`, so it FOLLOWS symlinks**: a symlink to
  a directory is `isDir:true`, and a dangling symlink is `isDir:false`.
- Missing dir → `-32603 open …: no such file or directory`.

#### files.read
`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`
- `content` is **raw text**, not base64.
- Missing file → `{content:"",exists:false}` (not an error).
- A directory → `-32602 files.read: path is a directory`.
- Size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`.
- **`maxBytes` absent, `0`, or negative → the cap is `262144` (256 KiB), not
  "unlimited".** A file of 262144 bytes reads, and a file of 262145 bytes errors.
  The daemon honors a positive `maxBytes` verbatim, above or below the default.
  The cap uses the stat size. On linux that size is `0` for every non-regular
  kind, so the cap never bounds a FIFO, socket or device on either binary.
- **Non-regular files: opt-in guard D4.** Off by default (parity): the reference
  reads `/dev/null` as `{"content":"","exists":true}` and blocks on a writerless
  FIFO, and it refuses neither. Set `-files-read-regular-only` (or the
  `files-read-regular-only` config key), and every non-regular path answers
  `-32602 files.read: not a regular file` — a frame the reference never produces.
  The predicate is `Mode().IsRegular()`. It is whole and not narrowable, because
  `/dev/null` and `/dev/zero` are indistinguishable by mode. The full measurement
  and rationale are in [`DIVERGENCES.md`](DIVERGENCES.md) → D4.

  | path | reference **=** claustrum at the default | with `-files-read-regular-only` |
  |---|---|---|
  | **CONTROL** a regular file | `{"content":"…","exists":true}` | *(unchanged — guard does not apply)* |
  | **CONTROL** a regular file over `maxBytes` | `-32602 files.read: file exceeds maxBytes` | *(unchanged)* |
  | a FIFO, writer paired | `{"content":"<bytes written>","exists":true}` | `-32602 files.read: not a regular file` |
  | a FIFO, no writer | **no frame until a writer opens** | `-32602 files.read: not a regular file` |
  | `/dev/null` | `{"content":"","exists":true}` | `-32602 files.read: not a regular file` |
  | a bound `AF_UNIX` socket | `-32603 open <p>: no such device or address` *(linux; darwin/amd64 says `operation not supported on socket` — a per-OS stdlib difference, identical between binaries on each OS)* | `-32602 files.read: not a regular file` |
  | an unreadable character device (`/dev/console`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |
  | an unreadable block device (`/dev/nvme0n1`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |

  The two device rows assume a **non-root** daemon, because they are permission
  failures. The opted-in column is **measured** for the FIFO and `/dev/null` rows.
  For the socket row and the two device rows it is **entailed** by a false
  `Mode().IsRegular()`, and was not run separately.
  The default gives up two things. A writerless FIFO parks a request goroutine and
  a descriptor until a writer arrives. An unbounded device read (`/dev/zero`) grows
  the daemon until the kernel OOM-kills it. Both are the reference's own behaviour,
  and both are measured (forensics condensed out of the committed docs).

#### files.validate
`{path}` → `{"valid":bool,"isDir":bool[,"error"]}`
- Missing path → `{valid:false,isDir:false,error:"Path does not exist"}`.

#### files.extract_tar
`{archivePath,destDir}` → extracts a **gzip** tar → `{"success":true,"fileCount":<n>}`

Side effects — deliberate, **not visible in the frame**:
1. **The daemon wipes `destDir`** (`os.RemoveAll`) and then recreates it before it
   unpacks. Extraction is idempotent and destructive.
2. Entries get **owner-only fixed modes**: files `0600`, dirs `0700`. An executable
   `0755` entry still lands `0600`.
3. On success the daemon writes an **empty `.synced` marker** at the `destDir`
   root. It does not count that marker in `fileCount`.
4. **The daemon consumes `archivePath`.** Once it opens the archive, it removes the
   file on *every* outcome (success, bad gzip, or unsafe path).

Errors. Unless a line says otherwise, each error goes in the `error` field with
`fileCount:0`, which has no `omitempty`:
- Missing params → `-32602 archivePath and destDir are required`.
- Non-absolute/root `destDir` → `destDir must be an absolute, non-root path: …`.
  The daemon rejects this before it opens the archive, so it does **not** consume
  the archive. "Root" is the platform's own notion (`/` on Unix; a drive root `C:\`
  or UNC share root `\\server\share\` on Windows). The root test and the
  `filepath.IsAbs` test share one branch and one message. Whether the reference
  refuses a root `destDir` at all is **not measured**. Our own consequence — a
  recursive delete of the volume — justifies the guard, not a claim about the
  reference, so it is neither parity nor a divergence entry.
- **`destDir` is, or contains, the home directory** → `destDir must not be or
  contain the home directory: …`. This is **intentional divergence D2**: the
  reference wipes `$HOME` on `"destDir":"~"`. The test is *containment*. The daemon
  refuses home and any ancestor of home, and accepts anything **under** home
  (`~/.claude/…`). See [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- Bad gzip → `gzip: …`.
- Zip slip → `unsafe path in archive: <entry>`. The daemon allows a `../` that
  resolves back inside `destDir`.
- Non-regular/non-directory entry (symlink, hardlink, device, fifo) →
  `unsupported tar entry type <c>: <entry>`. `<c>` is the tar typeflag char
  (symlink=`2`, hardlink=`1`).
- **Total uncompressed bytes over the opt-in cap** → `extraction size limit
  exceeded`. This is **not reachable by default**, because the cap is `0` (off),
  which matches the reference. Intentional divergence D3; see the flags table under
  `-serve` and [`DIVERGENCES.md`](DIVERGENCES.md) → D3.
- clean/mkdir/marker failures → `clean destDir: …` / `mkdir destDir: …` /
  `write .synced: …`.
- Target is an existing directory → `create <entry>: open <target>: is a
  directory`.
- The daemon cannot create the parent (e.g. an earlier entry wrote a file where
  this entry needs a directory) → `mkdir parent <entry>: <os error>`. Only the
  `mkdir parent <entry>: ` prefix is contract. The tail is the OS's. Both `create
  <entry>: ` and `mkdir parent <entry>: ` name the **archive entry**, not the
  resolved target.

### git.* (param: `path` = repo dir; worktree ops use `baseRepo`)

#### git.info
`{path}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""}`

- The daemon reads `branch` with `symbolic-ref`, so it works on an **unborn HEAD**
  (an empty repo gives the init branch name, e.g. `master`). A **detached HEAD**
  gives `branch:"detached:<short-sha>"`.
- `root` is the absolute repo top-level (`git rev-parse --show-toplevel`). It stays
  the same even when `path` is a subdirectory (added by reference `7cbfa471`).
- `7c2f88d` added `repoSlug` and `defaultBranch`. Both are **always present** (an
  empty string when undeterminable), including on the non-repo body.
- `repoSlug` is `owner/repo` from `remote.origin.url`. The daemon populates it
  **only for a canonical `github.com` remote**. Rules (measured across 42 URL
  shapes):
    - **Scheme** must be `https`, `http`, `ssh`, `git`, or absent (scp-like
      `[user@]host:owner/repo`). `git+ssh://` and `file://` → `""`.
    - **Host** must equal `github.com` case-insensitively. `www.github.com`,
      trailing-dot `github.com.`, a port (`github.com:443`), GitLab, Bitbucket and
      self-hosted GHE all → `""`. The daemon strips userinfo.
    - **Path** must be exactly two non-empty segments after one optional trailing
      `/` and one optional `.git`.
    - **Owner**: alphanumerics with *interior* hyphens only (`ac-me`, `ac--me`
      pass; `-acme`, `acme-`, `acme_corp`, `acme.co` do not).
    - **Repo**: alphanumerics plus `.`, `_`, `-`. It must not start with `-`, must
      not be `.` or `..`, and must not end in a **lowercase** `.wiki` (the check is
      case-sensitive and suffix-only, so `GIZMO.WIKI` and a repo named `wiki` are
      accepted).
- `defaultBranch` is what `refs/remotes/origin/HEAD` points to. It is empty when
  `refs/remotes/origin/HEAD` is unset.

#### git.status
`{path,baseRepo}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}`

- **`7d193f89` rebuilt this method around session worktrees.** `baseRepo` is now
  **required** — an absent one answers `-32602 baseRepo is required`. Status is
  reported only when `path` is a linked worktree whose main repository is
  `baseRepo`. Everything else — a plain path, a plain subdirectory, a nested
  repository, the repository root itself, a worktree of a *different* repository,
  and the right worktree named against the wrong `baseRepo` — answers the bare
  `{"isRepo":false,"clean":false}` (the full shape, unlike `git.info`).
- `changes` is `git status --porcelain --untracked-files=all --ignore-submodules=all`
  **stdout only**. Stderr warnings never appear. Lines are verbatim minus the line
  ending. The two-character XY column is **positional**, so the leading space of an
  unstaged-only change is data (`"M  a.txt"` staged vs `" M b.txt"` unstaged).
  `--untracked-files=all` is wire-visible: an untracked file inside an untracked
  directory is listed individually (`"?? sub/u.txt"`), not as the directory
  (`"?? sub/"`).
- **The first line is the exception.** The daemon trims the whole porcelain blob
  before it splits the blob, so only entry 0 loses a leading space. `[" M a1"," M
  a2"]` returns `["M a1"," M a2"]`. A client that parses by column must handle
  entry 0 separately.
- A failing git → `-32603` that carries the Go error string (`exit status 128`, not
  git's `fatal:` text). With opt-in D5 the same `-32603` can carry `signal: killed`.

#### git.list_branches
`{path}` → `{"isRepo":true,"branches":[…sorted…]}`
- Non-repo → `{"isRepo":false,"branches":[]}`.
- **stdout only.** A broken-ref `for-each-ref` warning must not become a branch.
- A failing `for-each-ref` → `-32603 exit status 128`. With opt-in D5 it can carry
  `signal: killed` (see D5 below).

#### git.worktree_create
`{baseRepo,branchName,worktreePath[,sourceBranch][,worktreeRoot][,timeoutMs]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>"}`
- The repo is **`baseRepo`**, not `path`. When `baseRepo` is absent, the daemon
  uses its cwd repo.
- Missing `branchName` → `-32602 branchName is required`.
- The resolved repo is not git → `{success:false,error:"not a git
  repository",errorCode:"not_a_repo"}`. The daemon checks this before the add.
- **By default (no `worktreeRoot`), `7d193f89` confines the worktree to inside the
  repository.** After the repo
  check, `worktreePath` must be absolute, carry no `..` component, sit strictly
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
- **`worktreeRoot` (the `external_root` capability).** When the client supplies
  `worktreeRoot`, the worktree is placed OUTSIDE the repository, at
  `<worktreeRoot>/<directory>/<name>` (exactly two levels under the root). **On Windows
  this capability is gated off:** any `worktreeRoot` is refused before any location
  check with `"refusing to {create,remove} worktree: <root> cannot be used: a custom
  worktree location is not supported on Windows hosts yet"` (`errorCode:"unsafe_path"`
  on create, none on remove). On unix the in-repo
  containment above is replaced by these checks (each `errorCode:"unsafe_path"`):
  `worktreeRoot` and `worktreePath` must be absolute and `..`-free, and `worktreePath`
  must sit exactly two levels under the root (`"<p> is not <worktree
  location>/<directory>/<name> beneath <root>"`). The root must be owned by the
  daemon's user (`"<root> is owned by uid <o>, not by you (uid <u>); …"`) and not
  writable by its group or by every user on the host (`"<root> is writable by <who>
  (mode <perm>); … chmod go-w"`). The `<directory>` level must not be a symlink and,
  unless already marked, must start out empty (`"<dir> already exists, is not marked as
  a worktree directory, and holds other files (for example \"<name>\"); … must start
  out empty …"`). On success the daemon writes a 285-byte `.claude-managed-worktrees`
  marker at the `<directory>` level. Independently, a `baseRepo` that itself sits under
  a managed-worktrees marker is refused `{success:false,error:"baseRepo is inside a
  managed worktrees directory …",errorCode:"nested_base_repo"}`.
- Other failure → `{success:false,error:"git worktree add failed: …",errorCode:"worktree_add_failed"}`.
  The tail is git's **combined** output, because the add writes its fatal to stderr
  and leaves stdout empty. For example: `"git worktree add failed: Preparing
  worktree (new branch 'dup')\nfatal: a branch named 'dup' already exists"`.
- **`timeoutMs`** (caller-supplied, added `4534d86`) bounds the add + checkout with a
  per-request deadline in milliseconds. Absent or `0` arms no deadline, so the reply
  is byte-identical to the default. A fired deadline answers
  `{success:false,error:"git worktree add timed out after <n>ms (…)",errorCode:"timeout"}`.
  The parenthetical is `deadline expired before the checkout started` when the add is
  killed (measured), `deadline expired during the checkout): <git error>` when the
  checkout (a `read-tree`) is killed (measured; the tail is git's own error, e.g.
  `signal: killed`), and `deadline expired after the checkout finished` for the
  near-unhittable window where the deadline expires just after the checkout returns
  (reproduced from the reference string). This is caller-activated, distinct from the
  operator-global `-git-timeout` divergence (D5), and applies to create only, not
  `git.worktree_remove`.
- `sourceBranch` omitted → the source defaults to the repo's current branch, and
  the daemon echoes it back. On an **unborn HEAD** the source resolves empty, the
  add infers an orphan branch and succeeds, and the result omits `sourceBranch`.

**Worktree population.** `git worktree add` checks out tracked files only, so the
daemon then seeds the new worktree. The copies are best-effort, and a failure never
fails the request:
- **`.worktreeinclude`** (repo root, `.gitignore` syntax) is an **include** filter
  over the git-ignored set: the daemon copies an untracked file only when it is
  **both** named by the manifest **and** ignored by git's standard rules — the
  intersection of `git ls-files --others --ignored --exclude-from=.worktreeinclude`
  and `git ls-files --others --ignored --exclude-standard`. A manifest match that
  git does not ignore is **not** copied, and without the manifest the daemon copies
  no untracked file. (`7d193f89`; at `5db5e4a` the daemon also copied every manifest
  match and copied `.claude/` recursively and unconditionally — `7d193f89` dropped
  both, so `.claude/` is now subject to the same manifest-and-ignored rule as any
  other path.)
- **The daemon skips symlinks.** **Manifest entries must be plain filenames.** The
  daemon silently skips a path that `git ls-files` C-quotes (tab, quote, backslash,
  non-ASCII). This is a reference limitation reproduced for parity.
- **The copies do not preserve the source mode.** The daemon creates them
  0666-subject-to-umask, so an executable arrives non-executable and a `0400`
  source is widened. This matches the reference. Treat the manifest as a way to
  name configuration, not secrets or scripts.
- An **opted-in `-git-timeout`** (D5) that kills the `git ls-files` skips **every**
  manifest-selected file, and the reply is still `{"success":true}` — a silent,
  wire-invisible loss. Off by default.

#### git.worktree_remove
`{baseRepo,worktreePath[,branchName][,worktreeRoot]}` → `{"success":true}` (lenient)

- **By default (no `worktreeRoot`), `7d193f89` confines the removal to inside the
  repository.** Before git runs,
  `worktreePath` must be absolute, carry no `..` component, and sit strictly under
  `baseRepo`; otherwise the reply is `{"success":false,"error":"refusing to remove
  worktree: <p> …"}` (no `errorCode`), with the same three reasons as
  `worktree_create`. An empty `worktreePath` is `{"success":false,"error":"failed to
  remove worktree: \"\" does not name a directory"}`. Only a path that passes these
  checks reaches git and the recursive-delete fallback below — so the fallback targets
  a path inside the repository, or, when `worktreeRoot` is supplied, one two levels
  under that root (the same external containment as `worktree_create`). An external
  remove additionally **verifies that `<p>` is a genuine registered worktree of
  `baseRepo`** before it may be deleted: `<p>/.git` must be a regular `gitdir:` pointer
  file naming `<baseRepo>/.git/worktrees/<name>` whose own record points back at `<p>`.
  Any other path is refused and LEFT IN PLACE — `{"success":false,"error":"refusing to
  remove worktree: <p> is not a worktree of <repo> (<reason>), so it is left in place;
  remove it by hand if it is a leftover"}` — where `<reason>` is one of `<p> has no
  .git file`, `<p>/.git is not a regular file`, `<p>/.git does not name a git dir`,
  `<p> carries a .git file that does not name this repository's own worktree admin
  directory`, or `<p> carries a .git file naming an admin directory whose own record is
  of a different worktree`. When `baseRepo`'s worktrees directory cannot be read the
  daemon cannot decide, and the reply is transient — `{"success":false,"error":"failed
  to remove worktree: could not verify that <p> is a worktree of <repo> (<detail>);
  retry"}`. A stale registration whose admin dir is gone is still removed, as is a `<p>`
  that is already gone (`success:true`). None of these paths reaches the recursive
  delete of an unrelated directory.
- The daemon runs `git worktree remove --force`. **`7d193f89` refuses a LOCKED
  worktree here:** git fails with `cannot remove a locked working tree`, and the
  reply is `{"success":false,"error":"refusing to remove worktree: <p> is locked
  (git worktree lock); unlock it to remove it"}` (message fixed regardless of the
  lock reason), leaving the directory in place. **For any OTHER non-zero git exit —
  an ordinary directory, a non-repo `baseRepo` — the daemon then removes
  `worktreePath` itself, recursively, and still answers `{"success":true}`.** So on
  a non-locked failure this method is a recursive delete of the caller-supplied
  `worktreePath`; treat `worktreePath` as a path you ask the daemon to remove, not
  as a filter. Both are **reference behavior**, matched deliberately — though the
  lock refusal is a `7d193f89` change (pre-`7d193f89` the reference deleted the
  locked worktree too, via the fallback). The reply carries
  `{"success":false,"error":"failed to remove worktree: <git output>; manual cleanup
  also failed: <err>"}` only when the manual cleanup *also* fails.
- A request that names a non-existent branch still answers a bare
  `{"success":true}` — hence "lenient".
- **A home-directory `worktreePath` is refused — now by the `7d193f89` containment,
  as parity.** A `~`-expanded home path is not strictly under `baseRepo`, so it is
  refused with the reference's `"…is not inside the repository…"` wording before git
  or the fallback. The claustrum-only D2 frame (`"worktreePath must not be or contain
  the home directory: …"`) is now behind that containment on this method's **default**
  branch and fires only in the exotic case of a repository that is itself an ancestor
  of home. On the `worktreeRoot` / `external_root` branch the in-repo containment does
  not apply, so there D2 is the **active** home guard. D2
  remains the primary guard for `files.extract_tar`, which gained no containment. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- **A relative `worktreePath` is refused upfront** (`"…is a relative path…"`), so it
  never reaches git or the fallback. Before `7d193f89` the daemon resolved a relative
  path twice — git with `-C <baseRepo>`, the manual cleanup against the daemon's
  working directory — and the fallback could delete a directory git never looked at.
  Containment closes that: **send an absolute path under the repository.**
- **The registration is pruned.** `7d193f89` removes `$GIT_DIR/worktrees/<name>`
  along with the directory, so `git worktree list` no longer shows it and a later
  create at the same path succeeds. (Before `7d193f89` neither binary pruned on the
  fallback path, so a re-create failed `already registered`; claustrum now reads the
  worktree's `.git` pointer before removal and drops the admin directory too.)
- **`gitTimeout` (D5) does NOT authorise the deletion**, and this whole timeout arm
  is off by default. When armed it answers
  `{"success":false,"error":"git worktree remove timed out after <dur>; no cleanup
  was attempted, and git may have partially removed the worktree"}` and removes
  nothing. See [`DIVERGENCES.md`](DIVERGENCES.md) → D5.

### process.* (the agent/MCP-hosting core)

The client supplies its own `id` (any string). The daemon delivers output as
id-less stream notifications, and **buffers** them for a later replay.

#### process.spawn
`{id,command[,args][,cwd][,env][,wantPid]}` → `{"success":true}`, then stream frames
- `args`: string[]. `env`: `{KEY:VAL}`, merged over the daemon environment.
- Missing `id` → `-32602 Process ID is required`. Missing `command` →
  `-32602 Command is required`.
- A request that reuses a still-live `id` succeeds and replaces the registry entry,
  like the reference. **Divergence:** claustrum also kills the now-orphaned previous
  process tree. It drops the subscribers first, so no stray frame arrives under the
  reused id. This is OS-level only and changes no wire byte. The reference leaves
  the old process running.
- **Session superseding (`4534d86` parity).** A `process.spawn` whose `args` name a
  stream-json CLI session — an `--input-format=stream-json` / `--output-format=stream-json`
  arg (or a bare `stream-json` arg) plus a valid `--session-id`/`--resume` token, the
  resume fallback suppressed by `--fork-session` — terminates any OTHER running process
  of the SAME session id. The superseded process is killed via the SIGTERM-then-SIGKILL
  path, and its exit frame reaches its client (the `killedBy:"client"` marker on that
  frame ships in a separate slice). A spawn with no session key, or a different session
  id, supersedes nothing. The eviction, the `client` kill reason, and the session-key
  rules above are measured against the reference; claustrum also serializes concurrent
  spawns of one session, matching an equivalent per-key lock seen in the reference.
- **`wantPid` opt-in (CT-1, claustrum-only).** With `"wantPid":true` the reply gains
  two fields **after** `success`: `{"success":true,"pid":<int>,"startTime":<number>}`.
  `pid` is the child's OS pid. `startTime` is the **daemon's wall clock (epoch
  seconds) captured at spawn**, and spawn and reattach return the identical value
  for the same process. It is an **opaque token** for PID-reuse / orphan detection.
  Compare a persisted daemon value against a later daemon value for the **same
  id**. **Do not** equality-compare it against an OS-read process start time
  (`psutil create_time`), because the two derivations differ. When `wantPid` is
  absent or `false`, the daemon omits both fields (`omitempty`), and the frame is
  byte-identical to `{"success":true}`. An older daemon ignores the unknown param
  (tolerant decode). See [`DIVERGENCES.md`](DIVERGENCES.md) → CT-1.

#### process.stdin
`{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}`
- `data` is **base64**. The daemon writes it to the child's stdin.
- The checks run in a fixed order (**decode → exists → running → offset**):
    - Invalid base64 → `-32602 Invalid base64 data`. The daemon returns this
      *before* it looks up the process, so an unknown id with a bad payload still
      reports the decode error.
    - Unknown id → `-32602 Process not found`.
    - Known but **exited** → `-32602 Process not running`.
- **`offset` / `applied` — the resumable-stdin contract** (added `7c2f88d`,
  advertised as `process.stdin.offset`). The reply **always** carries `applied`: the
  cumulative count of stdin bytes accepted for delivery (the high-water mark).
  `offset` is the byte position the caller believes this `data` starts at. `offset`
  makes stdin idempotent across reconnects:
    - **absent** `offset`, or `offset == applied` → the daemon appends, and
      `applied` grows by `len(data)`.
    - `offset > applied` → `-32003 stdin offset gap: offset ahead of applied bytes`.
      This is a hole that would drop input — resend from `applied`. The daemon
      enqueues nothing.
    - `offset + len(data) <= applied` (wholly applied) → **no-op**. The reply adds
      `"duplicate":true`, `applied` does not change, and nothing reaches the child.
    - partial overlap (`offset < applied < offset+len`) → the daemon writes only the
      fresh tail `data[applied-offset:]`, and `applied` advances to
      `offset+len(data)`. The daemon does not flag this as a duplicate.
  `applied` counts base64-**decoded** bytes, and it is never `omitempty` (the daemon
  emits it at 0). The daemon drops `duplicate` when it is false. A legacy client
  that never sends `offset` still works: it always appends.

#### process.kill
`{id[,signal]}` → `{"success":true}`
- Best-effort and **fire-and-forget**. It does not wait for the child to exit
  (contrast `process.killAndWait`).
- **How wide the signal reaches depends on the signal**, on Unix:
    - `KILL` goes to the whole **process group** (a negative pid), so the entire
      child tree dies.
    - **Every other signal** (`TERM`, `INT`, `HUP`, default) goes to the **direct
      child only**, and a backgrounded grandchild keeps running. A graceful
      `process.kill` does **not** kill the tree. Use `signal:"KILL"` or
      `killAndWait` with `escalate:true`.
  The split does not apply on Windows. There claustrum terminates the Job Object,
  which takes the tree either way.
- **Divergence:** claustrum skips the signal when the child has already exited,
  because the OS can recycle a reaped pgid. This is OS-level only, and the reply is
  identical.

#### process.killAndWait
`{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}`

Added by `7c2f88d`. It **blocks until the process is gone** (up to the grace) and
reports the outcome as a *result*. An unknown id is not an error:
- Missing `id` → `-32602 Process ID is required`. Absent `params` → `-32602 Invalid
  params`.
- Unknown id → `{"found":false,"died":false}`.
- Already exited → `{"found":true,"died":true,"alreadyExited":true}`. The daemon
  sends no signal.
- Live process → the daemon sends the graceful `signal` (default `SIGTERM`), and
  then waits up to the grace:
    - **`timeoutMs`** sets the grace. A non-positive or absent value gives the
      **3000 ms** default. The daemon honors a positive value verbatim **up to a
      30000 ms ceiling**, and clamps a larger value. `timeoutMs:45000` against a
      signal-ignoring child therefore answers after ~30 s. The `30000` ceiling is a
      black-box bracket `(29500, 30500]`. It is the only round value in that
      bracket, not a measured-exact figure.
    - **`escalate`** (default `true`). If the process is still alive after the
      grace, `true` escalates to a **process-group** `SIGKILL`, waits up to **7 s**
      for the reap, and adds `"escalated":true`. (Measured: `timeoutMs:500` against
      an unreapable child → the reference replies at 7.51 s.) The daemon sends the
      SIGKILL even when the graceful signal already killed the child, because a
      grandchild that holds the stdout pipe can keep the drain pending past the
      grace. `false` leaves the process running and reports
      `{"found":true,"died":false}` (no `escalated`, no SIGKILL), which spares the
      tree.
- A process that dies within the grace → `{"found":true,"died":true}` (no
  `escalated`).

#### process.reattach
`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`
- A missing or empty `id` → `-32602 Process ID is required`, the same frame
  `spawn` and `killAndWait` document (probed both ways: `"id":""` and `id`
  absent).
- The daemon replays buffered frames with **seq > fromSeq** (exclusive) to this
  connection, **transfers** the frame stream to it, and then returns the result.
- **The transfer is exclusive.** A reattach does not add a second listener. Any
  connection attached before stops receiving frames for that process. This is what
  makes a resume safe.
- **The cut is by `seq`, not by wall-clock.** The transfer point is the reported
  `lastSeq`. The old connection can still receive a frame `<= lastSeq` slightly
  after the reply, and never one above it. No frame reaches the old connection and
  is also absent from the new connection's replay. That is what `fromSeq` is for.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- **The daemon retains an exited process for ~15 minutes and then drops it**,
  together with its replay buffer. An id last seen longer ago therefore answers
  exactly like an unknown one, and `process.kill` on it still reports
  `{"success":true}`. The daemon never drops a **running** process. The sweep runs
  on a ~60-second timer *and* inline on every `process.spawn`. On the wire, the
  retention brackets only to `(45 s, 960 s]`. The exact 15 min and 60 s are
  pointer-class: no wire observable distinguishes them from other values in that
  bracket. Read `found:false` after a long gap as "finished and forgotten", not as
  "never existed".
- **`stdinApplied`** (added `7c2f88d`) is the process's cumulative applied-stdin
  byte count (§`process.stdin`). It is always present after `lastSeq`. A
  reconnecting client resumes stdin from this offset. **It is an acknowledgement,
  not a delivery receipt.** `process.stdin` returns before the child reads, so the
  daemon counts bytes accepted just before exit even though the writer never
  delivered them. A client that must know that data arrived confirms it in-band.
- **`wantPid` opt-in (CT-1).** With `"wantPid":true` **and** the process found, the
  reply appends `"pid":<int>,"startTime":<number>` **after** `stdinApplied`. It
  reports the same pid and startTime the spawn reported, so a client can confirm
  that it reattached to the same process and not to a pid-reuse. The daemon omits
  both fields otherwise.

### Stream notifications

```jsonc
{"type":"stream","processId":"<id>","stream":"stdout","seq":1,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"stderr","seq":2,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":0}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":-1,"signal":"SIGTERM","killedBy":"client"}
```

- `seq` is **per-process**. It starts at 1 and is monotonic across
  stdout/stderr/exit.
- `data` is base64 for stdout/stderr. The `exit` frame carries `exitCode` and no
  `data`. A signal-terminated child reports `exitCode: -1`, not `128+signo`.
- **`signal` and `killedBy`** (added `4534d86`) appear on the `exit` frame only,
  both after `exitCode` and both omitempty — a normal exit stays byte-identical.
  `signal` is the SIG-prefixed name of the terminating signal (`SIGTERM`,
  `SIGKILL`), read from the wait status; it is omitted on a normal exit and always
  on Windows, which has no signal on the wait path (measured on a Windows VM: the
  reference omits `signal` and still emits `killedBy` there). `killedBy` names who
  asked the daemon to kill the process: `client` for `process.kill` /
  `process.killAndWait`, `shutdown` for the shutdown/`killAll` sweep. `killedBy` is
  emitted on every OS. The `client` values and the `SIGTERM`/`SIGKILL` names are
  live-measured (the other mapped signal names derive from the same wait-status
  path and are not individually measured against the reference); the `shutdown`
  value is read from the reference by static analysis only, because the shutdown
  exit frame races connection teardown and is not client-observable.
- The `exit` frame waits at most **5 seconds** after the process exits for
  stdout/stderr to reach EOF. The daemon then closes the read ends and emits the
  frame anyway. This matters when the command leaves a **grandchild that holds the
  same pipe** (`npm run dev &`). The daemon does **not** forward output that the
  grandchild writes after the cap, because that write fails `EPIPE`. Until the
  daemon emits the frame, `process.reattach` still reports `running: true`. The flag
  flips with the frame, not with the process.
- Each stdout/stderr frame carries at most one **32 KiB** read. Larger output splits
  across frames. Concatenate `data` in `seq` order to reassemble it. The exact
  frame *boundaries* depend on pipe scheduling and are not stable. Only the
  reassembled bytes are stable.
- The replay buffer has a **bound of 16 MiB per process**. The daemon counts the
  **serialized frame including its trailing newline** (the bytes a subscriber would
  receive), not the base64 `data` alone. An exit frame therefore costs its envelope
  although it carries no `data`. The daemon drops frames oldest-first, whole frames
  at a time, once a new frame would exceed the cap. It always retains at least one
  frame, even a frame larger than the cap. `reattach{fromSeq:0}` therefore replays
  everything **still retained**, and not necessarily everything ever emitted.
  `firstSeq` is the floor. Compare it against the last `seq` you saw to detect a
  gap.
- A process **survives** the disconnect of the connection that spawned it. Another
  connection picks it up with `reattach`. This is the multi-attach / reconnect
  mechanism.

## Daemon lifecycle (flags)

One binary, five modes (`-serve`, `-bridge`, `-stop`, `-version`, `-install`).
Everything here is probe-verified against the reference unless it is marked
**claustrum-only**.

### Flags and config keys

Every opt-in divergence flag defaults to its zero value = **OFF**, which is
byte-identical to the reference. Each flag has a matching `claustrum.conf` key.
Claude Desktop owns the `-serve` / `-install` argv (a driver claim — see
[ARCHITECTURE.md → Driver claims and their
provenance](ARCHITECTURE.md#driver-claims-and-their-provenance)), so **the config
key is the reachable knob.** The precedence is: explicit CLI flag > config >
default. A disabled bound **bypasses the guard entirely**. It is never "a huge
limit".

| flag | config key | default | effect when set | mode |
|---|---|---|---|---|
| `-token-file <p>` | — | — | token source (read once, unlinked) | -serve |
| `-token-fd <n>` | — | `-1` | token from an open fd (claustrum-only) | -serve |
| `-metrics-addr <a>` | `metrics-addr` | `""` | Prometheus `/metrics` (claustrum-only, CT-3) | -serve |
| `-wire-log <p>` | `wire-log` | `""` | append every JSON-RPC frame to `<p>` as JSONL (claustrum-only, CT-3) | -serve |
| `-wire-log-max-string <n>` | `wire-log-max-string` | `512` | bytes kept per string value; `0` = whole payloads | -serve |
| `-keep-children` | `keep-children` | off | survive restart, POSIX-only (CT-2) | -serve |
| `-listen-pipe` | `listen-pipe` | off | named-pipe transport, Windows-only (CT-5) | -serve |
| `-max-extract-bytes <n>` | `max-extract-bytes` | `0` | cap `files.extract_tar` bytes (D3) | -serve |
| `-git-timeout <dur>` | `git-timeout` | `0` | deadline on git invocations (D5) | -serve |
| `-files-read-regular-only` | `files-read-regular-only` | off | refuse non-regular `files.read` (D4) | -serve |
| `-max-cli-bytes <n>` | `max-cli-bytes` | `0` | cap CLI decompress + download (D10) | -install |
| `-cli-probe-timeout <dur>` | `cli-probe-timeout` | `0` | `<cli> --version` deadline (D11) | -install |
| `-cli-download-timeout <dur>` | `cli-download-timeout` | `0` | download deadline (D12) | -install |
| `-libc-probe-timeout <dur>` | `libc-probe-timeout` | `0` | `ldd --version` deadline, linux only (D14) | -install |
| `-cli-keep <n>` | — | `3` | versions to retain on prune | -install |
| — (config only) | `version-override` | — | `-version` stdout rebrand (CT-3) | -version |

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

The binary self-daemonizes (it reparents to init / detaches), extracts the
login-shell PATH (Unix), and then runs the RPC server. On success it prints
`Claustrum remote server listening on <socket>` to stdout.

**Login-shell PATH extraction** (Unix) runs `$SHELL -l -i -c …` when `$SHELL` is an
executable file. Otherwise it runs the first usable of `/bin/zsh`, `/bin/bash`,
`/bin/sh` (**zsh first**, which matches the reference). The value reaches
`process.spawn` children as their `PATH` only. It never reaches the daemon's own
environment, so it never changes how the daemon resolves a `command`. The extraction
has a cap of **4 s**. On a timeout the daemon **discards** whatever the shell
printed, even a valid PATH, and children fall back to the inherited PATH.

**Token source** — required, and checked **in the detached child**, not in the
launcher:
- Both flags missing → the launcher daemonizes anyway, the child refuses to start,
  and the launcher reports its accept timeout after ~10 s: `claustrum: timeout
  waiting for daemon to accept on <socket>`, exit `1`. The specific reason
  (`claustrum: daemonized child requires --token-file or --token-fd`) reaches only
  the child's detached stderr. This is deliberate parity, because the reference
  exits 1 at ~10 s the same way. A zero-byte `-token-file` behaves identically.
- The daemon reads the token as a **line**. It strips one trailing `\n`/`\r\n`, and
  preserves other surrounding whitespace verbatim.
- A bad `-token-file` → `claustrum: read --token-file: <err>`, exit `1`.
- `-token-fd <n>` *(claustrum-only)* reads from an already-open fd (`0` = stdin), so
  this handoff never touches disk. The launcher forwards it to the detached
  child over an inherited pipe.

**Daemonize sentinel** *(internal; claustrum-namespaced)* — the re-exec marker is
**`CLAUSTRUM_DAEMON_CHILD`**, not the reference's `CLAUDE_SSH_DAEMON_CHILD`. The
reference name cannot serve here. A host that runs *inside* a real claude-ssh
session exports `CLAUDE_SSH_DAEMON_CHILD=1` ambiently, so the launcher would mistake
itself for the already-daemonized child. claustrum keeps the observable parity
separately: `daemonizeWithToken` still sets `CLAUDE_SSH_DAEMON_CHILD=1` in the
daemon's environ, so that variable propagates into `process.spawn` children (pinned
by `TestSpawnInheritsDaemonChildMarker`). claustrum unsets the internal marker
before it spawns.

**Claustrum-only extras** (off the wire; canonical detail in
[`DIVERGENCES.md`](DIVERGENCES.md)):
- **`-metrics-addr <a>` (CT-3).** Prometheus counters at `http://<a>/metrics`
  (connections, spawns/exits, reattaches, stream/stdin bytes). Off by default, with
  no listener. It counts only, and has **no auth**, so bind it to loopback. The
  daemon logs a bind failure (`[Server] metrics: …`), which is non-fatal.
- **`-wire-log <p>` (CT-3).** Appends every JSON-RPC frame, both directions, to `<p>`
  as JSONL — a diagnostic side channel that observes already-marshaled bytes, so a
  daemon logging emits frames byte-identical to one not. Off by default (no file, no
  work). `-wire-log-max-string <n>` bounds each string value (default 512; `0` keeps
  whole payloads, needed to reconstruct a session from stream frames). Credentials
  are redacted **by key** — the `auth` member and token-like env keys — but a secret
  a client embeds inside a payload string is not caught, so redaction is best-effort,
  not a guarantee. A capture holds whatever the client sent (`files.write`,
  `process.stdin`, the spawn env), so it is forced to `0600` on every open (append included) and belongs somewhere
  private. Each record carries the frame as a decoded `body` (structured, per-value
  truncated — a normalized view, so field order is not significant) or, at
  `-wire-log-max-string=0`, as `raw` (the frame verbatim, preserving the field order
  and number formatting that *is* the wire contract). An unopenable path is fatal,
  not silent.
- **`-keep-children` (CT-2; POSIX-only).** Off by default, so a graceful shutdown
  kills the whole child tree. When set, it leaves spawned children running across a
  restart, and logs `[Server] -keep-children: leaving <n> running child process(es)
  alive across shutdown`. The new daemon does **not** re-adopt them, and **the
  survivors lose their stdio** (stdin EOF; a stdout/stderr write gets SIGPIPE, or
  EPIPE for a child that ignores SIGPIPE, e.g. Node). It therefore suits only
  children that tolerate dead stdio. Windows ignores it and logs a warning
  (`[Server] -keep-children is not supported on Windows …`), because the Job Object
  terminates children regardless.
- **`-listen-pipe` (CT-5; Windows-only).** See [Named-pipe
  transport](#named-pipe-transport-windows-opt-in). The daemon logs a setup failure
  (`[Server] named-pipe transport: …`), which is non-fatal. The socket still serves.

**The opt-in divergences on this mode** are `-max-extract-bytes` (D3),
`-git-timeout` (D5) and `-files-read-regular-only` (D4). Off = parity. Their wire
frames appear in the method sections above (`files.extract_tar`, `git.status` /
`git.list_branches` / `git.worktree_remove`, `files.read`). See the flags table and
[`DIVERGENCES.md`](DIVERGENCES.md).

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

A simple relay — the thing an SSH session attaches to. It adds **no** auth. The
client that speaks through it supplies `"auth"` itself. It is **strict**: a dial
failure is a hard error — `claustrum: dial server: <err>` on stderr, exit `1`.

### -stop — ask a running daemon to shut down

```text
claustrum -stop -socket <p>          # no token needed, and none is read
```

`-stop` sends `server.shutdown` with **no `auth` member**, because that method is
not authenticated (see [Authentication](#authentication)). It is **best-effort**: a
missing or unreachable daemon is a silent no-op (exit `0`, no output), and `-stop`
reads and discards any reply. The daemon answers `server.shutdown` with
`{"ok":true}` and then stops — matching the reference — but delivery races the
teardown on both, so `-stop` may read that frame or an immediate EOF; either way it
prints nothing (it is a control command, not a relay).

**`-stop` unlinks the socket path on every exit path**, including when the dial
fails and it reached no daemon. This matches the reference, and it is destructive
on two arms: a stale socket with no listener, and a **live foreign
listener**. `-stop` removes a socket path it did not create, so a new client that
dials by path cannot reach that listener afterwards. The listener itself stays
alive. A conditional unlink would be a divergence, so it is a candidate not taken —
recorded under [Candidates considered but not
taken](DIVERGENCES.md#candidates-considered-but-not-taken).

> **Upgrading a live daemon.** A daemon still running from a build that predates the
> shutdown-auth-exemption change *does* require auth on `server.shutdown`. It
> answers `-32001` and keeps running. `-stop` discards the reply and exits `0`
> either way, so the caller sees success while the old daemon survives. Stop the old
> daemon before you upgrade, or kill it by PID once.

### -version

```text
claustrum -version                   # → claustrum <id> (built <time>)
```

**Intentional divergence: `version-override` via `claustrum.conf` (claustrum-only,
CT-3).** `claustrum.conf` is an optional `key = value` file. claustrum reads it from
the directory that holds the binary, and it gates the opt-in divergences above. An
**absent or malformed file gives stock behaviour**. If the file sets
`version-override` to a bare commit SHA (a 40-hex git SHA-1, the string the desktop
client pins; claustrum also accepts 64-hex; anything else is a no-op), the output
becomes:

```text
claustrum -version                   # → claude-ssh <sha> (via Claustrum <id>, built <time>)
```

This exists so that the desktop client treats an already-deployed claustrum as
up-to-date. That client decides whether to re-upload from a `<bin> --version` output
that matches `/claude-ssh\s+(\S+)/`. The override is **CLI stdout only**, not a
JSON-RPC frame, so it does not touch the wire contract. `server.capabilities`
still reports claustrum's own `<id>` (`server.version` was removed in `7d193f89`).
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
reaches the network **only with `-cli-url`**.

**`cliError` catalogue:**

| `cliError` | trigger |
|---|---|
| `installed cli at <path> is not runnable` | post-extraction `--version` probe failed (or timed out, D11) |
| `cli <v> missing and no --cli-url or --cli-zst provided` | cache miss (or a cache-hit probe timeout, D11) with no source flag |
| `checksum mismatch: expected=<x>, actual=<y>` | `-cli-checksum` verify failed (`-cli-url` always; `-cli-zst` only when supplied — D1) |
| `opening input: <err>` | `-cli-zst` read error |
| `decompressing: <err>` | bad zstd blob (e.g. `invalid input: magic number mismatch`) |
| `decompressing: decompressed CLI exceeds <n> bytes` | D10 cap, opt-in |
| `download failed: response exceeds <n> bytes` | D10 cap on the download body, opt-in |
| `download failed: <transport err>` | `io.Copy` transport error (e.g. `read tcp …: connection reset by peer`) |
| `download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)` | D12 download deadline, opt-in |
| `download stalled: no data for 60s after <got>/<total> bytes` | read-idle abort — no bytes for 60 s on the `-cli-url` body (always-on, `4534d86` parity, VM-measured) |
| `mkdir cli dir: <err>` | cli-dir uncreatable |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli version "…" collides with the install temp sweep` | D7 hardening |
| `cli version "…" collides with the install download blob` | version starting `.blob-` |
| `clearing stale dir at <path>: <err>` | occupied `cliPath` directory couldn't be removed |
| `staging file vanished before install: <err>` | a concurrent sweep took the staging file |

**Download progress + `fetch` stats (`4534d86`, `-cli-url`):**
- While downloading, `-install` prints `__INSTALL_PROGRESS__<json>` lines to stdout on
  a ~1 s ticker: `{"phase":"download","bytes":<n>[,"total":<m>]}`. A leading `bytes:0`
  line is always emitted; `total` carries the Content-Length and is dropped when the
  server sends none (chunked). The cadence is time-driven, so byte counts jump
  irregularly and there is no guaranteed final `bytes==total` line — a consumer treats
  these as progress, not a byte-exact sequence.
- The `__INSTALL_RESULT__` facts line gains a `fetch` object LAST (after `cliError`):
  `{"bytes":<n>,"ms":<n>,"longestPauseMs":<n>}` — bytes read, download duration, and
  the largest gap between reads. It appears whenever a `-cli-url` download was
  attempted (even a 0-byte 404) and is dropped on the `-cli-zst` / cache-hit paths.
- The `-cli-url` body has an always-on 60 s **read-idle abort** (reset on every byte)
  that fails the install with the `download stalled: …` cliError above. This is parity,
  not a divergence — the reference does it. It is a read-idle bound, not a total
  deadline, so a slow-but-progressing download completes (that total cap is the opt-in
  D12, off by default).

**Checksum + verify ordering:**
- claustrum verifies `-cli-checksum` on the `-cli-url` path **unconditionally**. An
  empty checksum still fails.
- **Verify happens BEFORE decompress — intentional divergence D13** (always-on,
  **unresolved**). The reference decompresses first, and claustrum checksums first.
  A blob that is both undecompressable and wrong-checksummed diverges on the
  string. A short artifact yields `checksum mismatch` where the reference says
  `decompressing: unexpected EOF`. A genuine interrupted transfer never reaches the
  checksum on claustrum at all (`download failed: <transport>` vs `decompressing:
  <transport>`). Both binaries fail the install either way. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D13.
- **`-cli-zst` checksum — intentional conditional divergence D1.** The reference
  never checksum-verifies the local SFTP-upload blob. claustrum verifies it **only
  when a `-cli-checksum` is supplied**, with the same `checksum mismatch` error, and
  it leaves the source blob intact. An absent or empty checksum stays trusting, so
  honest callers are byte-identical. See [`DIVERGENCES.md`](DIVERGENCES.md) → D1.

**Opt-in wall-clock bounds** — all three are off by default, so a stock claustrum
applies **none** of them, on linux or anywhere. At the shipped defaults no
claustrum-chosen `-install` bound applies. Only the stdlib transport clocks
(`net.Dialer{Timeout:30s}`, `TLSHandshakeTimeout:10s`) apply, and only on
`-cli-url`. Off = parity, because the reference showed no deadline at the durations
probed. See [`DIVERGENCES.md`](DIVERGENCES.md):
- **`-cli-download-timeout <dur>` (D12).** `0` = `http.Client{Timeout:0}`, which is
  no bound. When armed, it bounds the whole exchange. An honest download that is
  merely too slow therefore trips `download failed: context deadline exceeded (…)`
  as surely as a black hole does.
- **`-cli-probe-timeout <dur>` (D11).** `0` = no deadline on the `<cli> --version`
  runnability probe, on every platform. When armed, a CLI slower than the deadline
  diverges. After extraction it diverges as `installed cli at <path> is not
  runnable`, and claustrum deletes the staged binary. On the cache-hit check it
  diverges as a silent reinstall, or, with `-cli-url` and a timely replacement, as
  no `cliError` at all. It is a threshold, not a hang detector: an honest-but-slow
  CLI trips it too. The cached binary survives every failure before the rename.
- **`-libc-probe-timeout <dur>` (D14; linux only).** `0` = no deadline on `ldd
  --version`. Off linux the probe never runs. On linux it cannot fire on a host
  whose musl loader glob matches, because `detectLibcWith` returns before it spawns
  `ldd`. **Do not confuse it with `-cli-probe-timeout`.** The two names differ only in
  their `cli`/`libc` prefix, they have the same type, and main's `-install` arm
  resolves them in consecutive statements (pinned by
  `TestInstallArmWiresEachFlagToItsOwnGlobal`). `libc` build
  selection is a driver claim — see
  [ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance).

**Opt-in size cap (D10).** `-max-cli-bytes <n>` (or the `max-cli-bytes` config key)
governs **both** the decompressed CLI and the download body. `0` = off, which is
parity: the reference took a 600 MiB payload to the runnability check. claustrum
**streams the blob and never buffers it** — it keeps a path, not a `[]byte`, so the
staging retry can re-read it. "Cap off" therefore does not mean unbounded memory.
See [`DIVERGENCES.md`](DIVERGENCES.md) → D10.

**`-cli-version` hardening (claustrum-only):**
- **D6: must name a single path component.** The clearing step is an `os.RemoveAll`
  on `filepath.Join(cliDir, cliVersion)`. A version that escapes the cli-dir
  (`../victim`, or `link/1.0.0` through an intermediate symlink) therefore deletes
  unrelated data, and the reference destroys the target on both shapes. claustrum
  answers `cli version "…" must be a single path component` and touches nothing. It
  refuses `.`, `..`, `/` and `\` on every OS. claustrum uses a single-component
  check and not lexical containment, because containment accepts `link/1.0.0`, and
  `EvalSymlinks` would add a TOCTOU window. A final component that is itself a
  symlink stays legal, because `os.RemoveAll` unlinks it and does not follow it. The
  real client passes bare versions (`1.0.86`, `2.0.0-beta.1`, a commit sha,
  `latest`, `1.0.86+build.5` — all measured as accepted).
- **D7: must not collide with the orphan sweep.** The sweep claims `.fetch-*` and
  `*.zst`, and it runs after *every* attempted install. `-cli-version .fetch-x` or
  `1.0.zst` would therefore install, and the sweep would delete it moments later.
  Both binaries finish with an empty cli-dir and no `cliError`, and report a
  success that installed nothing. claustrum now answers `cli version "…" collides
  with the install temp sweep`. The sweep predicate and this check share one
  definition.

**Staging and cleanup:**
- claustrum stages the CLI at **`<cli-dir>/.fetch-<random>`** (mode `0600`) and
  renames it into place. It never stages at `<cliPath>.tmp`. This is one code path
  for `-cli-url` and `-cli-zst` alike. The orphan sweep matches `.fetch-*`, so it
  reclaims the litter of an interrupted install.
- A `-cli-url` download lands at **`<cli-dir>/.blob-<random>`** when the cli-dir
  exists. On a **first install it lands at `$TMPDIR/claustrum-fetch-<random>`**,
  because `fetchToFile` (`install.go`) runs before `ensureCLI` creates the
  directory. The `.blob-` prefix is deliberately different, so that the sweep and
  the `-cli-keep` prune (which counts every non-directory as a version) do not claim
  an in-flight blob. That is also why claustrum refuses a `-cli-version` that starts
  with `.blob-`. The install removes the blob on every path. Only a SIGKILLed
  download leaves it behind. No frame changes either way.
- **claustrum consumes the `-cli-zst` blob once decompression succeeds**, and not
  only on a fully successful install. An extracted CLI that fails the runnability
  check still costs the blob. claustrum leaves a blob that is not valid zstd alone.
- **claustrum clears an occupied `cliPath`, and that is not fatal.** `rename(2)`
  refuses to replace a non-empty directory, so claustrum removes it first. It
  removes it only when `cliPath` is a directory; a regular file, which an
  installed CLI always is, is replaced atomically. If claustrum cannot remove it →
  `clearing stale dir at <path>: <err>`. If the staging file has vanished →
  `staging file vanished before install: <err>`, and `cliPath` stays untouched. The
  end states match the reference for every destination shape (absent, regular
  file, non-empty directory).
- **The orphan sweep** removes `.fetch-*` and `*.zst` entries with one `os.Remove`
  per entry. It therefore clears files and *empty* directories, and leaves a
  non-empty `.fetch-dir/`. Unrelated files survive. The sweep runs whenever an
  install was attempted, and the `-cli-keep` prune runs only on success. claustrum
  stages its extract in this same `.fetch-*` namespace and holds it across the
  probe, so a concurrent install can reclaim another install's staging file.
  claustrum handles that with a **single retry** of the stage-verify-rename step,
  and does not narrow the sweep.
- claustrum runs `ldd` **only when the musl loader glob does not match**. On a host
  that carries `/lib/ld-musl-*.so.*` the marker decides, and no `ldd` starts.

### Behavior shared by every mode

- **Default socket** — when `-socket` is omitted, all modes fall back to
  `~/.claude/remote/rpc.sock`. `-serve` **creates** the parent directory (mode
  `0700`) if it is missing, so a bare `-serve` on a fresh machine works. `-bridge`
  and `-stop` do not create it. They fail with `connect: no such file or directory`
  when no daemon has run.
- **No mode given** →
  `claustrum: one of --version/--install/--serve/--bridge/--stop is required` on
  stderr, exit `2`, and no usage dump. An *unknown flag* gets the stdlib `flag`
  error plus the usage, exit `2`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the `-install` facts schema and the
deployment lifecycle, [`DIVERGENCES.md`](DIVERGENCES.md) for the full divergence
catalog and rules, and [EXAMPLES.md](EXAMPLES.md) for runnable snippets.
