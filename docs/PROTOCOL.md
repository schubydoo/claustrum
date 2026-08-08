# claustrum protocol reference

claustrum speaks newline-delimited **JSON-RPC 2.0** over an `AF_UNIX`
`SOCK_STREAM` socket. This document is the complete wire contract; it is also the
contract the validation battery checks byte-for-byte.

## Transport

- One JSON object per line (NDJSON). No length prefix, no binary framing.
- A single request line is capped at **1 MiB** (`bufio` max token = `1024*1024`): a
  line up to 1048575 bytes is served, 1048576+ closes the connection with no reply.
  Large `process.stdin` payloads must be chunked under this.
- `AF_UNIX` stream socket, created mode `0600` (owner only).
- The connection is **persistent**: it stays open after a response, and id-less
  stream notifications arrive on it asynchronously.
- A connection's requests are dispatched **concurrently** — responses may arrive
  out of request order; match them by `id`.

### Named-pipe transport (Windows, opt-in)

A **strictly additive claustrum extension** — the reference daemon has no such
transport. The `AF_UNIX` socket above is the default and the reference contract. As an
**opt-in, default-off** addition (enabled with `-serve -listen-pipe`, or
`listen-pipe=true` in `claustrum.conf`), claustrum *additionally* serves the
**exact same** NDJSON JSON-RPC dispatch over a **Windows named pipe**,
concurrently with the socket. It exists so a Windows client which cannot consume
an `AF_UNIX` socket (notably Python `asyncio`, whose Unix transports are
Unix-loop-only while its Windows Proactor loop natively supports named pipes) can
still connect. When the flag is off, behavior is byte-for-byte
identical to the reference; the wire contract, field ordering, and framing are
unchanged whether a request arrives over the socket or the pipe.

- **Windows-only.** The flag is ignored (with a warning) on every other platform,
  which serves JSON-RPC over the socket directly.
- **Name + discovery.** claustrum chooses the pipe name
  (`\\.\pipe\claustrum-<random-instance-id>`); the client treats it as opaque and
  learns it by reading **`rpc.pipe`** in the socket's directory (beside `rpc.sock`
  / `daemon.token`). `rpc.pipe` is written atomically **before** the pipe begins
  accepting and before the daemon prints its ready line, and is **removed on
  graceful shutdown** — the same lifecycle as the socket. The fixed name +
  socket-dir location are the discovery contract, so they are not configurable.
- **Stale-file invariant.** Because the name is per-boot-random, every `-serve`
  startup guarantees `rpc.pipe` exists **iff** a pipe is actively served this boot
  — mirroring the stale-socket clear. When the pipe is served the fresh name is
  written; when it is not (flag off, non-Windows, or the pipe failed to start), any
  leftover `rpc.pipe` from an unclean crash is removed, so a client can never read a
  stale file and dial a pipe that no longer exists.
- **Same auth.** Requests over the pipe carry the same in-band `"auth":"<token>"`;
  the `daemon.token` handshake is unchanged, so a client discovers the token and
  the pipe name the same way. The `server.shutdown` exemption below applies over
  the pipe too — it is the same dispatch.
- **Owner-only + local.** The pipe is local by two independent mechanisms: an
  owner-only DACL (SDDL `D:P(A;;GA;;;<current-user-SID>)` — GENERIC_ALL to the
  daemon user's SID and to no world principal), the named-pipe analogue of the
  socket's `0600` mode; **and** remote-client rejection at pipe creation
  (`FILE_PIPE_REJECT_REMOTE_CLIENTS`, set by go-winio's `ListenPipe`), so a client
  reaching it over SMB is refused regardless of the DACL. See
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

## Authentication

Every request carries a top-level `"auth":"<token>"` — **except
`server.shutdown`, which is not authenticated at all.** A shutdown frame whose
`auth` member is absent, empty, wrong, or valid all stop the daemon, and
`-stop` sends no `auth` member. This matches the reference and is load-bearing:
the Desktop client tears the daemon down with `server --stop --socket <sock>`
from a bare SSH command line, with no `CLAUDE_RPC_TOKEN` in its environment.
The exemption covers auth ONLY — a shutdown frame with a bad or absent
`jsonrpc` version is still rejected `-32600` and the daemon stays up. Every
other method rejects an unauthenticated request with `-32001`. The server's expected token
comes from `-token-file` (read once at startup, then **unlinked**) or `-token-fd`
(read from an open descriptor, forwarded to the daemon over a pipe — no temp
file). A bad or missing token →
`-32001 Unauthorized: invalid or missing auth token` (also logged
`[Server] Unauthorized request: method=…, id=…`).

**No claustrum mode reads `CLAUDE_RPC_TOKEN`** — not `-serve`, not `-bridge`, not
`-stop`. `-bridge` is a dumb relay and does **not** inject auth: whatever speaks
through it must include `"auth"` itself, obtaining the token from the
`daemon.token` handshake below or from whoever launched it. `-stop` needs no
credential at all. claustrum's only remaining dealings with the variable are to
*remove* it — unset before daemonizing, and stripped from every spawned child —
so a token never reaches a child through the environment.

### Token persistence (`daemon.token`)

Once the socket is listenable, the daemon writes the token to **`daemon.token`**
in the socket's directory (mode `0600`, written atomically via a
`daemon.token-*` temp file + rename), and **unlinks it on graceful shutdown**.
This lets a client reconnect to an already-running daemon and re-authenticate
after the original `-token-file` was unlinked / the `-token-fd` pipe closed —
the token would otherwise be unrecoverable. The write is token-source-agnostic
(it uses the in-memory token, so it works for both `-token-file` and
`-token-fd`) and best-effort: a failure is logged (`[daemon] failed to persist
token: …`) and non-fatal. Added by reference build `5db5e4a` and matched here
(off the JSON-RPC wire; the file sits beside the socket, not on it). An unclean
kill (`SIGKILL`/crash) leaves the file behind, since removal runs only on the
graceful `server.shutdown` / `SIGTERM` path.

### Daemon startup (`-serve`)

The `-serve` launcher **creates the socket's parent directory** if it is missing,
mode `0700`, and then **does not return until the daemon is accepting** on the
socket. It confirms this by dialing the socket and closing again, so a freshly
started daemon's log opens with a `New connection from: @` / `Connection
closed: @` pair from the launcher's own probe, before any real client appears.

The `0700` is measured directly on the reference under `umask 022`, with a `0755`
control to prove the fixture could have shown a different mode; earlier
observations all used `mktemp -d`, which creates `0700` by itself, so the mode
they reported was a fixture artefact rather than a reading of the reference.

What it waits for is the socket **path to exist** (polled every 20 ms, bounded at
**10 seconds**), not a successful dial, and it does **not** give up early when the
child dies — both deliberate, and both measured against `5db5e4a`:

| start | what the launcher sees | outcome |
|---|---|---|
| normal | path appears, confirming dial succeeds | exit `0` |
| socket path occupied by a directory | path exists **immediately** | exit `0`, in ~0.01 s (reference 0.08 s) |
| child can never bind (uncreatable parent dir) | path never appears | exit `1` at ~10.04 s (reference 10.06 s) |

On timeout the launcher prints
`claustrum: timeout waiting for daemon to accept on <socket>` to **stderr** and
exits `1`. On success it prints nothing and exits `0`.

The occupied-path row is why the wait polls for existence: a dial-based wait
would invert both rows, sitting out the full deadline where nothing ever accepts
and giving up early on a child that dies. The confirming dial's result is ignored
for the same reason.

Two things are *not* promised. The child's own startup errors reach the
launcher's stderr only when the daemon log could not be opened (a missing socket
directory), because the child then falls back to inherited stdio — normally they
go to `remote-server.log`. And exit `0` means the path appeared, not that a
daemon is healthy behind it, as the occupied-path row shows.

The practical guarantee is the one that matters to a client: after a successful
`-serve`, connecting immediately over SSH never loses the race.

### Daemon log (`remote-server.log`)

The `-serve` launcher creates **`remote-server.log`** in the socket's directory
(mode `0600`, a **fresh file on every start** — any existing log is unlinked and
recreated, not truncated in place, so the daemon never writes into a file it does
not own) and redirects the daemonized child's
**stdout and stderr** into it, so the launcher's own streams stay empty. The
first line is the ready banner, carrying no timestamp:

```
Claustrum remote server listening on /run/user/1000/claude/rpc.sock
2026/07/31 00:17:30 INFO  [Server] New connection from: @
```

If the existing log cannot be replaced — a sticky directory holding another
user's file — claustrum declines the log entirely and the daemon's output falls
back to the launcher's inherited stdio, rather than writing into a file another
user can read. **Intentional divergence (D8), and the reference is measured on
this path as of 2026-08-06**: given a root-owned, world-writable
`remote-server.log` in a `1777` directory, the reference **truncates it and
writes its own diagnostics in**, where claustrum leaves it untouched. Control:
the same fixture in a non-sticky directory, where both binaries unlink and
recreate the log — so the difference is about the refused replacement, not about
a daemon that failed to start.

**Not reachable on the deployed path**, which is why it is always-on rather than
opt-in: the socket directory is `~/.claude/remote/`, per-user and not
world-writable, so the fallback never fires there and the two binaries behave
identically. It fires only where the log's directory is shared.

⚠️ **This is hardening, not a vulnerability, and the difference matters for how it
is described.** Reaching it requires a local user who can already plant a file in
that directory. Claustrum declines because a daemon should not write into a file
it does not own — not because the reference is wrong. The precondition (a local
user who can already plant files there) is the same class claustrum's own
[SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md) puts
out of scope.

Unlike the socket and `daemon.token`, the log is **not removed on graceful
shutdown** — it outlives the daemon so a post-mortem stays readable. The fixed
name and socket-dir location are the deployment contract, so neither is
configurable. Matches the reference daemon; the banner word and the level tag on
subsequent lines are the intentional rebrand described under Logging.

## Message shapes

```jsonc
// request
{"jsonrpc":"2.0","id":<n>,"method":"<ns>.<method>","params":{…},"auth":"<token>"}
// success
{"jsonrpc":"2.0","id":<n>,"result":{…}}
// error
{"jsonrpc":"2.0","id":<n>,"error":{"code":<c>,"message":"…"}}
// id-less stream notification (server -> client)
{"type":"stream","processId":"<id>","stream":"stdout|stderr|exit","seq":<n>,"data":"<base64>","exitCode":<n>}
```

The reply's `id` is the request's id **decoded and re-encoded**, not the bytes the
client sent. Any JSON value is accepted, and it comes back canonicalized: a number
round-trips through a float64 (`1.0` → `1`, `1e2` → `100`,
`12345678901234567890` → `12345678901234567000`) and an object comes back with its
keys sorted (`{"b":1,"a":2}` → `{"a":2,"b":1}`). Integers, strings, arrays and
`null` are unchanged, so a client using ordinary ids sees nothing unusual — but a
client that matches replies by comparing the id *text* must compare the decoded
value instead. Probe-verified against the reference at `5db5e4a`.

### Error codes

| code | meaning |
|---|---|
| `-32700` | parse error — malformed JSON line (response `id` is `null`) |
| `-32600` | `Invalid JSON-RPC version` — `jsonrpc` absent or != `"2.0"` |
| `-32601` | `Invalid method format: <m>` (method has no `.`), `Unknown namespace: <ns>` (well-formed but unknown namespace), or `Unknown method: <ns>.<m>` (known namespace, unknown method) |
| `-32602` | invalid params (see per-method messages) |
| `-32603` | internal error (e.g. `open <path>: no such file or directory`); also a **recovered handler panic** → `recovered panic: <v>`, see below |
| `-32003` | `stdin offset gap: offset ahead of applied bytes` — `process.stdin` with an `offset` past the applied high-water (added in `7c2f88d`) |
| `-32001` | unauthorized |

### Handler panic recovery

The per-request goroutine wraps dispatch in `recover()`, so a panic in any
handler is caught rather than crashing the daemon. The reply is
`{"error":{"code":-32603,"message":"recovered panic: <v>"}}` and the daemon logs
`[Server] recovered panic: method=<m> id=<id>: <v>`.

⚠️ **This frame is claustrum's own, and it is the one entry in this document that
is not a statement about the wire.** No input is known to reach a handler panic
(extensive fuzzing found none, and claustrum's own panic sites are each either an
unreachable stdlib guard or an already-bounds-guarded slice), so the path is
unreachable and no client can provoke the frame. `-32603` is `codeInternal`, the
JSON-RPC 2.0 standard *Internal error* code; the message prefix, the log line and
the id rendering are claustrum's own conventions. It is documented here so an
operator who somehow sees it knows what it means — not as a compatibility
guarantee, and not as a claim about any other implementation.

### Validation precedence (probe-verified)

A request is checked in the order **parse → auth → version → method → params**:

- Auth is validated *before* the `jsonrpc` version: a request that fails both
  (no `auth` *and* a missing/wrong `jsonrpc`) reports `-32001 Unauthorized`,
  not the version error.
- Only once auth passes is `jsonrpc == "2.0"` enforced.
- **`server.shutdown` is the exception**, because auth is skipped for it
  entirely (see [Authentication](#authentication)). A shutdown frame missing
  *both* `auth` and `jsonrpc` therefore surfaces `-32600 Invalid JSON-RPC
  version`, not `-32001` — the version gate still applies to it, and the daemon
  stays up.

### Params presence and typing

Every `files.*` / `git.*` / `process.*` method requires a `params` object:

- **Absent** `params` → `-32602 Invalid params` — checked *after* method
  existence, so an unknown method is `-32601` regardless.
- An **empty** `{}` is accepted and runs the method's own validation.
- **Mistyped** `params` — a wrong field type (`"maxBytes":"4"`, `"path":123`)
  or a non-object value (`"params":"x"` / `[…]`) — is also
  `-32602 Invalid params`; the daemon does not silently coerce or ignore the
  decode error.
- **Unknown extra fields** *are* ignored — with one divergence in *how strictly* (D9).
  claustrum binds `params` into one struct per namespace (`pathParams`,
  `gitParams`), so a field that is valid for the *namespace* but unused by *this*
  method still participates in decoding: a **type-mismatched** value there →
  `-32602` (e.g. `files.stat {"maxBytes":"{"}`, `git.status {"baseRepo":[1,2]}`).
  The reference binds only the field the specific method reads and ignores the
  rest regardless of type, so it runs with defaults. A genuinely unknown key (in
  neither struct) is ignored by both. Accepted divergence (D9), found by
  differential fuzzing. ⚠️ The trigger is specifically a **type error** in a
  namespace field the target method does not read — a correctly typed extra field
  is ignored by both binaries. This bullet used to add "a real client never sends
  them"; that was an assertion with no measurement behind it, and Claude Desktop's
  per-method param set has never been enumerated against this binding.

### A path must be valid UTF-8 to be addressable at all

Before any expansion or method logic, the JSON decoder replaces bytes that are
not valid UTF-8 with `U+FFFD`. A file whose **name** contains such bytes
therefore cannot be named in any request: the daemon receives the substituted
name, and answers about a path that does not exist — `exists:false`, or a `chdir`
/ `stat` error quoting the name with `U+FFFD` where the byte was.

This is **parity, not a divergence** — both daemons inherit it from the same
place — so there is nothing to opt into or out of. It is recorded here because
the symptom ("the daemon says my file is missing, but it is right there") is
otherwise very hard to attribute. See
[ARCHITECTURE.md → Inherited wire bytes](ARCHITECTURE.md#inherited-wire-bytes)
for the rest of that class.

### Tilde expansion in path params (probe-verified)

Every path-bearing param is tilde-expanded before the method runs, and the rule
below applies identically to `files.*` `path`,
`extract_tar`'s `archivePath` / `destDir`, `git.*` `path` / `baseRepo` /
`worktreePath`, and `process.spawn`'s `cwd`. Branch names are refs, not paths, and
are never expanded.

A leading `~` is replaced with the daemon user's home directory, and **the
remainder is then cleaned lexically**. Measured at `5db5e4a` on 2026-08-02 by
reading the string the reference echoes back, with the equivalent absolute-form
request sent in the same run as the control:

| sent | reference replies | absolute-form control |
|------|-------------------|-----------------------|
| `~` | `<home>` — **verbatim, not cleaned** | n/a |
| `~/` | `<home>` (trailing separator stripped) | unchanged |
| `~/f.txt` | `<home>/f.txt` | unchanged |
| `~//f.txt` | `<home>/f.txt` (doubled separator collapsed) | `<home>//f.txt` |
| `~/a/./b` | `<home>/a/b` (`.` resolved) | `<home>/a/./b` |
| `~/a/x/../b` | `<home>/a/b` (`..` resolved **lexically**) | `<home>/a/x/../b` |
| `~user/f`, `/tmp/~/f`, `$HOME/f` | unchanged — not expanded | n/a |

Two consequences worth stating plainly:

- **Bare `~` is the exception.** It returns the home directory untouched, so a
  `HOME` of `/home/me/` echoes back with its trailing slash while `~/` under the
  same `HOME` does not. Cleaning it would diverge.
- **The cleaning is lexical, and it applies to the tilde form only.** With
  `~/link -> b/c`, the reference reads `<home>/x.txt` for `~/link/../x.txt` while
  the absolute spelling of the same path walks the symlink and reads
  `<home>/b/x.txt`. Same request, different file.

**Windows behaves the same way, in Windows separator terms.** Measured on Windows
11 against the reference at `5db5e4a` on 2026-08-02, same method and per-row
control:

| sent | reference replies |
|------|-------------------|
| `~\a7` | `~\a7` — **not expanded**; `~\` is not a tilde form |
| `~/a1` | `<home>\a1` — `/` rewritten to `\` |
| `~/a4\x\..\w` | `<home>\a4\w` — `\` **is** a separator for `..` |
| `~/a5/` | `<home>\a5` |
| `~//a6` | `<home>\a6` |
| `~` | `<home>` verbatim — a home of `C:\h\` keeps its trailing `\` |

Two Windows-specific notes: only bare `~` and a `~/` prefix expand, so a
backslash form passes through untouched; and the home comes from `USERPROFILE`,
not `HOME`. Every absolute-form control came back verbatim there too, so the
tilde-only rule holds on both platforms. The cleaning is therefore **not**
build-tagged to Unix.

**The expanded spelling is wire-visible on eight frames**, so this is a wire
contract and not an internal detail: `git.worktree_create` reflects
`worktreePath` verbatim into `result.path` and into git's error text, and the
expanded string appears in the error text of `files.stat`, `files.read`,
`files.list`, `files.validate`, `files.extract_tar` and `process.spawn`. Two
places that do *not* carry the spelling: `files.list` entry paths are re-joined,
and `git.info`'s `root` comes from git's own output. On a trailing-separator
spelling the difference is a change of *verdict*, not of formatting — POSIX
`stat("f.txt/")` is `ENOTDIR`, so cleaning the separator away turns a `-32603`
error frame into a success frame. Pinned by ids 15, 16, 18 and 19 of
`testdata/socket_tilde_expansion.golden.json`; id 17 sends `~//` to `files.list`
and is documentary only, since re-joined entry paths answer identically whatever
spelling they are sent.

> **Correction (2026-08-02).** Until this entry existed, the doc comment in
> `expandpath.go` was the only record of this behaviour, and it was wrong: it
> stated `~/` → `<home>/` and `~//f.txt` → `<home>//f.txt` as probe-measured, and
> concluded that no cleaning was applied. Those rows were asserted with
> `files.validate`, whose reply is `{valid,isDir}` and echoes no path — the probe
> could not have observed the difference it recorded. The values were inferred and
> both were wrong.

### Stat failures other than "does not exist"

`files.stat`, `files.read` and `files.validate` distinguish a path that is
**absent** from one that could not be examined:

- A genuine `ENOENT` is the "does not exist" answer in each method's own shape —
  `exists:false`, `content:"" exists:false`, and `valid:false` with
  `error:"Path does not exist"` respectively.
- **Any other stat failure is reported**, carrying the underlying message.
  `files.stat` and `files.read` return `-32603 stat <path>: <reason>`;
  `files.validate` keeps its result shape and puts that same text in its `error`
  field instead of `"Path does not exist"`.

Reachable reasons include `not a directory` (a path component is a regular file
— e.g. joining a path against a file, which needs no adversarial input),
`file name too long`, and `invalid argument` (a NUL byte in the path).
- `server.*` methods take no params, so a mistyped `params` on them is ignored
  and the call succeeds.

## Methods (19)

`server.capabilities` self-describes the set. Order as returned:

```
server.ping  server.version  server.capabilities  server.shutdown
files.list   files.validate  files.stat  files.read  files.extract_tar
git.info     git.status      git.list_branches  git.worktree_create  git.worktree_remove
process.spawn  process.stdin  process.kill  process.killAndWait  process.reattach
```

> `process.killAndWait` was added by the reference in `7c2f88d` (it sits between
> `process.kill` and `process.reattach`), bringing the set to 19.

### server.*

| method | params | result |
|---|---|---|
| `server.ping` | — | `{"pong":true}` |
| `server.version` | — | `{"version":"<id>","platform":"<goos>","arch":"<goarch>"}` |
| `server.capabilities` | — | `{"version":"<id>","methods":[…19…],"features":["process.stdin.offset"]}` |
| `server.shutdown` | — | *no response* — the daemon stops and the connection closes |

> **`features` array (added `7c2f88d`).** `server.capabilities` now carries a
> `features` array after `methods`, advertising optional protocol extensions a
> client may rely on. The sole entry is `process.stdin.offset` (the resumable/
> idempotent stdin contract — see `process.stdin` below). Always present.

> **`server.shutdown` is not authenticated** — see [Authentication](#authentication).
> A shutdown frame with an absent, empty, wrong or valid `auth` member all stop
> the daemon, and `-stop` sends no `auth` member at all. This matches the
> reference.
>
> This block previously documented the opposite as an intentional hardening:
> claustrum used to reject an unauthenticated shutdown with `-32001` and stay up.
> That was removed because it is not a free divergence — the Desktop client tears
> the daemon down with `server --stop --socket <sock>` from a bare SSH command
> line, with **no `CLAUDE_RPC_TOKEN` in its environment**, so a daemon that
> demands a token here cannot be stopped by its real client. The old note argued
> "honest callers are byte-identical" from `bridge.go` alone, without checking
> what the actual client sends.

### files.* (param: `path`)

#### files.stat

`{path}` → `{"exists","isDir","size","mode":"-rw-r--r--"}`

- Missing path → `{exists:false,isDir:false,size:0,mode:""}`.

#### files.list

`{path}` → `{"entries":[{"name","path","isDir"},…]}` (name-sorted)

- **Hidden entries are omitted** — any name beginning with `.` (`.git`, `.env`,
  …) is skipped, matching the reference daemon.
- `isDir` is resolved by **`Stat` — symlinks are FOLLOWED**: a symlink to a
  directory is `isDir:true`, a dangling symlink is `isDir:false`.
- Missing dir → `-32603 open …: no such file or directory`.

#### files.read

`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`

- `content` is **raw text**, not base64.
- Missing file → `{content:"",exists:false}` (not an error).
- A directory → `-32602 files.read: path is a directory`.
- Size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`.
- **`maxBytes` absent, `0`, or negative → the cap is `262144` (256 KiB), not
  "unlimited".** Probe-measured: 262144 bytes reads, 262145 errors. A positive
  `maxBytes` is honored verbatim, above or below that default — so the 256 KiB
  figure is a fallback, not a ceiling. ⚠️ **This is a regular-file contract**: the
  cap keys off the stat size, which is `0` for every non-regular kind measured on
  linux, so it never bounds a FIFO, socket or device on **either** binary — see
  below.
- **Non-regular files behave at the shipped default exactly as they do on the
  reference** — six such shapes measured, and "behave" not "read", because three of
  them error on both. An **opt-in** guard refuses them all instead with
  `-32602 files.read: not a regular file` — divergence D4, off by default, see below.

##### Non-regular files (opt-in divergence, D4)

**Off by default, and off is the parity position.** With
`-files-read-regular-only` (or `files-read-regular-only = true` in
`claustrum.conf`) claustrum refuses to read anything that is not a regular file.
The reference refuses none of them. **Every row's default-behaviour column is
measured on both binaries** — each run against `5db5e4a` and against claustrum at
the default in the same session, byte-identical throughout, modulo the temp path in
the socket row. Six of the eight rows are non-regular, and they split **three** ways
rather than two: two read, three error, and one (the writerless FIFO) neither — it
blocks. That three-way split is why this section says the shapes *behave* as the
reference does rather than *read*. (One further shape is measured but not tabled —
a FIFO over `maxBytes`, run at two sizes — so nine distinct shapes in all.)

⚠️ **Two claims here are entailed rather than measured.** One is in the table's
third column: for the socket and the two device rows, "an opted-in claustrum
answers `-32602`" follows from `Mode().IsRegular()` being false for them, and the
opted-in golden covers only the FIFO and `/dev/null`. The other is not in the table
at all — the reference's *reason* for ignoring `maxBytes` is inferred from its
frames rather than inspected, and is discussed below the table.

| path | reference **=** claustrum at the default | claustrum with `-files-read-regular-only` |
|---|---|---|
| **CONTROL** a regular file | `{"content":"…","exists":true}` | *(unchanged — the guard does not apply)* |
| **CONTROL** a regular file over `maxBytes` | `-32602 files.read: file exceeds maxBytes` | *(unchanged)* |
| a FIFO, writer paired | `{"content":"<the bytes written>","exists":true}` | `-32602 files.read: not a regular file` |
| a FIFO, no writer | **no frame until a writer opens** | `-32602 files.read: not a regular file` |
| `/dev/null` | `{"content":"","exists":true}` | `-32602 files.read: not a regular file` |
| a bound `AF_UNIX` socket | `-32603 open <p>: no such device or address` *(linux; on darwin/amd64 both binaries say `operation not supported on socket` instead — measured on macOS 26.5, a per-OS stdlib difference and identical between binaries on each OS. darwin/arm64 not run)* | `-32602 files.read: not a regular file` |
| an unreadable character device (`/dev/console`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |
| an unreadable block device (`/dev/nvme0n1`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |

⚠️ **The two CONTROL rows are load-bearing, not decoration.** A differential table
whose every row says "identical" cannot be told apart from a harness that never
discriminated. The first control proves the request reached both daemons; the
second proves the `maxBytes` arm still fires where it applies — which is what
makes "it never fires for a non-regular path" a finding rather than a broken probe.

⚠️ **The two device rows assume a NON-ROOT daemon.** They are permission failures,
so a root daemon does not merely see different text: `/dev/console` blocks on the
tty and a block device streams the disk into memory — the unbounded-read shape with
a descriptor attached. Reproduce them as the same unprivileged user, or expect a
different answer and do not read it as drift.

⚠️ **The `no writer` row was carried as DERIVED for claustrum and is now measured.**
The observation that settles it does not require waiting for a reply: a non-blocking
`open(O_WRONLY)` of the same FIFO succeeds only if a reader is already present. It
**succeeds** against a stock claustrum and against the reference, and returns
`ENXIO` against an opted-in claustrum — with a FIFO nobody read as the control,
which also returns `ENXIO`. So the parked reader is directly observable on both.

⚠️ **The predicate is `Mode().IsRegular()`, so the guard is whole — on or off, never
narrowed.** Permitting *only* the harmless character devices is not an option a
config key can express: `/dev/null` and `/dev/zero` are indistinguishable by mode,
so any predicate that admits the first admits the second. That is why D4 is a flag
rather than a smarter check.

The FIFO case is why the guard was written. A read of a FIFO with no writer blocks
in `open`, so the reference emits no frame for as long as that holds and a
frame-diffing comparison cannot see the request at all.

**It is not a permanent hang, and this document used to say it was.** Measured:
the reference replies `{"content":"<the bytes written>","exists":true}` the
instant a writer opens, and stays responsive on other requests throughout. The
control is a non-blocking
open-for-write of the same FIFO, which on POSIX succeeds only if a reader is
already present — it SUCCEEDS against the reference (a request goroutine really
is parked in the FIFO) and returns `ENXIO` against **an opted-in** claustrum (the
read was refused, so there is no reader). ⚠️ **That control discriminates only when
the guard is on**; against a stock claustrum it succeeds on both sides, because
both are parked — measured, and it is what promotes the table's `no writer` row
from derived to measured. `/dev/null` is the input that discriminates at the
default — instant on both, and `-32602` only when opted in. The earlier "never replies" came
from a probe that wrapped the read in `timeout 8` and never opened a writer; a
harness deadline shorter than the subject's own blocking behaviour records "no
reply" by construction.

So the guard's own case is real but narrower than stated: a client that reads a
FIFO nothing ever writes to waits indefinitely on the reference, and an opted-in
claustrum turns that into an immediate, actionable error instead.

The `/dev/null` row is the cost of the guard rather than a second decision, and it
is what made D4 opt-in: a client reading a character device is an **honest**
caller, the reference answers it, and Claude Desktop owns the `-serve` argv — so
with the guard always on there was no way through. ⚠️ Turning it off is a trade,
not a free fix. What an operator gives up is the two bounds the guard supplied:

- a FIFO with no writer parks a request goroutine **and a descriptor** until one
  arrives, plus the OS thread serving the blocking syscall. ⚠️ The fd is the part
  that scales badly: linux reserves the descriptor number *before* it blocks, so
  parked reads draw down `RLIMIT_NOFILE` for the whole wait — and the listener's
  `accept()` draws on the same table. Measured two ways: two parked opens push the
  next allocated fd from 3 to 5, and under `RLIMIT_NOFILE=16` one parked open costs
  exactly one of the 13 opens the unparked control achieves;
- an unbounded device read never reaches EOF, so the daemon grows until the kernel
  kills it.

⚠️ **Two further shapes are NOT measured on either binary, and are named here rather
than left to be discovered**: a writer that opens and then dribbles or never closes
makes the open *return*, so the read holds a descriptor **and** grows unbounded —
neither fixture covers it, because both writers close promptly. And each parked
`open(2)` pins an OS thread, so enough concurrent writerless-FIFO reads reach Go's
`maxmcount` (10000) and the runtime aborts. That second one needs `RLIMIT_NOFILE`
above ~10000 to be the binding constraint at all; at a typical 1024 soft limit the
descriptor table runs out first, which is the row above.

**Both are the reference's behavior too, and both are measured on it** (the costs
in claustrum's own runtime terms above are ours; what was measured on the reference
is the observable each produces):

| row | the reference | how we know |
|---|---|---|
| the FIFO park | blocks until a writer opens | frame absence, then a reply the instant a writer arrives — plus a non-blocking open-for-write of the same FIFO, which on POSIX succeeds only if a reader is already present, and does |
| the device OOM | **is OOM-killed the same way** | on `path:"/dev/zero"` under a **2 GiB** cgroup cap, both binaries dropped the connection with no frame and the kernel logged `Memory cgroup out of memory: Killed process` naming each binary at ~2091 MB anon-rss; the `/dev/null` control through the same launcher answered on both and logged nothing. ⚠️ The cap is 2 GiB and not the 512 MiB first run **because 512 MiB cannot discriminate**: it sits below any plausible internal bound, so a reference that capped its own read at, say, 1 GiB and answered a frame there would have looked identical. 2 GiB clears that range; neither binary caps at or below it |

⚠️ **`maxBytes` does not bound any of this, on either binary.** The cap keys off the
stat size, and that is `0` for every non-regular kind — measured on linux for a
FIFO (with bytes buffered and without), a bound socket, `/dev/null`, `/dev/zero`,
`/dev/console` and a block device. So it is inert for all of them: a FIFO carrying
100 bytes reads in full at `maxBytes:4`, and one carrying 300000 bytes reads in
full at the 256 KiB default, **identically on both binaries**, while the
regular-file control errors on both at the same `maxBytes`. That claustrum keys off
the stat size is its own `fi.Size()` check; on the reference it is what those rows
imply, not something inspected. This is parity, not a claustrum property, and it is
the reason the device row above is reachable at all. ⚠️ POSIX leaves a FIFO's
`st_size` unspecified, so the "0 for every kind" measurement is a **linux** one.

An operator who would rather have the bound than the parity sets the flag or the
config key.

Everything else about `files.read` is byte-identical either way.

#### files.validate

`{path}` → `{"valid":bool,"isDir":bool[,"error"]}`

- Missing path → `{valid:false,isDir:false,error:"Path does not exist"}`.

#### files.extract_tar

`{archivePath,destDir}` → extracts a **gzip** tar → `{"success":true,"fileCount":<n>}`

Side effects — deliberate, and **not visible in the frame**:

1. **`destDir` is wiped** (`os.RemoveAll`) then recreated before unpacking —
   extraction is idempotent and destructive.
2. Entries get **owner-only fixed modes** — files `0600`, dirs `0700` (an
   executable `0755` entry still lands `0600`).
3. On success an **empty `.synced` marker** is written at `destDir` root (not
   counted in `fileCount`).
4. **`archivePath` is consumed** — removed on *every* outcome once opened
   (success, bad gzip, or unsafe path).

Errors:

- Missing params → `-32602 archivePath and destDir are required`.
- Non-absolute/root `destDir` → `{success:false,fileCount:0,error:"destDir must
  be an absolute, non-root path: …"}` — rejected before the archive is opened, so
  the archive is **not** consumed. (`fileCount` has no `omitempty`, so it is on
  the wire as `0`; this row omitted it until #231.)

  **"Root" is the platform's own definition.** The test is
  `filepath.Dir(filepath.Clean(destDir)) == filepath.Clean(destDir)` — "has no
  parent" — which is `/` on Unix and a **drive root (`C:\`) or a UNC share root
  (`\\server\share\`)** on Windows. It used to compare against the literal string
  `"/"`, which is a Unix-only notion: `C:\` cleans to `C:\`, never to `"/"`, and
  `filepath.IsAbs` accepts it — so a Windows volume root passed the gate and
  reached the `os.RemoveAll` that wipes `destDir`. Fixed in #225; a trailing
  separator (a doubled `C:\\`, or `//`) cannot slip past by shape either, because
  the path is cleaned first.

  **Which arm fires is not observable.** The root test and the `filepath.IsAbs`
  test share one branch and one message, so `C:\` is refused on Unix too — there
  by the *non-absolute* arm, since `IsAbs` on Unix is a leading-`/` test — with a
  byte-identical error. What genuinely varies by OS is the **accepted** set: the
  same `destDir` can be absolute with a parent on one platform and not on the
  other, so a client can see one platform unpack a path the other refuses.

  Whether the reference refuses a root `destDir` at all is **not measured** —
  filed here as neither parity nor divergence, because the guard's justification
  is the consequence on our side (a recursive delete of the volume) rather than a
  claim about the reference.
- **`destDir` is, or contains, the home directory** →
  `{success:false,fileCount:0,error:"destDir must not be or contain the home directory: …"}`
  — also rejected before the archive is opened. **Intentional divergence** (D2):
  `destDir` is `~`-expanded before the method sees it, so `"destDir":"~"`
  otherwise reaches `os.RemoveAll($HOME)` — which is how an in-repo fuzzer
  destroyed a home directory on 2026-08-02. **The reference wipes it**: measured
  2026-08-06 at `5db5e4a`, `"destDir":"~"` answers
  `{"success":true,"fileCount":1}` and leaves the home directory emptied and
  recreated. The test is *containment*: the home
  directory itself and any ancestor of it (`/home`, `/Users`) are refused, while
  anything **under** home — `~/.claude/…`, the daemon's own install path — is
  still accepted.
- Bad gzip → `{success:false,fileCount:0,error:"gzip: …"}`.
- An entry whose path escapes `destDir` ("zip slip") →
  `{success:false,fileCount:0,error:"unsafe path in archive: <entry>"}`; a `../`
  that resolves back inside `destDir` is allowed.
- A non-regular/non-directory entry (symlink, hardlink, device, fifo) →
  `{success:false,fileCount:0,error:"unsupported tar entry type <c>: <entry>"}`
  — `<c>` is the tar typeflag char (symlink=`2`, hardlink=`1`).
- **Total uncompressed bytes over the opt-in cap** →
  `{success:false,fileCount:0,error:"extraction size limit exceeded"}`. **Not
  reachable by default** — the cap is `0` (off), matching the reference, which
  has none. **Intentional divergence** (D3), enabled with
  `-max-extract-bytes <n>` or `max-extract-bytes = <n>` in `claustrum.conf`; see
  that flag for the measurement and the reason the default flipped.
- destDir clean/mkdir or marker-write failures → `clean destDir: …` /
  `mkdir destDir: …` / `write .synced: …`.
- An entry whose target is an existing directory →
  `{success:false,fileCount:0,error:"create <entry>: open <target>: is a directory"}`.
- An entry whose parent cannot be created — e.g. an **earlier entry in the same
  archive** wrote a regular file where this one needs a directory →
  `{success:false,fileCount:0,error:"mkdir parent <entry>: <os error>"}` — the
  tail is the operating system's, e.g. `mkdir <path>: not a directory` on POSIX
  and `The system cannot find the path specified.` on Windows. Only the
  `mkdir parent <entry>: ` prefix is claustrum's, and only it is contract.

  Both prefixes name the **archive entry**, not the resolved target, and they are
  **different strings** — `create <entry>: ` and `mkdir parent <entry>: `. Both
  report `fileCount:0` even when earlier entries were already written to disk,
  the same shape the zip-slip rejection has. Measured against `5db5e4a`; the
  `io.Copy` failure path is *not* measured and is left unwrapped.

### git.* (param: `path` = repo dir; worktree ops use `baseRepo`)

#### git.info

`{path}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""}`

- `branch` is resolved via `symbolic-ref`, so it works on an **unborn HEAD**
  (empty repo with no commits → the init branch name, e.g. `master`).
- A **detached HEAD** is reported as `branch:"detached:<short-sha>"`.
- `root` is the absolute repo top-level (`git rev-parse --show-toplevel`), so it
  stays the repo root even when `path` is a subdirectory (added by reference
  `7cbfa471`; the `8de85faa` baseline omitted it).
- `repoSlug` and `defaultBranch` were added by reference `7c2f88d`. Both are
  **always present** (empty string when undeterminable) — including on the
  non-repo body, which is now `{"isRepo":false,"repoSlug":"","defaultBranch":""}`.
    - `repoSlug` is the `owner/repo` parsed from `remote.origin.url`, and it is
      populated **only for a canonical `github.com` remote**. Every rule below was
      measured by driving 42 remote-URL shapes through the reference at `5db5e4a`:
        - **Scheme** must be `https`, `http`, `ssh`, `git`, or absent (the
          scp-like `[user@]host:owner/repo` form). `git+ssh://` and `file://`
          yield `""` — the scheme is matched whole, not by suffix.
        - **Host** must equal `github.com`, case-insensitively (`GITHUB.COM` is
          accepted). `www.github.com`, a trailing-dot `github.com.`, GitLab,
          Bitbucket and any self-hosted GHE all yield `""`. A port makes it a
          different host, so `github.com:443/…` yields `""` too. Userinfo
          (`git@`, `user:pw@`) is stripped.
        - **Path** must be exactly two non-empty segments after one optional
          trailing `/` and one optional trailing `.git`. Three segments
          (`acme/sub/gizmo`) or one (`acme`) yield `""`.
        - **Owner** is alphanumerics with *interior* hyphens only: `ac-me` and
          `ac--me` pass; `-acme`, `acme-`, `acme_corp` and `acme.co` do not.
        - **Repo** is alphanumerics plus `.`, `_` and `-`, not starting with `-`,
          not `.` or `..`, and not ending in a **lowercase** `.wiki`. The owner
          charset is stricter than the repo charset — `_` and `.` are legal in a
          repo name and illegal in an owner. The `.wiki` test is case-sensitive
          and suffix-only, so `GIZMO.WIKI` and a repo named `wiki` are accepted.
    - `defaultBranch` is what `refs/remotes/origin/HEAD` points to (e.g. `main`);
      empty when origin/HEAD is unset.

#### git.status

`{path}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}` (porcelain lines)

- `changes` comes from `git status --porcelain`'s **stdout only**. git writes
  warnings (an unreadable `core.excludesFile`, an unreadable directory) to stderr
  while still exiting 0; those never appear in `changes`, and a repo with nothing
  modified still reports `"clean":true`. Measured against `5db5e4a`.

- Lines are `git status --porcelain` output **verbatim**, minus only the line
  ending. The two-character XY column is **positional**, so the leading space of
  an unstaged-only change is data: `"M  a.txt"` (staged) and `" M b.txt"`
  (unstaged) differ only in which column holds the letter.

- **The first line is the exception.** The daemon trims the whole porcelain blob
  before splitting it, so a leading space is stripped from the first entry only.
  A repo whose first two entries are `" M a1.txt"` and `" M a2.txt"` returns
  `["M a1.txt"," M a2.txt"]`. Probe-measured against the reference at `5db5e4a`;
  clients that parse by column must handle entry 0 separately.

- Non-repo → `{"isRepo":false,"clean":false}` — the **full shape**, unlike
  `git.info`'s bare `{"isRepo":false}`.

#### git.list_branches

`{path}` → `{"isRepo":true,"branches":[…sorted…]}`

- Non-repo → `{"isRepo":false,"branches":[]}`.
- **stdout only**, like `git.status` and unlike `git.worktree_create`. A repo
  with a broken ref makes `for-each-ref` warn on stderr while still exiting `0`;
  that warning must not become a branch name.
- A `for-each-ref` that **fails** (e.g. a corrupt `packed-refs`, exit 128) is
  reported as `-32603` carrying the Go error string — `exit status 128`, not
  git's `fatal: …` text. Same rule as `git.status`.
- **Claustrum-only frame.** If claustrum's opt-in `gitTimeout` kills git instead,
  the same `-32603` carries **`signal: killed`** — `Cmd.Wait` prefers the
  SIGKILLed process's exit error over the context error. The reference runs git
  with no deadline at or below the 75 s that was probed, and simply blocks, so it
  never emits this. `git.status` has
  the identical frame for the identical reason. Intentional divergence **(D5)**,
  and ⚠️ **not reachable at the shipped default** — `gitTimeout` is `0` (no
  deadline) unless an operator sets `-git-timeout` or the `git-timeout` key in
  `claustrum.conf`. See IMPROVEMENTS §5 and the D5 entry.

#### git.worktree_create

`{baseRepo,branchName,worktreePath[,sourceBranch]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>"}`

- The repo is **`baseRepo`** (not `path`); absent → the daemon's cwd repo.
- Missing `branchName` → `-32602 branchName is required`.
- Resolved repo isn't a git repo →
  `{success:false,error:"not a git repository",errorCode:"not_a_repo"}` —
  checked before the add, so git's raw error isn't leaked.
- Other failure →
  `{success:false,error:"git worktree add failed: …",errorCode:"worktree_add_failed"}`.
  The tail after the colon is git's **combined** output — the opposite of
  `git.status` above, and deliberately so: `git worktree add` writes both its
  progress and its fatal to stderr and leaves stdout empty, so reading stdout
  only would truncate this to `"git worktree add failed: "`. Measured against
  `5db5e4a` with an existing branch name:
  `"git worktree add failed: Preparing worktree (new branch 'dup')\nfatal: a branch named 'dup' already exists"`.
- When `sourceBranch` is omitted it defaults to the repo's current branch (and
  is echoed back). On an **unborn HEAD** (empty repo) the source resolves to
  empty, the add infers an orphan branch and still succeeds, and `sourceBranch`
  is omitted from the result.


**Worktree population.** `git worktree add` checks out tracked files only, so
the daemon then seeds the new worktree:

- **`.claude/` is copied recursively and unconditionally** — nested directories
  and dotfiles inside it included. It does not need to be listed anywhere; an
  absent `.claude/` is skipped silently.
- **`.worktreeinclude`** (repo root, `.gitignore` syntax) is an **include**
  manifest: untracked files matching it are copied, even when they are also
  gitignored. The selection is
  `git ls-files --others --ignored --exclude-from=.worktreeinclude` — without
  `--exclude-standard`, so a gitignored file the manifest does not name is *not*
  copied. Without the manifest, no untracked file is copied at all.
- **Symlinks are skipped** — neither the link nor its target is materialized.
- **Manifest entries must be plain filenames.** `git ls-files` C-quotes any path
  containing a tab, a quote, a backslash or a non-ASCII byte (`weird-café.txt`
  prints as `"weird-caf\303\251.txt"`), and such an entry is silently **not**
  copied. This is a reference limitation reproduced here for parity, not a
  claustrum bug — the reference skips the same files.
- **Copies do not preserve the source mode.** Each is created 0666-subject-to-
  umask, so an executable listed in the manifest arrives non-executable, and a
  deliberately private source (say `0400`) is widened to whatever the umask
  allows. This matches the reference and is reproduced deliberately; treat the
  manifest as naming configuration, not secrets or scripts.
- ⚠️ An **opted-in** `-git-timeout` (D5) kills the `git ls-files` that performs the
selection, and the early return then skips **every** manifest-selected file while
the reply is still `{"success":true}` — a silent, wire-invisible loss distinct from
the per-file case below. Off by default. Copy failures are best-effort and never fail the request.
#### git.worktree_remove

`{baseRepo,worktreePath[,branchName]}` → `{"success":true}` (lenient)

- Runs `git worktree remove --force`. **Whenever git exits non-zero — for any
  reason — the daemon then removes `worktreePath` itself, recursively, and still
  answers `{"success":true}`.** Only when that manual cleanup *also* fails does
  the reply carry the declared `error` field:
  `{"success":false,"error":"failed to remove worktree: <git output>; manual cleanup also failed: <err>"}`.
- ⚠️ **"For any reason" is literal, and it is measured.** The deletion is not
  limited to the locked-worktree case that motivated it. Probed at `5db5e4a`,
  checking the directory afterwards rather than the reply:

  | `worktreePath` | why git fails | directory afterwards |
  |---|---|---|
  | a locked worktree | it is locked | **deleted** |
  | an ordinary directory, never a worktree | `not a working tree` | **deleted** |
  | any path, with a `baseRepo` that is not a repo | `not a git repository` | **deleted** |

  So this method is a recursive delete of the caller-supplied `worktreePath`
  whenever git is unhappy. That is **reference behavior**, matched deliberately —
  not a claustrum addition. Callers should treat `worktreePath` as a path they are
  asking to have removed, not as a filter.
- **One input is exempt from that parity — claustrum-only hardening (D2).** A
  `worktreePath` that **is, or contains, the home directory** is refused before
  git runs at all:
  `{"success":false,"error":"worktreePath must not be or contain the home directory: …"}`.
  Neither the removal nor the `branchName` delete happens.

  ⚠️ **Containment is judged AFTER the path is resolved against the daemon's
  working directory**, not on the string the caller sent — so `"."` and `".."` are
  refused too whenever that working directory is the home directory or a
  descendant of it. That is the same double resolution documented three bullets
  below, and it is deliberate: the daemon's cwd is the root `os.RemoveAll` itself
  uses, so the guard judges the path the delete would actually hit. The
  consequence for a client is that **the verdict on a relative `worktreePath` is
  not predictable without knowing where the daemon was started** — send an
  absolute path. An **empty or omitted** `worktreePath` is exempt from the guard
  entirely and keeps its pre-D2 reply, since `os.RemoveAll("")` is a no-op.

  The reason for the rule is the row the table above was missing — now measured,
  2026-08-06 at `5db5e4a` on an ephemeral VM:

  | `worktreePath` | reference reply | directory afterwards |
  |---|---|---|
  | `"~"`, i.e. the home directory | `{"success":true}` | **deleted** |

  `worktreePath` is `~`-expanded before the method sees it (and the reference
  *client* tilde-expands too, see below), a home directory is not a worktree so
  git fails, and the fallback is therefore
  `os.RemoveAll($HOME)`. Matching the reference is this project's rule for
  *frames*; it was never a commitment to reproduce an unrecoverable data loss the
  reference reaches by accident. Same containment test as `files.extract_tar` —
  paths **under** home are unaffected.
- The worktree stays **registered**. Deleting the directory does not remove
  `$GIT_DIR/worktrees/<name>`, so `git worktree list` still shows it afterwards
  and a later `git.worktree_create` at the same path fails with
  `already registered`. Measured identical on both binaries (2 entries still
  listed after a locked removal); the reference does not prune either.
- **A relative `worktreePath` is resolved twice, against different roots.** git
  runs with `-C <baseRepo>` and resolves it against the repo; the manual cleanup
  resolves it against the **daemon's working directory**. So the fallback can
  delete a directory git never looked at. Measured at `5db5e4a` with a locked
  worktree at `<repo>/wt` and a decoy at `<daemon cwd>/wt`: **both binaries
  deleted the decoy and left `<repo>/wt` in place.** Parity, and alarming — send
  an absolute `worktreePath`. The reference client does: it tilde-expands every
  remote path before sending.
- **`gitTimeout` does NOT authorise the deletion — claustrum-only (D5), and off by
  default since the flip, so this whole arm needs an opt-in to reach.** (The cap is
  also softer than it reads: it waits on git's output pipe, so a git that spawns a
  surviving child stays blocked past the deadline — see IMPROVEMENTS §5.) The git cap
  is a claustrum divergence (the reference showed no deadline at or below
  the 75 s probed, and
  blocks — measured: no reply at 75 s against claustrum's 60.1 s, with a fast-git
  control proving the fixture could answer at all; this was pointer-class until
  that run), so a timeout must not be read as "git refused". It answers
  `{"success":false,"error":"git worktree remove timed out after <dur>; no cleanup
  was attempted, and git may have partially removed the worktree"}` and the daemon
  itself removes nothing. The wording claims only what the daemon can observe: the
  git it SIGKILLed unlinks files as it goes, so the directory state is not knowable
  from here. Before this was separated out, a wedged git produced a deletion plus
  `{"success":true}` — an outcome the reference cannot reach.
- Naming a branch that does not exist still answers a bare `{"success":true}` —
  hence "lenient".

### process.* (the agent/MCP-hosting core)

The client supplies its own `id` (any string). Output is delivered as id-less
stream notifications, **buffered** for later replay.

#### process.spawn

`{id,command[,args][,cwd][,env][,wantPid]}` → `{"success":true}`, then stream frames

- `args`: string[]. `env`: `{KEY:VAL}` merged over the daemon environment.
- Missing `id` → `-32602 Process ID is required`; missing `command` →
  `-32602 Command is required`.
- Reusing a still-live `id` succeeds and replaces the registry entry (like the
  reference). **Divergence:** claustrum also tears down the now-orphaned
  previous process tree (it would otherwise be unreachable via
  `kill`/`stdin`/`reattach` and leak), with its subscribers dropped first so no
  stray frames arrive under the reused id. OS-level only — no wire frame
  changes; the reference leaves the old process running.
- **`wantPid` opt-in (CT-1, claustrum-only).** When the params carry
  `"wantPid":true`, the reply gains two fields **after** `success`:
  `{"success":true,"pid":<int>,"startTime":<number>}`. `pid` is the child's OS
  pid; `startTime` is the **daemon's wall clock (epoch seconds) captured at
  spawn**, returned identically on spawn and reattach for the same process. It is
  an **opaque token** for PID-reuse / orphan detection (CL-8): compare a persisted
  daemon value against a later daemon value for the **same id**. **Do not** equality-
  compare it against an independently-read OS process start time (e.g. psutil
  `create_time`) — the daemon's spawn-moment wall clock differs from the kernel's
  process-creation time by the fork→`time.Now()` delta and a different clock
  derivation. **Default-mode is byte-identical:** absent or `false`, the two
  fields are omitted (`omitempty`) and the frame is exactly the old
  `{"success":true}`. The fields live on a dedicated result struct, so they can
  never leak into a `process.stdin`/`process.kill` reply. An older daemon ignores
  the unknown `wantPid` param (tolerant decode), so a CT-1 client is safe to send
  it unconditionally — graceful degradation in both directions.

#### process.stdin

`{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}`

- `data` is **base64**, written to the child's stdin.
- Checks run in a fixed order (probe-verified): **decode → exists → running →
  offset**:
    - Invalid base64 → `-32602 Invalid base64 data` — returned *before* the
      process is even looked up, so an unknown id with a bad payload still
      reports the decode error.
    - Unknown id → `-32602 Process not found`.
    - Known but **exited** process → `-32602 Process not running`.
- **`offset` / `applied` — the resumable-stdin contract (added `7c2f88d`,
  advertised as the `process.stdin.offset` feature).** The reply now **always**
  carries `applied`: the cumulative count of stdin bytes accepted for delivery
  (the high-water mark). `offset` is the byte position the caller believes this
  `data` starts at; it makes stdin **idempotent** across reconnects:
    - **absent** `offset`, or `offset == applied` → append; `applied` grows by
      `len(data)`.
    - `offset > applied` → `-32003 stdin offset gap: offset ahead of applied
      bytes` (a hole that would drop input — the caller must resend from
      `applied`). Nothing is enqueued.
    - `offset + len(data) <= applied` (wholly already applied) → **no-op**, reply
      adds `"duplicate":true`, `applied` unchanged, nothing reaches the child.
    - partial overlap (`offset < applied < offset+len`) → only the fresh tail
      `data[applied-offset:]` is written and `applied` advances to
      `offset+len(data)` (not flagged duplicate).
  `applied` counts base64-**decoded** bytes and is never `omitempty` (emitted even
  at 0); `duplicate` is dropped when false. A legacy client that never sends
  `offset` still works — it just always appends — and simply gains the `applied`
  field it can ignore.

#### process.kill

`{id[,signal]}` → `{"success":true}`

- Best-effort, **fire-and-forget**. Does not wait for the child to actually exit
  (contrast `process.killAndWait`).
- **How wide the signal reaches depends on the signal**, on Unix:
    - `KILL` goes to the whole **process group** (negative pid), so the entire
      child tree dies — backgrounded grandchildren included.
    - **Every other signal** (`TERM`, `INT`, `HUP`, and the default) goes to the
      **direct child only**. A grandchild the child backgrounded keeps running.
      Measured against the reference at `5db5e4a`: spawning
      `sh -c "sleep 40 & wait"` and sending `process.kill` with `TERM` leaves the
      backgrounded sleeper alive.
    - So a graceful `process.kill` is **not** a tree teardown. Use
      `signal:"KILL"`, or `process.killAndWait` with `escalate:true` (which
      escalates to a group `SIGKILL`), when the whole tree must go.
  On Windows the split does not apply — the Job Object is terminated, which takes
  the tree either way.
- **Divergence:** claustrum skips the signal when the child has already
  exited — after the child is reaped its Unix pgid can be recycled, so the
  reference's unconditional negative-pid signal could hit an unrelated process
  group. OS-level only — the reply is identical either way.

#### process.killAndWait

`{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}`

Added by reference `7c2f88d`. Unlike `process.kill`, it **blocks until the
process is gone** (up to the grace) and reports the outcome as a *result* (an
unknown id is not an error):

- Missing `id` → `-32602 Process ID is required`; absent `params` object →
  `-32602 Invalid params`.
- Unknown id → `{"found":false,"died":false}`.
- Already exited before the call → `{"found":true,"died":true,"alreadyExited":true}`
  (no signal sent).
- Live process → the graceful `signal` (default `SIGTERM`) is sent, then it waits
  up to the grace:
    - **`timeoutMs`** sets that grace. Non-positive or absent → the **3000 ms**
      default (probe-verified: `0` and `-100` both wait 3000 ms); positive values
      are honored verbatim (50 ms → ~50 ms, 8000 ms → ~8 s) **up to a 30000 ms
      ceiling**. A larger value is clamped to it, so `timeoutMs: 45000` against a
      signal-ignoring child answers after ~30 s, not 45 s. Measured against the
      reference at `5db5e4a`; the black-box bracket is (29500, 30500] and 30000 is
      the only round value in it. The ceiling is not new in `5db5e4a`: `7c2f88d`,
      the build that added the method, answers at ~30 s for the same input.
    - **`escalate`** (default `true`) decides what happens if the process is still
      alive after the grace. `true` → **escalate** to `SIGKILL`, wait up to
      **7 s** for the reap, and add `"escalated":true` to the reply. That 7 s is
      itself measured against the reference (2026-08-06, black-box): with
      `timeoutMs: 500` against a child `SIGKILL` cannot reap, the reference
      replies at 7.51 s. It is client-observable twice over — in when the reply
      arrives, and in whether a child reaped between 5 s and 7 s reports
      `"died":true` rather than `false`. A pipe-holding grandchild cannot measure
      it: the exit drain closes the read ends at 5 s first, so both binaries
      answer at 5.01 s regardless. `false` → leave the process running
      and report `{"found":true,"died":false}` (no `escalated`, no SIGKILL).
      The escalation `SIGKILL` goes to the **process group**, so it sweeps up the
      child tree the graceful signal spared — and it is sent even when the
      graceful signal already killed the child itself, since a grandchild holding
      the stdout pipe can keep the drain pending past the grace. `escalate:false`
      spares the tree entirely.
- On a process that dies within the grace (cooperative, or a hard `signal:"KILL"`)
  → `{"found":true,"died":true}` with no `escalated`.

#### process.reattach

`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`

- Replays buffered frames with **seq > fromSeq** (exclusive) to this
  connection, **transfers** the frame stream to it, then returns the result.
- **The transfer is exclusive.** A reattach does not add a second listener: any
  previously attached connection stops receiving frames for that process. This
  is what makes resume safe — otherwise an old connection that is still open
  would keep getting frames the new one has just been replayed, and the two
  deliveries would overlap. Measured against the reference at `5db5e4a`.
- **The cut is by `seq`, not by wall-clock.** The transfer point is the `lastSeq`
  the reattach reports: the old connection can still receive a frame whose `seq`
  is `<= lastSeq` slightly *after* the reattach reply, and never one above it. A
  frame is stamped with its `seq`, appended to the replay buffer, and matched
  against the subscriber set in a single critical section, so a frame that
  predates the transfer belongs to the old connection and is simultaneously in
  the new connection's replay — which is exactly what `fromSeq` is for. There is
  no frame that reaches the old connection and is absent from the new one's
  replay.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- **Exited processes are retained for 15 minutes, then dropped** — with their
  replay buffers — so an id last seen longer ago than that answers exactly like
  an unknown one, and `process.kill` on it still reports `{"success":true}`. A
  **running** process is never dropped, however old. The sweep runs on a 60-second
  timer *and* inline on every `process.spawn`, so an idle daemon prunes too. A
  client that reconnects after a long gap must therefore treat `found:false` as
  "finished and forgotten", not as "never existed".

  *Provenance.* This bullet used to be flagged "probe-verified against the
  reference at `5db5e4a`" in full, which overstated it. What the probe supports:
  an entry is still reachable **45 s** after its process exited and gone after
  **960 s** — bracketing the retention to *(45 s, 960 s]* — a running process
  survives, and an entry can disappear with no intervening `process.spawn`.

  Both ends of that bracket are observations. It previously read *(20 s, 960 s]*,
  whose lower bound followed from nothing stated beside it ("a just-exited one is
  still there" supports no lower bound at all); 45 s is a measured `reattach`
  answering `found:true`. The exact **15 minutes** and the **60-second** period
  remain pointer-class — no wire observable distinguishes them from any other
  value inside the bracket, or one ticker period from another.
- **`stdinApplied` (added `7c2f88d`).** The process's cumulative applied-stdin
  byte count (§`process.stdin`), always present after `lastSeq`. A reconnecting
  client resumes stdin from this offset so no bytes are re-applied or dropped.
  **It is an acknowledgement, not a delivery receipt.** `process.stdin` returns
  before the child has read the data, so bytes accepted just before the child
  exits are counted here even though the writer never delivered them — including
  bytes accepted during the exit-drain window, where the child is already reaped
  but still reports `running`. Probe-verified as reference behavior at `5db5e4a`,
  down to the same `stdinApplied` value; a client that must know data arrived has
  to confirm it in-band, not from this counter.
- **`wantPid` opt-in (CT-1, claustrum-only).** As on `process.spawn`: with
  `"wantPid":true` **and** the process found, the reply appends
  `"pid":<int>,"startTime":<number>` **after** `stdinApplied`, reporting the
  **same** pid and startTime the spawn did (so a client can confirm it reattached
  to the same process, not a pid-reuse). Omitted when `wantPid` is absent/false or
  the process was not found — the default frame stays byte-identical.

### Stream notifications

```jsonc
{"type":"stream","processId":"<id>","stream":"stdout","seq":1,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"stderr","seq":2,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":0}
```

- `seq` is **per-process**, starts at 1, monotonic across stdout/stderr/exit.
- `data` is base64 for stdout/stderr.
- The `exit` frame carries `exitCode` and no `data`. A signal-terminated child
  reports `exitCode: -1` (not `128+signo`).
- The `exit` frame waits at most **5 seconds** after the process itself exits for
  stdout/stderr to reach EOF, then the daemon closes the read ends and emits it
  anyway. This only matters when the command leaves a **grandchild holding the
  same pipe** (`npm run dev &`, anything that daemonizes): output that grandchild
  writes after the cap is **not** forwarded, and its write fails with `EPIPE`.
  Until the frame is emitted, `process.reattach` still reports
  `running: true` — the flag flips with the frame, not with the process. All
  probe-verified against the reference at `5db5e4a`.
- Each stdout/stderr frame carries at most one **32 KiB** read (the streaming
  read buffer); larger output is split across frames. A client reassembles by
  concatenating `data` in `seq` order.
- Exact frame *boundaries* depend on pipe scheduling and are not stable — only
  the reassembled bytes are. (Both the 32 KiB cap and the `-1` signal code are
  probe-verified against the reference.)
- The replay buffer is **bounded at 16 MiB per process**, counted as the
  **serialized frame including its trailing newline** — the bytes a subscriber
  would receive — not as the base64 `data` alone. Probe-verified against the
  reference; an exit frame therefore costs its envelope even though it carries no
  `data`. (This previously read "16 MiB of base64 `data`". The constant was right
  and the unit was not: the difference is under 1% at 8.7 KB frames and ~12% at
  600-byte frames, so the run that established the constant could not see it.)
  Frames are dropped oldest-first, whole frames at a time, once a new frame would
  exceed the cap; at least one frame is always retained, even one larger than the
  cap. So `reattach{fromSeq:0}` replays
  everything **still retained**, not necessarily everything ever emitted — the
  reply's `firstSeq` is the floor, and a client that needs the gap detected must
  compare it against the last `seq` it saw.
- A process **survives** the disconnect of the connection that spawned it;
  another connection can pick it up via `reattach`. This is the multi-attach /
  reconnect mechanism.

## Daemon lifecycle (flags)

One binary, five modes. Everything below is probe-verified against the
reference unless marked **claustrum-only**.

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] [-keep-children] [-listen-pipe] [-max-extract-bytes <n>] [-git-timeout <dur>] [-files-read-regular-only]
```

Self-daemonizes (reparents to init / detached), extracts the login-shell PATH
(Unix), then runs the RPC server. On success it prints
`Claustrum remote server listening on <socket>` to stdout.

**Login-shell PATH extraction** (Unix) runs `$SHELL -l -i -c …` when `$SHELL` is
an executable file, else the first usable of `/bin/zsh`, `/bin/bash`, `/bin/sh`
— **zsh first**, matching the reference. The resolved PATH goes to spawned
children only, never into the daemon's own environment. Two observable rules:

- Extraction is capped at **4 s**, and a timeout **discards** whatever the shell
  printed — even a complete, valid PATH. The daemon logs one line naming the
  shell and children fall back to the inherited PATH.
- The value reaches `process.spawn` children as their `PATH`. It does **not**
  affect how the daemon resolves the `command` you send: that is looked up
  against the daemon's own PATH, so the extracted value can never turn a spawn
  into `executable file not found`. It is visible only to a child that resolves
  binaries itself (`sh -c …`).

**Token source** — required, and checked **in the detached child**, not in the
launcher:

- Missing both flags → the launcher daemonizes anyway, the child refuses to
  start, and the launcher reports its accept timeout after ~10 s:
  `claustrum: timeout waiting for daemon to accept on <socket>`, exit `1`. The
  specific reason —
  `claustrum: daemonized child requires --token-file or --token-fd` — reaches
  only the child's own stderr, which is detached.

  This is deliberate parity, measured against the reference: `-serve` with no
  token flags exits 1 after 10.07 s there with the same accept-timeout shape.
  claustrum used to check in the launcher and fail in 0.03 s naming the actual
  problem. That is friendlier and it is a divergence, so it is not what ships.
  A zero-byte `-token-file` always behaved this way on both.
- `CLAUDE_RPC_TOKEN` is **not** accepted for `-serve` — nor read by any other
  mode; no claustrum code path reads it at all. The daemon never starts
  unauthenticated.
- The token is read as a **line**: one trailing `\n`/`\r\n` is stripped; spaces
  and other surrounding whitespace are preserved verbatim (a token file ending
  in a newline still authenticates).
- A bad `-token-file` → `claustrum: read --token-file: <err>`, exit `1`.
- `-token-file` is read once at startup, then **unlinked**.

**`-token-fd <n>`** *(claustrum-only)* — token from a descriptor, no temp file:

- Reads the token from an already-open file descriptor (`-token-fd 0` = stdin),
  so it never touches disk.
- Because `-serve` re-execs to daemonize, the parent reads the fd and forwards
  the token to the detached child over an inherited pipe — never via disk,
  argv, or the environment.
- Additive and off the wire: `-token-file` callers are unaffected, and without
  the flag the reference is matched byte-for-byte.

**Daemonize sentinel** *(internal; claustrum-namespaced)* — the re-exec marker
that tells a freshly-exec'd process "you are the detached child, don't
re-daemonize" is **`CLAUSTRUM_DAEMON_CHILD`**, not the reference daemon's
`CLAUDE_SSH_DAEMON_CHILD`. The reference name cannot serve this role here: a host
running *inside* a real claude-ssh session exports `CLAUDE_SSH_DAEMON_CHILD=1` to
every descendant, so the claustrum launcher would inherit it ambiently, mistake
itself for the already-daemonized child, skip the parent token-forward path, and
exit `1`. The sentinel is purely internal (never on the wire), so namespacing it
is free. Observable parity is preserved separately: `daemonizeWithToken` still
sets `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's own environ so it propagates
verbatim into `process.spawn` children, exactly as the reference does (pinned by
`TestSpawnInheritsDaemonChildMarker`); the internal `CLAUSTRUM_DAEMON_CHILD` is
unset in the child before it spawns anything, so it never leaks downstream.

**`-metrics-addr <a>`** *(claustrum-only)* — opt-in observability:

- Serves Prometheus-format counters at `http://<a>/metrics` — connections,
  process spawns/exits, reattaches, stream/stdin bytes.
- **Off by default**: no listener exists unless the flag is passed; not part of
  the JSON-RPC wire contract, so parity is unaffected.
- Counts only (no command output, no tokens) and **no auth** — bind it to a
  trusted interface (loopback).
- A bind failure is logged (`[Server] metrics: …`) and non-fatal.
- Also settable in `claustrum.conf` as `metrics-addr = <a>` (an explicit
  `-metrics-addr` flag wins); see [`-version`](#-version) / IMPROVEMENTS CT-3.

**`-keep-children`** *(claustrum-only, CT-2; POSIX-only)* — survive a daemon restart:

- **Off by default**, behavior is unchanged: graceful shutdown (a
  `server.shutdown` RPC, `SIGTERM`, or `SIGINT`) kills the whole child-process
  tree, exactly as before.
- **With the flag set**, graceful shutdown tears down the listener and client
  connections but **leaves spawned children running**, so they survive a daemon
  restart/upgrade. It logs one line —
  `[Server] -keep-children: leaving <n> running child process(es) alive across shutdown`.
- The new daemon does **not** re-adopt the survivors (no persist / re-manage); an
  out-of-band consumer reconciles them (e.g. via the CT-1 `pid`/`startTime`).
- **Survivors lose their stdio.** A child's stdin/stdout/stderr are pipes whose
  other ends die with the daemon: the survivor sees **EOF on stdin**, and a later
  write to stdout/stderr fails — **SIGPIPE** (default disposition: terminate) for
  a child that hasn't ignored it, **EPIPE** write errors for one that has (Node.js
  ignores SIGPIPE by default, so Node children get write errors, not killed).
  Survival is therefore only useful for children that tolerate dead stdio —
  quiet workers, EPIPE-tolerant processes, or children that re-plumb their own
  output. There is no way for the daemon to re-plumb a live child's fds.
- **Off the wire**: no method, frame, or capability changes — it is a lifecycle
  flag, so parity is unaffected (default-mode frames are byte-identical).
- **Windows decision:** the flag is **POSIX-only**. Children are confined to a Job
  Object created with `KILL_ON_JOB_CLOSE`, which the OS terminates when the daemon
  exits regardless of any shutdown-time choice. Rather than silently kill while
  claiming to keep, Windows **ignores the flag and logs a warning** at startup
  (`[Server] -keep-children is not supported on Windows …`). The hosted channel
  that uses this is POSIX-only anyway.
- Also settable in `claustrum.conf` as `keep-children = true|false` (an explicit
  `-keep-children` flag wins).
- *(Implementation note: shutdown teardown now runs synchronously on the main
  goroutine so the kill-or-keep decision reliably completes before the process
  exits — it previously ran in a goroutine that could lose the race to the accept
  loop's return, skipping child teardown entirely.)*

**`-listen-pipe`** *(claustrum-only, CT-5; Windows-only)* — additional named-pipe transport:

- **Off by default**, byte-for-byte identical to the reference. When set, the
  daemon *additionally* serves the same NDJSON JSON-RPC dispatch over a Windows
  named pipe, concurrently with the `AF_UNIX` socket — the socket, wire contract,
  field ordering, and framing are unchanged. Same `"auth"` handshake.
- Exists so a Windows client that cannot consume `AF_UNIX` (e.g. Python `asyncio`,
  pinned to the Proactor loop) can connect over a pipe, which that loop supports
  natively. See [Named-pipe transport](#named-pipe-transport-windows-opt-in).
- claustrum picks the pipe name and publishes it to **`rpc.pipe`** beside the
  socket (written before accepting/ready, removed on graceful shutdown); the
  client reads that file to discover the opaque name.
- The pipe is **owner-only** (SDDL `D:P(A;;GA;;;<current-user-SID>)`, the analogue
  of the socket's `0600`) and local-only.
- **Windows-only:** ignored with a warning on other platforms (they use the socket
  directly). A setup failure is logged (`[Server] named-pipe transport: …`) and
  non-fatal — the socket still serves.
- Also settable in `claustrum.conf` as `listen-pipe = true|false` (an explicit
  `-listen-pipe` flag wins).

**`-max-extract-bytes <n>`** *(claustrum-only, D3)* — opt-in extraction cap:

- **`0` (the default) means no cap**, which is what the reference does at every
  size the probe could reach: measured at `5db5e4a`, a 629 MB payload extracts
  fully and answers `{"success":true,"fileCount":1}`. claustrum's default answers
  the identical frame. (That measurement disproves a 512 MiB cap; it does not
  prove the reference has none above 629 MB.)
- A non-zero `<n>` caps the **total uncompressed bytes** `files.extract_tar` will
  write across all entries of one archive. Exceeding it returns
  `{success:false,fileCount:0,error:"extraction size limit exceeded"}` — a frame
  the reference never produces, hence the divergence. The entry that tripped the
  cap is removed rather than left truncated; entries already written are not.
- The cap **shipped on by default at 512 MiB** and that was a live user-facing
  break: a caller with a tree over the cap got an error with no way through,
  because Claude Desktop owns the argv (a **driver** claim, tracked with its
  provenance and reopen trigger in [`ARCHITECTURE.md`](ARCHITECTURE.md) →
  *Driver claims and their provenance*. ⚠️ The evidence behind it is **one look at the setup UI on one
  unrecorded build, 2026-08-07** — Desktop's own config files and any forwarded
  environment were never examined, and a config file it turns into argv is one of
  the two routes that would falsify it). Flipping the default to `0` is the parity
  fix; the cap itself survives as an opt-in for hosts that want it.
- Also settable in `claustrum.conf` as `max-extract-bytes = <n>` (an explicit
  `-max-extract-bytes` flag wins). **That is the reachable knob** — see the argv
  point above. Negative or unparseable values are ignored, so a typo can never
  silently enable a cap.

**`-files-read-regular-only`** *(claustrum-only, D4)* — opt-in `files.read` guard:

- **Off by default**, which is what the reference does: it reads `/dev/null` as
  `{"content":"","exists":true}` and blocks on a writerless FIFO rather than
  refusing either. claustrum's default answers the identical frames.
- Set it and any non-regular path answers
  `-32602 files.read: not a regular file` — a frame the reference never produces,
  hence the divergence. In exchange the daemon stops parking a goroutine **and a
  descriptor** on a writerless FIFO and stops being OOM-killed on `/dev/zero`.
- The guard is **whole**: the predicate is `Mode().IsRegular()`, and `/dev/null`
  and `/dev/zero` are indistinguishable by mode, so there is no narrower setting
  to offer. Full detail and the measured rows: [files.read → *Non-regular
  files*](#non-regular-files-opt-in-divergence-d4).
- The guard **shipped on by default** (PR 56) and Claude Desktop owns the argv
  (the same **driver** claim recorded under `-max-extract-bytes` above, with its
  provenance and reopen trigger in [`ARCHITECTURE.md`](ARCHITECTURE.md) → *Driver
  claims and their provenance*), so a caller reading a character device had no way
  through. Flipping the default to off is the parity fix.
- Also settable in `claustrum.conf` as `files-read-regular-only = true|false` (an
  explicit `-files-read-regular-only` flag wins). **That is the reachable knob** —
  see the argv point above. An unrecognised value leaves the key **unset**, so the
  flag value stands — which is "off" only when no flag was passed. The exact claim
  is that no accepted **oddity** switches the divergence on: `= true` arms it
  deliberately, which is what the key is for, and nothing else this parser accepts
  arms it at all. ("A typo leaves the guard off" is the weaker sentence that used
  to sit here, and it is false for `-files-read-regular-only` beside
  `files-read-regular-only = maybe`.)

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

A dumb relay — what an SSH session attaches to. It injects **no** auth;
whatever speaks through it supplies `"auth"` itself.

- **Strict**: a dial failure is a hard error —
  `claustrum: dial server: <err>` on stderr, exit `1`.

### -stop — ask a running daemon to shut down

```text
claustrum -stop -socket <p>          # no token needed, and none is read
```

Sends `server.shutdown`, with **no `auth` member** — the daemon does not
authenticate that method (see [Authentication](#authentication)).

> **Upgrading a live daemon.** A daemon still running from a build that predates
> this change *does* require auth on `server.shutdown`, so it answers `-32001`
> and keeps running. `-stop` discards the reply and exits `0` either way, so the
> caller sees success while the old daemon survives — and a new `-serve` then
> takes the socket beside it. Stop the old daemon before upgrading, or kill it by
> PID once. Self-correcting, but silent the first time.

- **Best-effort**: a missing or unreachable daemon is a silent no-op — exit
  `0`, no output. Nothing is ever echoed to stdout: any reply is read and
  discarded, matching the reference. Against a current daemon there is no reply
  to begin with, since `server.shutdown` answers nothing and closes.
- **The socket path is unlinked on every exit path**, including the one where the
  dial fails and no daemon was ever reached. Measured against the reference on
  three arms:

  | arm | reference | claustrum before |
  |-----|-----------|------------------|
  | live daemon (control) | gone | gone |
  | stale socket, no listener | gone | left in place |
  | live foreign listener | gone, listener alive | left in place, listener alive |

  The control arm attributes nothing: a live daemon unlinks its own socket during
  graceful shutdown, so "gone" there says nothing about who removed it. The other
  two arms are the evidence, because no claustrum daemon is involved in either.

  The foreign-listener arm is destructive, and it is matched deliberately: `-stop`
  removes a socket path it did not create and cannot identify the owner of. The
  listener itself is not torn down — that arm confirms it is still alive
  afterwards — but the path it was reachable through is gone, so a new client
  dialing by path cannot reach it. What becomes of its already-open connections
  was not measured. Making the unlink conditional would be a divergence, so it is
  recorded as a candidate rather than taken — see
  [IMPROVEMENTS.md → Candidates identified but NOT taken](IMPROVEMENTS.md#candidates-identified-but-not-taken-cli-mode-parity-2026-08-02).
  Note also that all three measured arms used socket-shaped paths: `os.Remove`
  removes a regular file or an empty directory at the `-socket` path just the
  same, and neither shape was put in front of the reference.

### -version

```text
claustrum -version                   # → claustrum <id> (built <time>)
```

**Intentional divergence: `version-override` via `claustrum.conf` (claustrum-only, CT-3).**
An optional `key = value` file named `claustrum.conf`, read from the directory
holding the binary, gates a few opt-in divergences; **absent/malformed ⇒ stock**.
If it sets `version-override` to a bare commit SHA (git SHA-1, 40 hex — the string
the desktop client pins; 64-hex also accepted; anything else is a no-op), the
output becomes:

```text
claustrum -version                   # → claude-ssh <sha> (via Claustrum <id>, built <time>)
```

This exists so the desktop client treats an already-deployed claustrum as
up-to-date — it keys re-upload on `<bin> --version` matching `/claude-ssh\s+(\S+)/`
against the pinned SHA. It is **CLI stdout only** — not a JSON-RPC frame — so the
wire contract is untouched; `server.version` / `server.capabilities` still report
claustrum's own `<id>`. The same file also carries `keep-children`,
`metrics-addr`, `listen-pipe`, `max-extract-bytes`, `max-cli-bytes`,
`cli-probe-timeout`, `cli-download-timeout`, `libc-probe-timeout`, `git-timeout` and
`files-read-regular-only` defaults (precedence: explicit CLI flag > config > default). See
[IMPROVEMENTS.md](IMPROVEMENTS.md) CT-3 for the full contract, key list, and
hardening.

### -install — ensure the agent CLI

```text
claustrum -install -cli-dir <d> -cli-version <v> \
          [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>] \
          [-max-cli-bytes <n>] [-cli-probe-timeout <dur>] [-cli-download-timeout <dur>] \
          [-libc-probe-timeout <dur>]
```

Download / verify / extract / prune, then print one `__INSTALL_RESULT__<json>`
facts line. `-install` itself always exits `0` — failures are reported inside
the facts (`cliError`), not via the exit code.

Five `-install` behaviours are easy to miss. Three are wall-clock bounds: the
`--version` runnability probe (D11), the download (D12), and — **on linux only** —
the `libc` probe (D14). **The reference showed no deadline at the durations
probed** — none at or below 45 s for D11 and D14 (D14 in one stall shape only; see
its bullet below), 400 s for D12. That is a floor,
not a demonstration of absence: D11 is additionally bounded **above 90 s, not shown
absent**, since the reference installed a CLI that answered at 90 s.
**All three are off by default now** — D11's, D12's and, since D14's flip, the
`ldd` one too — so a stock claustrum applies **none** of them, on linux or
anywhere else. The last two behaviours have no frame at all.

The D11, D12 and D14 bullets below therefore describe what an operator turns on,
except where a sentence says otherwise. An opted-in download bound always surfaces
a `cliError` when it fires — at the shipped default it fires never; an opted-in
runnability timeout on the cache-hit check can surface as the **absence** of an
error — but only when the replacement answers in time; if it is just as slow, both
probes time out and the run fails after ~2× the deadline with the cached binary
left in place:

- **The download can be bounded, but is NOT by default — opt-in divergence
  (D12).** With `-cli-download-timeout` unset (or `0`) the download has **no bound
  at all** — `http.Client{Timeout: 0}` is the stdlib's own "no timeout" — which
  matches the reference on every input measured. Opt in with
  `-cli-download-timeout <duration>` or the `cli-download-timeout` key in
  `claustrum.conf`. Only the `-cli-download-timeout 5m` row below is the old
  hardcoded default; the other two are the reference and claustrum at the new one.
  Measured 2026-08-07 with a valid 30-byte zstd blob dribbled one byte at a time
  over ~324 s and a correct `-cli-checksum`: the reference installs it at 324 s
  with no `cliError`, claustrum at the new default installs it at 324 s with no
  `cliError`, and claustrum with `-cli-download-timeout 5m` — the value that
  shipped — fails the same download at 300 s with `cliError "download failed:
  context deadline exceeded (Client.Timeout or context cancellation while reading
  body)"`. The fixture straddles the retracted bound on purpose; a shorter dribble
  cannot discriminate, since the old build would have installed it too. ⚠️ A D12 probe **must** use a valid zstd body: with an invalid
  one the reference answers `decompressing: invalid input: magic number mismatch`
  at 0 s, which is D13's ordering, not a download bound. The
  reference showed no bound at or below 400 s: measured against a server that
  sends headers and then never sends the body, it was still downloading when the
  harness stopped it at 400 s while claustrum
  returned at 300 s with `cliError "download failed: context deadline exceeded
  (Client.Timeout or context cancellation while reading body)"`. A real 629 MB
  body completes on both. But `http.Client.Timeout` bounds the whole exchange, so
  an honest download merely too slow to finish within the configured duration trips
  it as surely as a black hole does — the control passed because it arrived in time, not because
  it was honest. (That half is **no longer derived**: the 324 s straddling run in
  the D12 bullet above measures it directly — an honest download failed at 300 s by
  the value that shipped and completed by the reference. The never-arrives row
  remains measured on the reference only.)
- **The `--version` runnability probe can be bounded, but is NOT by default —
  opt-in divergence (D11).** With `-cli-probe-timeout` unset (or `0`) the probe
  runs with **no deadline at all**, matching the reference on every input measured
  (still running at 45 s on a CLI that never answers; installing one that answers
  at 90 s). Above 90 s the reference is unmeasured, so this is parity with the
  observed behaviour rather than a proof that it has no deadline at all. Except
  where a sentence says "at the default", everything below describes what an
  operator opts into
  by passing `-cli-probe-timeout <duration>` or setting the `cli-probe-timeout` key
  in `claustrum.conf`; the figures are the old hardcoded 15 s default, which shipped
  in every release up to and including 1.7.3 and in every build before this change.

  The reference showed no deadline at or below 45 s: measured
  with a CLI that hangs on `--version`, it was still running when the harness
  stopped it at 45 s, where claustrum bounded at 15 s returns at 15 s (control: a
  CLI that answers instantly returns at 0 s on both; claustrum with the deadline
  off is likewise still running when cut at 45 s — but on a planted `sleep 120` in
  a separate run, not the reference row's hang-forever CLI under the same probe). It also **installed a 90 s CLI, waiting 91 s**.
  Those figures bound the reference's deadline above 90 s; none shows it is
  absent. **A CLI answering within the configured deadline
  is expected to behave identically — derived from the constant, not bisected;
  slower ones diverge, and not only broken ones.** A deadline is a threshold, not a
  hang detector — measured 2026-08-07 with a CLI that
  answers honestly in 20 s, the reference installs it (no `cliError`, 20 s) while
  claustrum bounded at 15 s fails with `cliError "installed cli at <path> is not
  runnable"` and deletes the staged binary, leaving the cli-dir empty. A control
  CLI answering instantly installs on both. So that string is reachable on a
  working CLI, not only a broken one — **which is why the bound is now off by
  default.** What an opted-in timeout reports depends on which of
  the two probe sites hit it: after extraction it is
  `cliError "installed cli at <path> is not runnable"`, while on the cache-hit
  check a timeout is indistinguishable from a cache miss and the install simply
  proceeds — ending at `"cli <v> missing and no --cli-url or --cli-zst provided"`
  only when no source flag was given, which a plainly missing file produces too.
  **With `-cli-url` present, and the downloaded CLI answering in time, that shape
  emits no `cliError` at all**: the run downloads, installs, passes the probe on
  the fresh binary and reports `cliWasPresent:false`. If the replacement is just as
  slow, both probes time out and the run ends after ~2× the deadline at `installed
  cli at <path> is not runnable`, with the cached binary left in place because the
  rename is never reached. (**~2× measured once**, as 30 s at a 15 s deadline; on
  the `-cli-url` shape the download time is added on top.) That is the divergence at its purest — an opted-in
  claustrum recovers silently from a stale hanging CLI, where the reference was
  still wedged on it when the harness stopped the probe. The observable is the
  absence of an error, not the presence of one. **At the default, none of this
  happens: claustrum waits with the reference.**
- **The `ldd --version` libc probe deadline is OFF by default — intentional
  divergence (D14), opt-in, linux only.** Set it with `-libc-probe-timeout <dur>` or
  the `libc-probe-timeout` key in `claustrum.conf`; at the default no deadline is
  armed at all, so an `ldd` of any latency is waited for, as the reference waits.
  ⚠️ **Do not confuse the key with `cli-probe-timeout`** — that one bounds the
  `<cli> --version` runnability probe (D11) and applies on every platform; this one
  bounds `ldd --version` and only exists on linux. It was flipped rather than
  justified: it was a threshold with no escape hatch whose honest-path cost was
  untested in either direction, and an untested conjunction is not a justification.
  Off linux the probe never runs
  (`libc_other.go` returns `""`), so no bound exists there; on linux
  `detectLibcWith` returns `"musl"` from the loader glob **before** spawning `ldd`,
  so it cannot fire on a host that glob matches. **Measured: the reference applies
  no deadline here at or below 45 s** — established in the stall shape where
  nothing survives the kill; the surviving-child shape cannot show it, since
  claustrum has a deadline and looks identical there. `libc` is a field of the
  `__INSTALL_RESULT__` facts, so a fallback is wire-visible in principle — but in
  practice the fallback and the true value coincide for an honest-but-slow `ldd`: a
  host **whose loader the glob matches** returns `"musl"` without spawning `ldd` at
  all (the predicate is the glob, not the host — a musl box the glob misses does
  reach the probe), and a glibc host's fallback *is* `"glibc"`. The field moves only where `ldd`
  reports musl **and exits 0** while `/lib/ld-musl-*.so.*` does not match — the exit
  code is load-bearing, since a faithful musl `ldd --version` prints to stderr and
  exits 1, and `classifyLibc` then falls back to `"glibc"` whether or not the bound
  fired — ⚠️ narrow, but not
  cosmetic, since Claude Desktop uses `libc` to choose which CLI build to download
  (a **driver** claim the parity harness cannot settle; see
  [`ARCHITECTURE.md`](ARCHITECTURE.md) → *Driver claims and their provenance*).
  🔴 **The bound fires in only one of the two stall shapes**, and this bullet used
  to claim the divergence was total. Measured: with a stalled `ldd` that leaves
  nothing holding its output pipe, an **opted-in** claustrum falls back at its
  deadline and emits a complete
  `__INSTALL_RESULT__` where the reference emits nothing at 45 s (the measured run
  used the retracted 5 s default; at the shipped default claustrum does not fall back
  at all, and this row's claustrum column becomes the reference's); with one that
  leaves a surviving child, **neither binary replies at 45 s**. See IMPROVEMENTS
  → D14.
- **The local `-cli-zst` blob is consumed once decompression succeeds**, not only
  on a fully successful install. An extracted CLI that fails the `--version`
  runnability check still costs you the blob. A blob that is not valid zstd is
  left alone, because decompression never succeeded (the staging file is created
  before decompression runs, so its existence is not the boundary). Measured
  against the reference on four fixtures that bracket the decompress step, where
  it behaves the same way on all four. The narrow window between decompression
  and the rename (`chmod`, the destination clear) was not provoked — it sits on
  the consumed side by construction, not by observation.
- **`ldd` is executed only when the musl loader glob does not match.** On a host
  carrying `/lib/ld-musl-*.so.*` the marker decides on its own and no `ldd`
  process is started at all. Measured with a stand-in `ldd` on `PATH` that
  records its own invocation: the reference does not start it on that path
  either, and with the marker masked both binaries reach the stand-in — so
  "not started" is an observation and not a fixture that never fired.

Checksum + error framing (probe-verified):

- `-cli-checksum` is verified on the download (`-cli-url`) path
  **unconditionally** — an empty `-cli-checksum` still fails
  (`checksum mismatch: expected=, actual=<sha>`).
- **Verify happens BEFORE decompress — intentional divergence (D13).** The
  reference decompresses first and aborts on the first invalid bytes. A blob that is
  **both** undecompressable **and** wrong-checksummed tells them apart — but ⚠️ **the
  divergent string is not always `checksum mismatch`**: a transfer that dies
  mid-stream never reaches the checksum on claustrum at all. Measured at `5db5e4a`:

  | input | reference | claustrum |
  |---|---|---|
  | origin serves a **short artifact** (`Content-Length` matches the short body), checksum of the intended full blob | `decompressing: unexpected EOF` | `checksum mismatch: expected=…, actual=…` |
  | bad-magic blob + wrong checksum | `decompressing: invalid input: magic number mismatch` | `checksum mismatch: expected=…, actual=…` |
  | **interrupted transfer** — `Content-Length` says full, connection reset at 60 % | `decompressing: read tcp …: connection reset by peer` | `download failed: read tcp …: connection reset by peer` |
  | *control:* corrupt blob + correct checksum | `decompressing: invalid input: …` | **same** |
  | *control:* valid blob + wrong checksum | `checksum mismatch: …` | **same** |
  | *control:* short artifact + checksum **of the short bytes** | `decompressing: unexpected EOF` | **same** |
  | *control:* valid blob + correct checksum | **installs** | **installs** |

  The short-bytes control is what makes the rows readable: giving claustrum a checksum that
  *matches the short bytes* makes it answer exactly like the reference, which is how
  we know the `checksum mismatch` above comes from the **ordering** and not from the
  truncation.

  🔴 **The trigger is REACHABLE on an honest path, and this bullet used to imply
  otherwise** by naming only the bad-magic shape. But the reachable case is
  narrower than "flaky network", and the two shapes must not be merged:
  - **An origin serving a SHORT artifact** (a bad mirror, a partial upload, a proxy
    answering with a stale short object) reaches the checksum, so you see
    `checksum mismatch` from claustrum where the reference reported a decompression
    error. That is D13, not drift.
  - **A genuine INTERRUPTED transfer never reaches the checksum on claustrum** —
    `fetchToFile` returns `io.Copy`'s error first — so it surfaces as
    `download failed: <transport error>` against the reference's
    `decompressing: <transport error>`. Triaging a flaky link by looking for
    `checksum mismatch` will therefore miss it.

  It stays always-on — not because a rule clears it, but because matching would mean
  feeding unverified bytes to the decompressor, and **both binaries fail the install**
  anyway, so neither leaves a usable CLI.
  ⚠️ The two rows measured for disk state — the short artifact and the interrupted
  transfer — are **not** identical: the reference creates an empty
  cli-dir, claustrum creates none — so "the only delta is diagnostic text" is false.
  ⚠️ **That holds only when the cli-dir did not already exist** (measured
  2026-08-08): pre-create it and claustrum's failing install leaves exactly what the
  reference leaves, because the diverging path returns at the checksum comparison
  before the cli-dir would be created. It does not self-heal — two failing installs
  from a fresh cli-dir leave the same split — and a *successful* install is
  identical on both binaries in either pre-state.
  🔴 **D13's always-on status is therefore UNRESOLVED**, recorded in IMPROVEMENTS
  as unresolved rather than justified — and since D14's flip it is the ONLY entry
  there.
- Input/decompress failures surface as `cliError` strings:
  `opening input: <err>` (zst read) and `decompressing: <err>` (bad zstd blob).
- **A decompressed CLI (or a download body) over the opt-in cap** →
  `cliError "decompressing: decompressed CLI exceeds <n> bytes"` /
  `"download failed: response exceeds <n> bytes"`. **Not reachable by default** — the cap is `0`
  (off), matching the reference. **Intentional divergence** (D10), enabled with
  `-max-cli-bytes <n>` or `max-cli-bytes = <n>` in `claustrum.conf`. Measured at
  `5db5e4a` on the `-cli-zst` path with a 600 MiB payload (21 KB compressed): the
  reference decompressed all of it and failed only at the runnability check
  (`installed cli at <path> is not runnable`), which claustrum now answers
  identically. The `-cli-url` half was measured separately with a 629 MB
  incompressible body — same result, and the cap-on control answers
  `download failed: response exceeds 536870912 bytes`, proving the probe reaches
  that limit. (Both disprove a cap at or below ~600 MiB; neither proves the
  reference has none above it.)
- **A download slower than the opt-in bound** → `cliError "download failed: context
  deadline exceeded (Client.Timeout or context cancellation while reading body)"`.
  **Not reachable by default** — the bound is `0` (off), matching the reference,
  which was still downloading at 400 s against a body that never arrives and
  completes one that takes 324 s. **Intentional divergence** (D12), enabled with
  `-cli-download-timeout <dur>` or `cli-download-timeout = <dur>` in
  `claustrum.conf`. It bounds the whole exchange, so an honest download merely
  slower than the value trips it: measured, `-cli-download-timeout 5m` fails a
  324 s download that the reference and the new default both complete.
- **A CLI slower than the opt-in runnability deadline** → `cliError "installed cli
  at <path> is not runnable"` from the post-extraction probe, or (from the
  cache-hit probe) `"cli <v> missing and no --cli-url or --cli-zst provided"`, or
  no `cliError` at all. **Not reachable by default** — the deadline is `0` (off),
  matching the reference, which was still running at 45 s on a CLI that never
  answers and installed one that answered at 90 s. **Intentional divergence**
  (D11), enabled with `-cli-probe-timeout <dur>` or `cli-probe-timeout = <dur>` in
  `claustrum.conf`. The three shapes and their end states are in the D11 bullet
  above; note that the same string is also produced by a genuinely broken CLI on
  both binaries, so it does not by itself identify a timeout.
- A cli-dir that cannot be created is reported with a **`mkdir cli dir: `**
  prefix, e.g. `mkdir cli dir: mkdir /ro/nested: permission denied`.

Staging and cleanup (probe-verified):

- The CLI is staged at **`<cli-dir>/.fetch-<random>`** (mode `0600`) and renamed
  into place, never at `<cliPath>.tmp`. The name matters: the orphan sweep below
  matches `.fetch-*`, so an interrupted install's litter is reclaimed.
- A `-cli-url` download lands beside it at **`<cli-dir>/.blob-<random>`** when the
  cli-dir already exists — on a **first install it lands at
  `$TMPDIR/claustrum-fetch-<random>` instead**, because `fetchToFile` runs before
  `ensureCLI` creates the directory, so the in-cli-dir `os.CreateTemp` fails and
  falls back. In that case the notes below about the sweep and the prune do not
  apply, since the file is not in the cli-dir at all. Where it does land beside the
  destination, the
  different prefix is deliberate — the sweep must **not** claim it. The sweep runs
  after every attempted install, so a `.fetch-*` blob could be removed by a
  concurrent install's sweep together with the staging file, leaving the
  retry-on-`errStagingVanished` with no source to re-read. The orphan **prune**
  skips it for the same reason: `pruneCLI` counts every non-directory in the
  cli-dir as a CLI version, so an in-flight blob would sort newest, consume a
  `-cli-keep` slot and evict a real binary. For the same reason a `-cli-version`
  starting `.blob-` is **refused** (`cli version "…" collides with the install
  download blob`) — it would install fine and then be exempt from the prune census
  forever, never counted against `-cli-keep` and never evicted. That is the mirror
  of the sweep-collision refusal beside it, on the same input. It is removed by the install itself on
  every path; only a SIGKILLed download leaves it behind, and nothing reclaims
  that. (This staging file is claustrum's own. No frame changes either way.)
- **What each binary has on disk mid-download — measured 2026-08-08, filling the
  gap this bullet used to record as never measured.** Listing the cli-dir against a
  deliberately slow origin, then reading the first bytes of whatever is in flight:

  | | in-flight file | first bytes |
  |---|---|---|
  | reference, cli-dir absent or pre-created | `<cli-dir>/.fetch-<random>` | the **decompressed** CLI's |
  | claustrum, cli-dir absent | `$TMPDIR/claustrum-fetch-<random>` | zstd magic — the **compressed body** |
  | claustrum, cli-dir pre-created | `<cli-dir>/.blob-<random>` | zstd magic — the **compressed body** |

  So the reference writes decompressed output into the cli-dir as the stream
  arrives, in both pre-states, and (from the P0 row, where the directory did not
  exist) creates it before the download **completes** — which is
  the same behaviour D13's entry infers from `decompressing: <transport error>`
  surfacing on an interrupted transfer. Claustrum instead has the *compressed body*
  on disk, in `$TMPDIR` on a first install and in the cli-dir once it exists.
  ⚠️ **These are different artifacts, so do not read the rows as a like-for-like
  location difference** — an earlier version of this bullet called the reference's
  file "the download body", which the probe had not established. Claustrum's two
  rows are the control that makes the reference row readable: the same instrument
  reads zstd magic there, so it can tell the two apart.
  The **end state is identical on both binaries in both pre-states**, so no frame
  moves and this is not a numbered divergence. The sampling was one listing partway
  through the transfer, so "before it completes" is what is established; creation on
  the first byte is not excluded.
- **An occupied `cliPath` is cleared, not fatal.** `rename(2)` refuses to replace
  a non-empty directory, so whatever sits there is removed first and the install
  succeeds. If it cannot be removed the failure is reported as
  `clearing stale dir at <path>: <err>`. The clear runs **only when `cliPath` is a
  directory**, which is the only shape `rename(2)` refuses — a regular file is
  replaced atomically. An installed CLI is always a regular file, so it is never
  the thing being cleared, and a destination is never sacrificed for an install
  that cannot finish. If the staging file has vanished (a concurrent sweep took
  it) the result is `staging file vanished before install: <err>` and `cliPath`
  is left untouched. Clearing unconditionally destroyed it: the already-installed
  CLI was deleted and nothing replaced it, leaving an empty cli-dir. End states
  match the reference for every destination shape — absent, regular file, and
  non-empty directory.
- **Divergence (claustrum-only hardening, D6): `-cli-version` must name a single
  path component.** That clearing step is an `os.RemoveAll` on
  `filepath.Join(cliDir, cliVersion)`, so a version that reaches outside the
  cli-dir deletes unrelated data recursively. Two ways it can, both measured
  against `5db5e4a`, and **the reference destroys the target on both**:

  | `-cli-version` | why it escapes |
  |---|---|
  | `../victim` | `Join` **cleans**, so `cliPath` lands beside the cli-dir |
  | `link/1.0.0` | an intermediate symlink under the cli-dir, followed at open time |

  claustrum answers `cli version "…" must be a single path component` in
  `cliError` and touches nothing. `.` and `..` are refused for the same reason
  (`.` resolves to the cli-dir itself), and both `/` and `\` are rejected on
  every OS so the accepted set does not change with the platform.

  A single component rather than a containment check, because a *lexical*
  containment check accepts `link/1.0.0` — it is lexically inside the cli-dir —
  and `EvalSymlinks` would only add a TOCTOU window before the `RemoveAll`.
  Nesting costs nothing to give up: **the reference does not support a nested
  version either**, failing `sub/2.0.0` with
  `creating temp file: … no such file or directory` because it never creates the
  nested parent. A final component that is itself a symlink stays legal and
  safe — `os.RemoveAll` unlinks a symlink rather than following it.

  The real client passes a bare version string (`1.0.86`, `2.0.0-beta.1`, a
  commit sha, `latest`, `1.0.86+build.5` — all measured as accepted), so every
  honest path is byte-identical. Same shape as the `remote-server.log` refusal
  above (D8) and D1 below.
- **Divergence (claustrum-only hardening, D7): `-cli-version` must not collide
  with the orphan sweep.** The sweep below claims `.fetch-*` and `*.zst`, and it runs
  after *every* attempted install — so `-cli-version .fetch-x` or `1.0.zst`
  installs correctly and is deleted moments later in the same run. Measured at
  `5db5e4a`: reference **and** claustrum both finish with an **empty cli-dir and
  no `cliError`**, reporting success while having installed nothing. claustrum
  now answers `cli version "…" collides with the install temp sweep` instead.
  Unlike the escape rules above this gives up exact parity, on the grounds that
  an error beats a success that installed nothing. The sweep predicate and this
  check share one definition, so they cannot drift apart.
- The orphan sweep removes both **`.fetch-*`** and **`*.zst`** entries from the
  cli-dir, with `os.Remove` per entry — so it clears files and *empty*
  directories and silently leaves a non-empty `.fetch-dir/` in place. Unrelated
  files (a `README`) survive. The sweep runs whenever an install was attempted,
  succeeded or not; the `-cli-keep` prune runs only on success.
  - The sweep is **unconditional**, matching the reference. claustrum stages its
    extract at `.fetch-<random>` in this same namespace and holds it across the
    `--version` probe, where the reference shows only the installed version
    (mid-*download* the reference does have a `.fetch-<random>` of its own — see
    the staging table above; the difference is the window), so a concurrent install can reclaim
    another's staging file. That is handled by a **single retry** of the
    stage-verify-rename step rather than by narrowing the sweep: a name-only guard
    cannot tell a staggered peer's in-flight file from litter, and narrowing it
    would also diverge. Recovering from the loss is exact.
### Behavior shared by every mode

- **Default socket** — when `-socket` is omitted, `-serve`/`-bridge`/`-stop`
  fall back to `~/.claude/remote/rpc.sock`. `-serve` **creates** the parent
  directory (mode `0700`) if it is missing — see *Daemon startup* above — so a
  bare `-serve` on a fresh machine works. `-bridge`/`-stop` do not create it and
  still fail with `connect: no such file or directory` when no daemon has run.
- **No mode given** →
  `claustrum: one of --version/--install/--serve/--bridge/--stop is required`
  on stderr, exit `2` — no usage dump. An *unknown flag* still gets the stdlib
  `flag` error + usage, exit `2`.

#### Intentional divergence: `-cli-zst` checksum (claustrum-only, D1)

See [IMPROVEMENTS.md](IMPROVEMENTS.md) D1 for history.

- The **reference** never checksum-verifies the local `-cli-zst` (SFTP-upload)
  path — it trusts the already-authenticated channel, so a wrong/empty checksum
  is ignored and the blob installs.
- **Claustrum** verifies `-cli-zst` **when (and only when) a `-cli-checksum` is
  supplied**, rejecting a corrupt/tampered blob with the same
  `checksum mismatch: expected=<x>, actual=<y>` error. The source blob is left
  intact, not consumed.
- An **absent/empty** `-cli-checksum` stays trusting — byte-identical to the
  reference — so honest callers are unaffected.
- The observable delta, for a *supplied wrong* checksum only: a valid blob the
  reference would install now returns `checksum mismatch` (was success), and a
  corrupt blob returns `checksum mismatch` instead of `decompressing: <err>`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the `-install` facts schema and the
deployment lifecycle, and [EXAMPLES.md](EXAMPLES.md) for runnable snippets.
