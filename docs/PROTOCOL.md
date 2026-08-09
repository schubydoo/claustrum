# claustrum protocol reference

claustrum speaks newline-delimited **JSON-RPC 2.0** over an `AF_UNIX`
`SOCK_STREAM` socket. This document is the complete wire contract, and the same
contract the validation battery checks byte-for-byte. All reference behaviour was
probed at `5db5e4a` unless a line says otherwise; the divergence catalog, its
rules, and the reopen triggers live in [`DIVERGENCES.md`](DIVERGENCES.md).

## Transport

- One JSON object per line (NDJSON). No length prefix, no binary framing.
- A single request line is capped at **1 MiB** (`bufio` max token = `1024*1024`):
  a line up to 1048575 bytes is served, 1048576+ closes the connection with no
  reply. Large `process.stdin` payloads must be chunked under this.
- `AF_UNIX` stream socket, created mode `0600` (owner only).
- The connection is **persistent**: it stays open after a response, and id-less
  stream notifications arrive on it asynchronously.
- A connection's requests are dispatched **concurrently** — responses may arrive
  out of request order; match them by `id`.

### Named-pipe transport (Windows, opt-in)

A strictly additive claustrum extension (CT-5) — the reference daemon has no such
transport. Off by default and byte-for-byte identical to the reference when off.
Enabled with `-serve -listen-pipe` (or `listen-pipe = true` in `claustrum.conf`),
claustrum *additionally* serves the **exact same** NDJSON JSON-RPC dispatch over a
**Windows named pipe**, concurrently with the socket — same wire contract, field
ordering, framing, and `"auth"` handshake. It exists so a Windows client that
cannot consume `AF_UNIX` (notably Python `asyncio`, whose Unix transports are
Unix-loop-only) can still connect.

- **Windows-only.** Ignored with a warning on other platforms.
- **Name + discovery.** claustrum chooses the name
  (`\\.\pipe\claustrum-<random-instance-id>`) and publishes it to **`rpc.pipe`** in
  the socket's directory (beside `rpc.sock` / `daemon.token`), written atomically
  before the pipe accepts and before the ready banner, removed on graceful
  shutdown. The client reads that file to learn the opaque name.
- **Stale-file invariant.** Because the name is per-boot-random, `rpc.pipe` exists
  **iff** a pipe is actively served this boot; any leftover from an unclean crash
  is removed at startup, so a client can never dial a stale name.
- **Owner-only + local**, by two independent mechanisms: an owner-only DACL (SDDL
  `D:P(A;;GA;;;<current-user-SID>)`, the named-pipe analogue of the socket's
  `0600`) **and** remote-client rejection at creation
  (`FILE_PIPE_REJECT_REMOTE_CLIENTS`, set by go-winio's `ListenPipe`). See
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

See [`DIVERGENCES.md`](DIVERGENCES.md) → CT-5 for the full contract.

## Authentication

Every request carries a top-level `"auth":"<token>"` — **except `server.shutdown`,
which is not authenticated at all.** A shutdown frame whose `auth` member is
absent, empty, wrong, or valid all stop the daemon, and `-stop` sends no `auth`
member. This matches the reference and is load-bearing: the Desktop client tears
the daemon down with `server --stop --socket <sock>` from a bare SSH command line,
with no `CLAUDE_RPC_TOKEN` in its environment. The exemption covers auth **only** —
a shutdown frame with a bad or absent `jsonrpc` version is still rejected `-32600`
and the daemon stays up. Every other method rejects an unauthenticated request
with `-32001 Unauthorized: invalid or missing auth token` (also logged
`[Server] Unauthorized request: method=…, id=…`).

The server's expected token comes from `-token-file` (read once at startup, then
**unlinked**) or `-token-fd` (read from an open descriptor, forwarded to the
detached child over a pipe — no temp file).

**No claustrum mode reads `CLAUDE_RPC_TOKEN`** — not `-serve`, not `-bridge`, not
`-stop`. `-bridge` is a dumb relay and does not inject auth: whatever speaks
through it must include `"auth"` itself (from the `daemon.token` handshake below or
from its launcher). claustrum's only remaining dealings with the variable are to
*remove* it — unset before daemonizing, stripped from every spawned child — so a
token never reaches a child through the environment.

### Token persistence (`daemon.token`)

Once the socket is listenable, the daemon writes the token to **`daemon.token`** in
the socket's directory (mode `0600`, written atomically via a `daemon.token-*` temp
file + rename), and **unlinks it on graceful shutdown**. This lets a client
reconnect to an already-running daemon and re-authenticate after the original
`-token-file` was unlinked / the `-token-fd` pipe closed. The write is
token-source-agnostic (it uses the in-memory token) and best-effort: a failure is
logged (`[daemon] failed to persist token: …`) and non-fatal. Added by reference
build `5db5e4a` and matched here (off the JSON-RPC wire — the file sits beside the
socket, not on it). An unclean kill (`SIGKILL`/crash) leaves the file behind, since
removal runs only on the graceful `server.shutdown` / `SIGTERM` path.

The fixed name + socket-dir location are the reconnect contract, so they are not
configurable. Two parity caveats match the reference and are deliberately not
"fixed": two daemons sharing one directory collide on the file, and on Windows
`0600` is not an owner-only DACL (a Go `os.CreateTemp` limitation — the per-user
session dir is the confinement).

### Daemon startup (`-serve`)

The `-serve` launcher **creates the socket's parent directory** if missing (mode
`0700`) and then **does not return until the socket path exists** — polled every
20 ms, bounded at **10 seconds**. It confirms readiness by dialing the socket and
closing again, so a freshly started daemon's log opens with a `New connection from:
@` / `Connection closed: @` pair from the launcher's own probe.

It waits for the **path to exist**, not a successful dial, and does not give up
early when the child dies. Both are measured against `5db5e4a`:

| start | what the launcher sees | outcome |
|---|---|---|
| normal | path appears, confirming dial succeeds | exit `0` |
| socket path occupied by a directory | path exists **immediately** | exit `0` (~0.01 s; reference 0.08 s) |
| child can never bind (uncreatable parent dir) | path never appears | exit `1` at ~10.04 s (reference 10.06 s) |

On timeout the launcher prints
`claustrum: timeout waiting for daemon to accept on <socket>` to **stderr**, exit
`1`. On success it prints the ready banner and exits `0`. The practical guarantee:
after a successful `-serve`, the socket is accepting before `-serve` returns.
(Measurement detail condensed out of this reference.)

### Daemon log (`remote-server.log`)

The launcher creates **`remote-server.log`** in the socket's directory (mode
`0600`, a **fresh file on every start** — any existing log is unlinked and
recreated, not truncated in place) and redirects the daemonized child's stdout and
stderr into it, so the launcher's own streams stay empty. The first line is the
ready banner (no timestamp):

```
Claustrum remote server listening on /run/user/1000/claude/rpc.sock
2026/07/31 00:17:30 INFO  [Server] New connection from: @
```

If the existing log cannot be replaced (a sticky directory holding another user's
file), claustrum **declines the log entirely** and the daemon's output falls back
to inherited stdio, rather than writing into a file another user can read. This is
**intentional divergence D8** (always-on): the reference truncates a root-owned,
world-writable log and writes into it; claustrum leaves it untouched. Not reachable
on the deployed path — the socket directory (`~/.claude/remote/`) is per-user and
not world-writable — which is why it is always-on rather than opt-in. See
[`DIVERGENCES.md`](DIVERGENCES.md) → D8.

Unlike the socket and `daemon.token`, the log is **not removed on graceful
shutdown** — it outlives the daemon so a post-mortem stays readable. The fixed name
and location are the deployment contract, not configurable.

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
client sent. Any JSON value is accepted and comes back canonicalized: a number
round-trips through a float64 (`1.0` → `1`, `1e2` → `100`,
`12345678901234567890` → `12345678901234567000`) and an object comes back with keys
sorted (`{"b":1,"a":2}` → `{"a":2,"b":1}`). Integers, strings, arrays and `null`
are unchanged. A client that matches replies by comparing the id *text* must
compare the decoded value instead.

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

Every method-level error string, verbatim, in one place. Per-method sections below
give the trigger and result shape; this table is the searchable index. Codes are
`-32602` unless noted.

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
| git.status / git.list_branches | `<go error>` e.g. `exit status 128` | -32603 (git failed, stdout parse) |
| git.status / git.list_branches | `signal: killed` | -32603, D5 opt-in only |
| git.worktree_create | `branchName is required` | |
| git.worktree_create | `not a git repository` | in `error`, `errorCode:"not_a_repo"` |
| git.worktree_create | `git worktree add failed: <combined output>` | in `error`, `errorCode:"worktree_add_failed"` |
| git.worktree_remove | `failed to remove worktree: <git output>; manual cleanup also failed: <err>` | in `error` (only if manual cleanup also fails) |
| git.worktree_remove | `worktreePath must not be or contain the home directory: …` | D2, in `error` |
| git.worktree_remove | `git worktree remove timed out after <dur>; no cleanup was attempted, and git may have partially removed the worktree` | D5 opt-in, in `error` |
| process.spawn | `Process ID is required` / `Command is required` | |
| process.stdin | `Invalid base64 data` / `Process not found` / `Process not running` | (checked in that order after decode) |
| process.stdin | `stdin offset gap: offset ahead of applied bytes` | -32003 |
| process.killAndWait | `Process ID is required` / `Invalid params` | |

`-install` reports failures inside the `__INSTALL_RESULT__` facts line as
`cliError` strings, not via exit code — catalogued in the `-install` section
below.

### Handler panic recovery

The per-request goroutine wraps dispatch in `recover()`, so a panic in any handler
is caught rather than crashing the daemon. The reply is
`{"error":{"code":-32603,"message":"recovered panic: <v>"}}` and the daemon logs
`[Server] recovered panic: method=<m> id=<id>: <v>`.

**This frame is claustrum's own, not a statement about the wire.** No input is known
to reach a handler panic (extensive fuzzing found none, and claustrum's own panic
sites are each an unreachable stdlib guard or an already-bounds-guarded slice), so
no client is known to provoke it. `-32603` is the JSON-RPC 2.0 *Internal error*
code; the message prefix, log line, and id rendering are claustrum's own
conventions, documented so an operator who sees it knows what it means — not as a
compatibility guarantee.

### Validation precedence

A request is checked in the order **parse → auth → version → method → params**:

- Auth is validated *before* the `jsonrpc` version: a request that fails both (no
  `auth` *and* a missing/wrong `jsonrpc`) reports `-32001 Unauthorized`, not the
  version error.
- Only once auth passes is `jsonrpc == "2.0"` enforced.
- **`server.shutdown` is the exception**: auth is skipped for it entirely, so a
  shutdown frame missing both `auth` and `jsonrpc` surfaces `-32600 Invalid
  JSON-RPC version` (the version gate still applies) and the daemon stays up.

### Params presence and typing

Every `files.*` / `git.*` / `process.*` method requires a `params` object;
`server.*` methods take none (a mistyped `params` on them is ignored and the call
succeeds).

- **Absent** `params` → `-32602 Invalid params` — checked *after* method existence,
  so an unknown method is `-32601` regardless.
- An **empty** `{}` is accepted and runs the method's own validation.
- **Mistyped** `params` — a wrong field type (`"maxBytes":"4"`, `"path":123`) or a
  non-object value (`"params":"x"` / `[…]`) — is `-32602 Invalid params`; the daemon
  does not coerce or ignore the decode error.
- **Unknown extra fields are ignored**, with one divergence in *how strictly* (D9).
  claustrum binds `params` into one struct per namespace (`pathParams`,
  `gitParams`), so a field valid for the *namespace* but unused by *this* method
  still participates in decoding: a **type-mismatched** value there → `-32602`
  (e.g. `files.stat {"maxBytes":"{"}`, `git.status {"baseRepo":[1,2]}`). The
  reference binds only the field the specific method reads and ignores the rest, so
  it runs with defaults. A genuinely unknown key (in neither struct) is ignored by
  both. Accepted divergence D9; see [`DIVERGENCES.md`](DIVERGENCES.md).

## Path handling

### A path must be valid UTF-8 to be addressable at all

Before any expansion or method logic, the JSON decoder replaces bytes that are not
valid UTF-8 with `U+FFFD`. A file whose **name** contains such bytes therefore
cannot be named in any request: the daemon answers about a path that does not exist
— `exists:false`, or a `chdir`/`stat` error quoting the substituted name. This is
**parity, not a divergence** — both daemons inherit it from the JSON decoder. See
[ARCHITECTURE.md → Inherited wire bytes](ARCHITECTURE.md#inherited-wire-bytes).

### Tilde expansion in path params

Every path-bearing param is tilde-expanded before the method runs — `files.*`
`path`, `extract_tar`'s `archivePath` / `destDir`, `git.*` `path` / `baseRepo` /
`worktreePath`, and `process.spawn`'s `cwd`. Branch names are refs, not paths, and
are never expanded. A leading `~` is replaced with the daemon user's home
directory, and **the remainder is then cleaned lexically** — except bare `~`, which
returns home verbatim (uncleaned).

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

**The expanded spelling is wire-visible on eight frames**, so it is contract:
`git.worktree_create` reflects `worktreePath` into `result.path` and git's error
text, and the expanded string appears in the error text of `files.stat`,
`files.read`, `files.list`, `files.validate`, `files.extract_tar` and
`process.spawn`. Two places that do *not* carry the spelling: `files.list` entry
paths are re-joined, and `git.info`'s `root` comes from git's own output. On a
trailing-separator spelling the difference is a change of *verdict*: POSIX
`stat("f.txt/")` is `ENOTDIR`, so cleaning the separator away turns a `-32603` error
frame into a success frame. Pinned by ids 15, 16, 18 and 19 of
`testdata/socket_tilde_expansion.golden.json` (id 17 sends `~//` to `files.list`
and is documentary only).

### Stat failures other than "does not exist"

`files.stat`, `files.read` and `files.validate` distinguish a path that is
**absent** from one that could not be examined:

- A genuine `ENOENT` is the "does not exist" answer in each method's own shape —
  `exists:false`, `content:"" exists:false`, and `valid:false` with
  `error:"Path does not exist"` respectively.
- **Any other stat failure is reported** with the underlying message. `files.stat`
  and `files.read` return `-32603 stat <path>: <reason>`; `files.validate` keeps its
  result shape and puts that text in its `error` field instead. Reachable reasons
  include `not a directory` (a path component is a regular file), `file name too
  long`, and `invalid argument` (a NUL byte in the path).

## Methods (19)

`server.capabilities` self-describes the set. Order as returned:

```
server.ping  server.version  server.capabilities  server.shutdown
files.list   files.validate  files.stat  files.read  files.extract_tar
git.info     git.status      git.list_branches  git.worktree_create  git.worktree_remove
process.spawn  process.stdin  process.kill  process.killAndWait  process.reattach
```

`process.killAndWait` was added by reference `7c2f88d` (between `process.kill` and
`process.reattach`), bringing the set to 19.

### server.*

| method | params | result |
|---|---|---|
| `server.ping` | — | `{"pong":true}` |
| `server.version` | — | `{"version":"<id>","platform":"<goos>","arch":"<goarch>"}` |
| `server.capabilities` | — | `{"version":"<id>","methods":[…19…],"features":["process.stdin.offset"]}` |
| `server.shutdown` | — | *no response* — the daemon stops and the connection closes |

- **`features` array** (added `7c2f88d`) follows `methods`, advertising optional
  extensions. Sole entry `process.stdin.offset` (the resumable/idempotent stdin
  contract), always present.
- **`server.shutdown` is not authenticated** — see [Authentication](#authentication).

### files.* (param: `path`)

#### files.stat
`{path}` → `{"exists","isDir","size","mode":"-rw-r--r--"}`
- Missing path → `{exists:false,isDir:false,size:0,mode:""}`.

#### files.list
`{path}` → `{"entries":[{"name","path","isDir"},…]}` (name-sorted)
- **Hidden entries omitted** — any name beginning `.` (`.git`, `.env`) is skipped,
  matching the reference.
- `isDir` is resolved by **`Stat` — symlinks are FOLLOWED**: a symlink to a
  directory is `isDir:true`, a dangling symlink `isDir:false`.
- Missing dir → `-32603 open …: no such file or directory`.

#### files.read
`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`
- `content` is **raw text**, not base64.
- Missing file → `{content:"",exists:false}` (not an error).
- A directory → `-32602 files.read: path is a directory`.
- Size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`.
- **`maxBytes` absent, `0`, or negative → the cap is `262144` (256 KiB), not
  "unlimited".** 262144 bytes reads, 262145 errors. A positive `maxBytes` is
  honored verbatim, above or below the default — the 256 KiB figure is a fallback,
  not a ceiling. The cap keys off the stat size, which is `0` for every non-regular
  kind on linux, so it never bounds a FIFO, socket or device on either binary.
- **Non-regular files: opt-in guard D4.** Off by default (parity): the reference
  reads `/dev/null` as `{"content":"","exists":true}` and blocks on a writerless
  FIFO rather than refusing either. Set `-files-read-regular-only` (or the
  `files-read-regular-only` config key) and every non-regular path answers
  `-32602 files.read: not a regular file` — a frame the reference never produces.
  The predicate is `Mode().IsRegular()` (whole, not narrowable: `/dev/null` and
  `/dev/zero` are indistinguishable by mode). See table below; full measurement
  and rationale in [`DIVERGENCES.md`](DIVERGENCES.md) → D4.

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

  The two device rows assume a **non-root** daemon (they are permission failures).
  The opted-in column is **measured** for the FIFO and `/dev/null` rows; for the
  socket and the two device rows it is **entailed** by `Mode().IsRegular()` being
  false, not separately run.
  What the default gives up: a writerless FIFO parks a request goroutine and a
  descriptor until a writer arrives, and an unbounded device read
  (`/dev/zero`) grows the daemon until the kernel OOM-kills it — both the
  reference's own behaviour, both measured (forensics condensed out of the committed docs).

#### files.validate
`{path}` → `{"valid":bool,"isDir":bool[,"error"]}`
- Missing path → `{valid:false,isDir:false,error:"Path does not exist"}`.

#### files.extract_tar
`{archivePath,destDir}` → extracts a **gzip** tar → `{"success":true,"fileCount":<n>}`

Side effects — deliberate, **not visible in the frame**:
1. **`destDir` is wiped** (`os.RemoveAll`) then recreated before unpacking —
   extraction is idempotent and destructive.
2. Entries get **owner-only fixed modes** — files `0600`, dirs `0700` (an
   executable `0755` entry still lands `0600`).
3. On success an **empty `.synced` marker** is written at `destDir` root (not
   counted in `fileCount`).
4. **`archivePath` is consumed** — removed on *every* outcome once opened (success,
   bad gzip, or unsafe path).

Errors (all in the `error` field with `fileCount:0`, which has no `omitempty`,
unless noted):
- Missing params → `-32602 archivePath and destDir are required`.
- Non-absolute/root `destDir` → `destDir must be an absolute, non-root path: …` —
  rejected before the archive is opened, so it is **not** consumed. "Root" is the
  platform's own notion (`/` on Unix; a drive root `C:\` or UNC share root
  `\\server\share\` on Windows). The root and `filepath.IsAbs` tests share one
  branch and message. Whether the reference refuses a root `destDir` at all is
  **not measured** — the guard is justified by our own consequence (a recursive
  delete of the volume), not a claim about the reference, so it is neither parity
  nor a divergence entry.
- **`destDir` is, or contains, the home directory** → `destDir must not be or
  contain the home directory: …` — **intentional divergence D2** (the reference
  wipes `$HOME` on `"destDir":"~"`). The test is *containment*: home and any
  ancestor are refused; anything **under** home (`~/.claude/…`) is accepted. See
  [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- Bad gzip → `gzip: …`.
- Zip slip → `unsafe path in archive: <entry>` (a `../` that resolves back inside
  `destDir` is allowed).
- Non-regular/non-directory entry (symlink, hardlink, device, fifo) →
  `unsupported tar entry type <c>: <entry>` — `<c>` is the tar typeflag char
  (symlink=`2`, hardlink=`1`).
- **Total uncompressed bytes over the opt-in cap** → `extraction size limit
  exceeded`. **Not reachable by default** — the cap is `0` (off), matching the
  reference. Intentional divergence D3; see the flags table under `-serve` and
  [`DIVERGENCES.md`](DIVERGENCES.md) → D3.
- clean/mkdir/marker failures → `clean destDir: …` / `mkdir destDir: …` /
  `write .synced: …`.
- Target is an existing directory → `create <entry>: open <target>: is a
  directory`.
- Parent cannot be created (e.g. an earlier entry wrote a file where this one needs
  a directory) → `mkdir parent <entry>: <os error>`. Only the `mkdir parent
  <entry>: ` prefix is contract; the tail is the OS's. Both `create <entry>: ` and
  `mkdir parent <entry>: ` name the **archive entry**, not the resolved target.

### git.* (param: `path` = repo dir; worktree ops use `baseRepo`)

#### git.info
`{path}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""}`

- `branch` via `symbolic-ref`, so it works on an **unborn HEAD** (empty repo → init
  branch name, e.g. `master`). A **detached HEAD** → `branch:"detached:<short-sha>"`.
- `root` is the absolute repo top-level (`git rev-parse --show-toplevel`), stable
  even when `path` is a subdirectory (added by reference `7cbfa471`).
- `repoSlug` and `defaultBranch` added by `7c2f88d`, both **always present** (empty
  string when undeterminable), including on the non-repo body.
- `repoSlug` is `owner/repo` from `remote.origin.url`, populated **only for a
  canonical `github.com` remote**. Rules (measured across 42 URL shapes):
    - **Scheme** must be `https`, `http`, `ssh`, `git`, or absent (scp-like
      `[user@]host:owner/repo`). `git+ssh://` and `file://` → `""`.
    - **Host** must equal `github.com` case-insensitively. `www.github.com`,
      trailing-dot `github.com.`, a port (`github.com:443`), GitLab, Bitbucket and
      self-hosted GHE all → `""`. Userinfo is stripped.
    - **Path** must be exactly two non-empty segments after one optional trailing
      `/` and one optional `.git`.
    - **Owner**: alphanumerics with *interior* hyphens only (`ac-me`, `ac--me`
      pass; `-acme`, `acme-`, `acme_corp`, `acme.co` do not).
    - **Repo**: alphanumerics plus `.`, `_`, `-`; not starting `-`; not `.`/`..`;
      not ending in a **lowercase** `.wiki` (case-sensitive, suffix-only — `GIZMO.WIKI`
      and a repo named `wiki` are accepted).
- `defaultBranch` is what `refs/remotes/origin/HEAD` points to; empty when unset.

#### git.status
`{path}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}`

- `changes` is `git status --porcelain` **stdout only**; stderr warnings never
  appear. Lines are verbatim minus the line ending. The two-character XY column is
  **positional**, so the leading space of an unstaged-only change is data (`"M  a.txt"`
  staged vs `" M b.txt"` unstaged).
- **The first line is the exception**: the daemon trims the whole porcelain blob
  before splitting, so a leading space is stripped from entry 0 only. `[" M a1"," M
  a2"]` returns `["M a1"," M a2"]`. Clients parsing by column must handle entry 0
  separately.
- Non-repo → `{"isRepo":false,"clean":false}` (full shape, unlike `git.info`).
- A failing git → `-32603` carrying the Go error string (`exit status 128`, not
  git's `fatal:` text). With opt-in D5 the same `-32603` can carry `signal: killed`.

#### git.list_branches
`{path}` → `{"isRepo":true,"branches":[…sorted…]}`
- Non-repo → `{"isRepo":false,"branches":[]}`.
- **stdout only** (a broken-ref `for-each-ref` warning must not become a branch).
- A failing `for-each-ref` → `-32603 exit status 128`; with opt-in D5, `signal:
  killed` (see D5 below).

#### git.worktree_create
`{baseRepo,branchName,worktreePath[,sourceBranch]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>"}`
- Repo is **`baseRepo`** (not `path`); absent → the daemon's cwd repo.
- Missing `branchName` → `-32602 branchName is required`.
- Resolved repo isn't git → `{success:false,error:"not a git
  repository",errorCode:"not_a_repo"}` — checked before the add.
- Other failure → `{success:false,error:"git worktree add failed: …",errorCode:"worktree_add_failed"}`.
  The tail is git's **combined** output (add writes fatal to stderr, stdout empty),
  e.g. `"git worktree add failed: Preparing worktree (new branch
  'dup')\nfatal: a branch named 'dup' already exists"`.
- `sourceBranch` omitted → defaults to the repo's current branch (echoed back). On
  an **unborn HEAD** the source resolves empty, the add infers an orphan branch and
  succeeds, and `sourceBranch` is omitted from the result.

**Worktree population.** `git worktree add` checks out tracked files only, so the
daemon then seeds the new worktree (copies are best-effort; failures never fail the
request):
- **`.claude/` is copied recursively and unconditionally**; an absent one is
  skipped silently.
- **`.worktreeinclude`** (repo root, `.gitignore` syntax) is an **include**
  manifest: `git ls-files --others --ignored --exclude-from=.worktreeinclude`
  (without `--exclude-standard`), so a gitignored file the manifest doesn't name is
  *not* copied. Without the manifest, no untracked file is copied.
- **Symlinks are skipped.** **Manifest entries must be plain filenames** — a path
  `git ls-files` C-quotes (tab, quote, backslash, non-ASCII) is silently not
  copied (a reference limitation reproduced for parity).
- **Copies do not preserve source mode** (created 0666-subject-to-umask): an
  executable arrives non-executable, a `0400` source is widened. Matches the
  reference; treat the manifest as naming configuration, not secrets or scripts.
- An **opted-in `-git-timeout`** (D5) killing the `git ls-files` skips **every**
  manifest-selected file while the reply is still `{"success":true}` — a silent,
  wire-invisible loss. Off by default.

#### git.worktree_remove
`{baseRepo,worktreePath[,branchName]}` → `{"success":true}` (lenient)

- Runs `git worktree remove --force`. **Whenever git exits non-zero — for any
  reason — the daemon then removes `worktreePath` itself, recursively, and still
  answers `{"success":true}`.** So this method is a recursive delete of the
  caller-supplied `worktreePath` whenever git is unhappy (a locked worktree, an
  ordinary directory, a non-repo `baseRepo`). That is **reference behavior**,
  matched deliberately — treat `worktreePath` as a path you are asking to have
  removed, not as a filter. Only when the manual cleanup *also* fails does the reply
  carry `{"success":false,"error":"failed to remove worktree: <git output>; manual
  cleanup also failed: <err>"}`.
- Naming a non-existent branch still answers a bare `{"success":true}` — hence
  "lenient".
- **One input is exempt — claustrum-only hardening D2.** A `worktreePath` that
  **is, or contains, the home directory** is refused before git runs:
  `{"success":false,"error":"worktreePath must not be or contain the home
  directory: …"}`. Containment is judged **after** the path is resolved against the
  daemon's working directory, so `"."` / `".."` are refused too when that cwd is
  home or a descendant — the verdict on a relative `worktreePath` is not predictable
  without knowing where the daemon started, so **send an absolute path**. An empty
  or omitted `worktreePath` is exempt (`os.RemoveAll("")` is a no-op). Same
  containment test as `files.extract_tar`. See [`DIVERGENCES.md`](DIVERGENCES.md) → D2.
- **A relative `worktreePath` is resolved twice**, against different roots: git runs
  with `-C <baseRepo>` (repo-relative), the manual cleanup resolves against the
  **daemon's working directory**. So the fallback can delete a directory git never
  looked at — parity, and alarming; send an absolute path.
- The worktree stays **registered** — deleting the directory does not remove
  `$GIT_DIR/worktrees/<name>`, so `git worktree list` still shows it and a later
  create at the same path fails `already registered`. Neither binary prunes.
- **`gitTimeout` (D5) does NOT authorise the deletion**, and off by default this
  whole timeout arm needs an opt-in. When armed it answers
  `{"success":false,"error":"git worktree remove timed out after <dur>; no cleanup
  was attempted, and git may have partially removed the worktree"}` and removes
  nothing. See [`DIVERGENCES.md`](DIVERGENCES.md) → D5.

### process.* (the agent/MCP-hosting core)

The client supplies its own `id` (any string). Output is delivered as id-less
stream notifications, **buffered** for later replay.

#### process.spawn
`{id,command[,args][,cwd][,env][,wantPid]}` → `{"success":true}`, then stream frames
- `args`: string[]. `env`: `{KEY:VAL}` merged over the daemon environment.
- Missing `id` → `-32602 Process ID is required`; missing `command` →
  `-32602 Command is required`.
- Reusing a still-live `id` succeeds and replaces the registry entry (like the
  reference). **Divergence:** claustrum also tears down the now-orphaned previous
  process tree (subscribers dropped first so no stray frames arrive under the reused
  id) — OS-level only, no wire change; the reference leaves the old process running.
- **`wantPid` opt-in (CT-1, claustrum-only).** With `"wantPid":true` the reply gains
  two fields **after** `success`: `{"success":true,"pid":<int>,"startTime":<number>}`.
  `pid` is the child's OS pid; `startTime` is the **daemon's wall clock (epoch
  seconds) captured at spawn**, returned identically on spawn and reattach for the
  same process. It is an **opaque token** for PID-reuse / orphan detection — compare
  a persisted daemon value against a later daemon value for the **same id**; **do
  not** equality-compare it against an OS-read process start time (`psutil
  create_time`) — the derivations differ. Absent or `false`, both fields are omitted
  (`omitempty`) and the frame is byte-identical to `{"success":true}`. An older
  daemon ignores the unknown param (tolerant decode). See
  [`DIVERGENCES.md`](DIVERGENCES.md) → CT-1.

#### process.stdin
`{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}`
- `data` is **base64**, written to the child's stdin.
- Checks run in a fixed order (**decode → exists → running → offset**):
    - Invalid base64 → `-32602 Invalid base64 data` — returned *before* the process
      is looked up, so an unknown id with a bad payload still reports the decode
      error.
    - Unknown id → `-32602 Process not found`.
    - Known but **exited** → `-32602 Process not running`.
- **`offset` / `applied` — the resumable-stdin contract** (added `7c2f88d`,
  advertised as `process.stdin.offset`). The reply **always** carries `applied`: the
  cumulative count of stdin bytes accepted for delivery (high-water mark). `offset`
  is the byte position the caller believes this `data` starts at, making stdin
  idempotent across reconnects:
    - **absent** `offset`, or `offset == applied` → append; `applied` grows by
      `len(data)`.
    - `offset > applied` → `-32003 stdin offset gap: offset ahead of applied bytes`
      (a hole that would drop input — resend from `applied`). Nothing enqueued.
    - `offset + len(data) <= applied` (wholly applied) → **no-op**, reply adds
      `"duplicate":true`, `applied` unchanged, nothing reaches the child.
    - partial overlap (`offset < applied < offset+len`) → only the fresh tail
      `data[applied-offset:]` is written, `applied` advances to `offset+len(data)`
      (not flagged duplicate).
  `applied` counts base64-**decoded** bytes and is never `omitempty` (emitted at 0);
  `duplicate` is dropped when false. A legacy client that never sends `offset` still
  works — it just always appends.

#### process.kill
`{id[,signal]}` → `{"success":true}`
- Best-effort, **fire-and-forget** — does not wait for the child to exit (contrast
  `process.killAndWait`).
- **How wide the signal reaches depends on the signal**, on Unix:
    - `KILL` goes to the whole **process group** (negative pid) — the entire child
      tree dies.
    - **Every other signal** (`TERM`, `INT`, `HUP`, default) goes to the **direct
      child only**; a backgrounded grandchild keeps running. A graceful
      `process.kill` is **not** a tree teardown — use `signal:"KILL"` or
      `killAndWait` with `escalate:true`.
  On Windows the split does not apply — the Job Object is terminated, taking the
  tree either way.
- **Divergence:** claustrum skips the signal when the child has already exited (a
  reaped pgid can be recycled) — OS-level only, reply identical.

#### process.killAndWait
`{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}`

Added by `7c2f88d`. **Blocks until the process is gone** (up to the grace) and
reports the outcome as a *result* (an unknown id is not an error):
- Missing `id` → `-32602 Process ID is required`; absent `params` → `-32602 Invalid
  params`.
- Unknown id → `{"found":false,"died":false}`.
- Already exited → `{"found":true,"died":true,"alreadyExited":true}` (no signal
  sent).
- Live process → the graceful `signal` (default `SIGTERM`) is sent, then it waits up
  to the grace:
    - **`timeoutMs`** sets the grace. Non-positive or absent → the **3000 ms**
      default; positive values honored verbatim **up to a 30000 ms ceiling** (a
      larger value is clamped, so `timeoutMs:45000` against a signal-ignoring child
      answers after ~30 s). The `30000` ceiling is a black-box bracket `(29500,
      30500]` — the only round value in it, not a measured-exact figure.
    - **`escalate`** (default `true`): if still alive after the grace, `true` →
      escalate to a **process-group** `SIGKILL`, wait up to **7 s** for the reap
      (measured: `timeoutMs:500` against an unreapable child → the reference
      replies at 7.51 s),
      add `"escalated":true`. It is sent even when the graceful signal already killed
      the child, since a grandchild holding the stdout pipe can keep the drain
      pending past the grace. `false` → leave the process running, report
      `{"found":true,"died":false}` (no `escalated`, no SIGKILL), sparing the tree.
- A process that dies within the grace → `{"found":true,"died":true}` (no
  `escalated`).

#### process.reattach
`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`
- Replays buffered frames with **seq > fromSeq** (exclusive) to this connection,
  **transfers** the frame stream to it, then returns the result.
- **The transfer is exclusive.** A reattach does not add a second listener: any
  previously attached connection stops receiving frames for that process. This is
  what makes resume safe.
- **The cut is by `seq`, not wall-clock.** The transfer point is the reported
  `lastSeq`: the old connection can still receive a frame `<= lastSeq` slightly
  after the reply, never one above it. There is no frame that reaches the old
  connection and is absent from the new one's replay — which is what `fromSeq` is
  for.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- **Exited processes are retained for ~15 minutes, then dropped** — with their replay
  buffers — so an id last seen longer ago answers exactly like an unknown one (and
  `process.kill` on it still reports `{"success":true}`). A **running** process is
  never dropped. The sweep runs on a ~60-second timer *and* inline on every
  `process.spawn`. Wire-observed, retention only brackets to `(45 s, 960 s]`; the
  exact 15 min and 60 s are pointer-class — no wire observable distinguishes them
  from other values in the bracket. Treat `found:false` after a long gap as
  "finished and forgotten", not "never existed".
- **`stdinApplied`** (added `7c2f88d`): the process's cumulative applied-stdin byte
  count (§`process.stdin`), always present after `lastSeq`. A reconnecting client
  resumes stdin from this offset. **It is an acknowledgement, not a delivery
  receipt** — `process.stdin` returns before the child reads, so bytes accepted just
  before exit are counted even though the writer never delivered them. A client that
  must know data arrived confirms it in-band.
- **`wantPid` opt-in (CT-1).** With `"wantPid":true` **and** the process found, the
  reply appends `"pid":<int>,"startTime":<number>` **after** `stdinApplied`,
  reporting the same pid/startTime the spawn did (so a client can confirm it
  reattached to the same process, not a pid-reuse). Omitted otherwise.

### Stream notifications

```jsonc
{"type":"stream","processId":"<id>","stream":"stdout","seq":1,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"stderr","seq":2,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":0}
```

- `seq` is **per-process**, starts at 1, monotonic across stdout/stderr/exit.
- `data` is base64 for stdout/stderr. The `exit` frame carries `exitCode` and no
  `data`. A signal-terminated child reports `exitCode: -1` (not `128+signo`).
- The `exit` frame waits at most **5 seconds** after the process exits for
  stdout/stderr to reach EOF, then the daemon closes the read ends and emits it
  anyway. This matters when the command leaves a **grandchild holding the same pipe**
  (`npm run dev &`): output that grandchild writes after the cap is **not** forwarded
  (its write fails `EPIPE`). Until the frame is emitted, `process.reattach` still
  reports `running: true` — the flag flips with the frame, not the process.
- Each stdout/stderr frame carries at most one **32 KiB** read; larger output splits
  across frames. Reassemble by concatenating `data` in `seq` order. Exact frame
  *boundaries* depend on pipe scheduling and are not stable — only the reassembled
  bytes are.
- The replay buffer is **bounded at 16 MiB per process**, counted as the
  **serialized frame including its trailing newline** (the bytes a subscriber would
  receive), not the base64 `data` alone — so an exit frame costs its envelope though
  it carries no `data`. Frames drop oldest-first, whole frames at a time, once a new
  frame would exceed the cap; at least one frame is always retained (even one larger
  than the cap). So `reattach{fromSeq:0}` replays everything **still retained**, not
  necessarily everything ever emitted — `firstSeq` is the floor; compare it against
  the last `seq` you saw to detect a gap.
- A process **survives** the disconnect of the connection that spawned it; another
  connection picks it up via `reattach`. This is the multi-attach / reconnect
  mechanism.

## Daemon lifecycle (flags)

One binary, five modes (`-serve`, `-bridge`, `-stop`, `-version`, `-install`).
Everything is probe-verified against the reference unless marked **claustrum-only**.

### Flags and config keys

Every opt-in divergence flag defaults to its zero value = **OFF** (byte-identical to
the reference), and each has a matching `claustrum.conf` key. Because Claude Desktop
owns the `-serve` / `-install` argv (a driver claim — see [ARCHITECTURE.md → Driver
claims and their provenance](ARCHITECTURE.md#driver-claims-and-their-provenance)),
**the config key is the reachable knob.** Precedence: explicit CLI flag > config >
default. A disabled bound **bypasses the guard entirely** — it is never "a huge
limit".

| flag | config key | default | effect when set | mode |
|---|---|---|---|---|
| `-token-file <p>` | — | — | token source (read once, unlinked) | -serve |
| `-token-fd <n>` | — | `-1` | token from an open fd (claustrum-only) | -serve |
| `-metrics-addr <a>` | `metrics-addr` | `""` | Prometheus `/metrics` (claustrum-only, CT-3) | -serve |
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

Config-value parsing: bool keys accept `true/1/yes/on` and `false/0/no/off`;
negative or unparseable numeric values are ignored (so a typo can never silently
enable a cap); an unrecognised bool value leaves the key unset (the flag value, or
the default, stands).

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] \
          [-keep-children] [-listen-pipe] [-max-extract-bytes <n>] [-git-timeout <dur>] \
          [-files-read-regular-only]
```

Self-daemonizes (reparents to init / detached), extracts the login-shell PATH
(Unix), then runs the RPC server. On success it prints `Claustrum remote server
listening on <socket>` to stdout.

**Login-shell PATH extraction** (Unix) runs `$SHELL -l -i -c …` when `$SHELL` is an
executable file, else the first usable of `/bin/zsh`, `/bin/bash`, `/bin/sh` (**zsh
first**, matching the reference). The value reaches `process.spawn` children as
their `PATH` only — never the daemon's own environment, so it never changes how the
daemon resolves a `command`. Extraction is capped at **4 s**; a timeout **discards**
whatever the shell printed (even a valid PATH) and children fall back to the
inherited PATH.

**Token source** — required, checked **in the detached child**, not the launcher:
- Missing both flags → the launcher daemonizes anyway, the child refuses to start,
  and the launcher reports its accept timeout after ~10 s: `claustrum: timeout
  waiting for daemon to accept on <socket>`, exit `1`. The specific reason
  (`claustrum: daemonized child requires --token-file or --token-fd`) reaches only
  the child's detached stderr. This is deliberate parity (the reference exits 1 at
  ~10 s the same way). A zero-byte `-token-file` behaves identically.
- The token is read as a **line**: one trailing `\n`/`\r\n` is stripped; other
  surrounding whitespace is preserved verbatim.
- A bad `-token-file` → `claustrum: read --token-file: <err>`, exit `1`.
- `-token-fd <n>` *(claustrum-only)* reads from an already-open fd (`0` = stdin), so
  the token never touches disk; the launcher forwards it to the detached child over
  an inherited pipe.

**Daemonize sentinel** *(internal; claustrum-namespaced)* — the re-exec marker is
**`CLAUSTRUM_DAEMON_CHILD`**, not the reference's `CLAUDE_SSH_DAEMON_CHILD`. The
reference name can't serve here: a host running *inside* a real claude-ssh session
exports `CLAUDE_SSH_DAEMON_CHILD=1` ambiently, so the launcher would mistake itself
for the already-daemonized child. Observable parity is preserved separately —
`daemonizeWithToken` still sets `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's environ
so it propagates into `process.spawn` children (pinned by
`TestSpawnInheritsDaemonChildMarker`); the internal marker is unset before spawning.

**Claustrum-only extras** (off the wire; canonical detail in
[`DIVERGENCES.md`](DIVERGENCES.md)):
- **`-metrics-addr <a>` (CT-3).** Prometheus counters at `http://<a>/metrics`
  (connections, spawns/exits, reattaches, stream/stdin bytes). Off by default (no
  listener); counts only, **no auth** — bind to loopback. Bind failure logged
  (`[Server] metrics: …`), non-fatal.
- **`-keep-children` (CT-2; POSIX-only).** Off by default (graceful shutdown kills
  the whole child tree). Set, it leaves spawned children running across a restart
  (logs `[Server] -keep-children: leaving <n> running child process(es) alive across
  shutdown`); the new daemon does **not** re-adopt them, and **survivors lose their
  stdio** (stdin EOF; stdout/stderr writes → SIGPIPE, or EPIPE for a child that
  ignores it, e.g. Node), so it suits only children that tolerate dead stdio.
  Windows ignores it with a warning (`[Server] -keep-children is not supported on
Windows …`; the Job Object terminates children regardless).
- **`-listen-pipe` (CT-5; Windows-only).** See [Named-pipe
  transport](#named-pipe-transport-windows-opt-in). Setup failure logged (`[Server]
  named-pipe transport: …`), non-fatal — the socket still serves.

**Opt-in divergences on this mode** are `-max-extract-bytes` (D3),
`-git-timeout` (D5) and `-files-read-regular-only` (D4). Off = parity; their wire
frames appear in the method sections above (`files.extract_tar`, `git.status` /
`git.list_branches` / `git.worktree_remove`, `files.read`). See the flags table and
[`DIVERGENCES.md`](DIVERGENCES.md).

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

A dumb relay — what an SSH session attaches to. It injects **no** auth; whatever
speaks through it supplies `"auth"` itself. **Strict**: a dial failure is a hard
error — `claustrum: dial server: <err>` on stderr, exit `1`.

### -stop — ask a running daemon to shut down

```text
claustrum -stop -socket <p>          # no token needed, and none is read
```

Sends `server.shutdown` with **no `auth` member** — that method is not
authenticated (see [Authentication](#authentication)). **Best-effort**: a missing or
unreachable daemon is a silent no-op (exit `0`, no output); any reply is read and
discarded. Against a current daemon there is no reply, since `server.shutdown`
answers nothing and closes.

**The socket path is unlinked on every exit path**, including where the dial fails
and no daemon was reached. This is matched to the reference and is destructive on
two arms: a stale socket with no listener, and a **live foreign listener** — `-stop`
removes a socket path it did not create, so a new client dialing by path cannot
reach that listener afterwards (the listener itself stays alive). Making the unlink
conditional would be a divergence, so it is a candidate not taken — recorded under
[Candidates considered but not taken](DIVERGENCES.md#candidates-considered-but-not-taken).

> **Upgrading a live daemon.** A daemon still running from a build that predates the
> shutdown-auth-exemption change *does* require auth on `server.shutdown`, answers
> `-32001`, and keeps running. `-stop` discards the reply and exits `0` either way,
> so the caller sees success while the old daemon survives. Stop the old daemon
> before upgrading, or kill it by PID once.

### -version

```text
claustrum -version                   # → claustrum <id> (built <time>)
```

**Intentional divergence: `version-override` via `claustrum.conf` (claustrum-only,
CT-3).** An optional `key = value` file named `claustrum.conf`, read from the
directory holding the binary, gates the opt-in divergences above; **absent/malformed
⇒ stock**. If it sets `version-override` to a bare commit SHA (40-hex git SHA-1, the
string the desktop client pins; 64-hex also accepted; anything else is a no-op), the
output becomes:

```text
claustrum -version                   # → claude-ssh <sha> (via Claustrum <id>, built <time>)
```

This exists so the desktop client treats an already-deployed claustrum as
up-to-date (it keys re-upload on `<bin> --version` matching `/claude-ssh\s+(\S+)/`).
It is **CLI stdout only** — not a JSON-RPC frame — so the wire contract is untouched;
`server.version` / `server.capabilities` still report claustrum's own `<id>`. See
[`DIVERGENCES.md`](DIVERGENCES.md) → CT-3.

### -install — ensure the agent CLI

```text
claustrum -install -cli-dir <d> -cli-version <v> \
          [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>] \
          [-max-cli-bytes <n>] [-cli-probe-timeout <dur>] [-cli-download-timeout <dur>] \
          [-libc-probe-timeout <dur>]
```

Download / verify / extract / prune, then print one `__INSTALL_RESULT__<json>` facts
line (schema in [ARCHITECTURE.md](ARCHITECTURE.md)). `-install` always exits `0` —
failures are reported inside the facts as `cliError`, not via the exit code.
`-install` reaches the network **only with `-cli-url`**.

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
| `mkdir cli dir: <err>` | cli-dir uncreatable |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli version "…" collides with the install temp sweep` | D7 hardening |
| `cli version "…" collides with the install download blob` | version starting `.blob-` |
| `clearing stale dir at <path>: <err>` | occupied `cliPath` directory couldn't be removed |
| `staging file vanished before install: <err>` | a concurrent sweep took the staging file |

**Checksum + verify ordering:**
- `-cli-checksum` is verified on the `-cli-url` path **unconditionally** — an empty
  checksum still fails.
- **Verify happens BEFORE decompress — intentional divergence D13** (always-on,
  **unresolved**). The reference decompresses first; claustrum checksums first. A
  blob both undecompressable and wrong-checksummed diverges on the string: a short
  artifact yields `checksum mismatch` where the reference says `decompressing:
  unexpected EOF`; a genuine interrupted transfer never reaches the checksum on
  claustrum at all (`download failed: <transport>` vs `decompressing: <transport>`).
  Both binaries fail the install either way. See [`DIVERGENCES.md`](DIVERGENCES.md)
  → D13.
- **`-cli-zst` checksum — intentional conditional divergence D1.** The reference
  never checksum-verifies the local SFTP-upload blob. claustrum verifies it **only
  when a `-cli-checksum` is supplied** (same `checksum mismatch` error, source blob
  left intact); an absent/empty checksum stays trusting, so honest callers are
  byte-identical. See [`DIVERGENCES.md`](DIVERGENCES.md) → D1.

**Opt-in wall-clock bounds** — all three off by default, so a stock claustrum
applies **none** of them (on linux or anywhere); at the shipped defaults no
claustrum-chosen `-install` bound applies, only the stdlib transport clocks
(`net.Dialer{Timeout:30s}`, `TLSHandshakeTimeout:10s`) on `-cli-url`. Off = parity
(the reference showed no deadline at the durations probed). See
[`DIVERGENCES.md`](DIVERGENCES.md):
- **`-cli-download-timeout <dur>` (D12).** `0` = `http.Client{Timeout:0}` (no
  bound). Armed, it bounds the whole exchange, so an honest download merely too slow
  trips `download failed: context deadline exceeded (…)` as surely as a black hole
  does.
- **`-cli-probe-timeout <dur>` (D11).** `0` = no deadline on the `<cli> --version`
  runnability probe (every platform). Armed, a CLI slower than the deadline diverges
  — after extraction as `installed cli at <path> is not runnable` (staged binary
  deleted); on the cache-hit check as a silent reinstall or, with `-cli-url` and a
  timely replacement, no `cliError` at all. A threshold, not a hang detector: an
  honest-but-slow CLI trips it too. The cached binary survives every failure before
  the rename.
- **`-libc-probe-timeout <dur>` (D14; linux only).** `0` = no deadline on `ldd
  --version`. Off linux the probe never runs; on linux it cannot fire on a host
  whose musl loader glob matches (`detectLibcWith` returns before spawning `ldd`).
  **Do not confuse with `-cli-probe-timeout`** (they are one letter apart, same type,
  resolved two lines apart in main's `-install` arm — pinned by
  `TestInstallArmWiresEachFlagToItsOwnGlobal`). `libc` build selection is a driver
  claim — see [ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance).

**Opt-in size cap (D10).** `-max-cli-bytes <n>` (or `max-cli-bytes` config) governs
**both** the decompressed CLI and the download body; `0` = off (parity — the
reference took a 600 MiB payload to the runnability check). The blob is **streamed,
never buffered** (a path, not a `[]byte`, so the staging retry can re-read it), which
keeps "cap off" from meaning unbounded memory. See
[`DIVERGENCES.md`](DIVERGENCES.md) → D10.

**`-cli-version` hardening (claustrum-only):**
- **D6: must name a single path component.** The clearing step is an `os.RemoveAll`
  on `filepath.Join(cliDir, cliVersion)`, so a version escaping the cli-dir
  (`../victim`, or `link/1.0.0` via an intermediate symlink) deletes unrelated data
  — the reference destroys the target on both. claustrum answers `cli version "…"
  must be a single path component` and touches nothing. `.`, `..`, `/` and `\` are
  refused on every OS. A single-component check (not lexical containment) is used
  because containment accepts `link/1.0.0` and `EvalSymlinks` would add a TOCTOU
  window. A final component that is itself a symlink stays legal (`os.RemoveAll`
  unlinks it rather than following). The real client passes bare versions (`1.0.86`,
  `2.0.0-beta.1`, a commit sha, `latest`, `1.0.86+build.5` — all measured accepted).
- **D7: must not collide with the orphan sweep.** The sweep claims `.fetch-*` and
  `*.zst` and runs after *every* attempted install, so `-cli-version .fetch-x` or
  `1.0.zst` would install and be deleted moments later (both binaries finish with an
  empty cli-dir and no `cliError`, reporting a success that installed nothing).
  claustrum now answers `cli version "…" collides with the install temp sweep`. The
  sweep predicate and this check share one definition.

**Staging and cleanup:**
- The CLI is staged at **`<cli-dir>/.fetch-<random>`** (mode `0600`) and renamed
  into place, never at `<cliPath>.tmp` — one code path for `-cli-url` and `-cli-zst`
  alike. The orphan sweep matches `.fetch-*`, so an interrupted install's litter is
  reclaimed.
- A `-cli-url` download lands at **`<cli-dir>/.blob-<random>`** when the cli-dir
  exists; on a **first install it lands at `$TMPDIR/claustrum-fetch-<random>`**
  because `fetchToFile` (`install.go`) runs before `ensureCLI` creates the
  directory. The `.blob-` prefix is deliberately different so the sweep and the
  `-cli-keep` prune (which counts every non-directory as a version) do not claim an
  in-flight blob — which is also why a `-cli-version` starting `.blob-` is refused.
  The blob is removed by the install on every path; only a SIGKILLed download leaves
  it behind. No frame changes either way.
- **The `-cli-zst` blob is consumed once decompression succeeds**, not only on a
  fully successful install — an extracted CLI that fails the runnability check still
  costs the blob. A blob that is not valid zstd is left alone.
- **An occupied `cliPath` is cleared, not fatal.** `rename(2)` refuses to replace a
  non-empty directory, so it is removed first (only when `cliPath` is a directory — a
  regular file, which an installed CLI always is, is replaced atomically); if it
  cannot be removed → `clearing stale dir at <path>: <err>`. If the staging file has
  vanished → `staging file vanished before install: <err>`, `cliPath` untouched. End
  states match the reference for every destination shape (absent, regular file,
  non-empty directory).
- **The orphan sweep** removes `.fetch-*` and `*.zst` entries with `os.Remove` per
  entry (so it clears files and *empty* directories, leaving a non-empty
  `.fetch-dir/`); unrelated files survive. It runs whenever an install was attempted;
  the `-cli-keep` prune runs only on success. claustrum stages its extract in this
  same `.fetch-*` namespace and holds it across the probe, so a concurrent install
  can reclaim another's staging file — handled by a **single retry** of the
  stage-verify-rename step rather than by narrowing the sweep.
- `ldd` is executed **only when the musl loader glob does not match** — on a host
  carrying `/lib/ld-musl-*.so.*` the marker decides and no `ldd` starts.

### Behavior shared by every mode

- **Default socket** — when `-socket` is omitted, all modes fall back to
  `~/.claude/remote/rpc.sock`. `-serve` **creates** the parent directory (mode
  `0700`) if missing, so a bare `-serve` on a fresh machine works. `-bridge` /
  `-stop` do not create it and fail with `connect: no such file or directory` when
  no daemon has run.
- **No mode given** →
  `claustrum: one of --version/--install/--serve/--bridge/--stop is required` on
  stderr, exit `2` — no usage dump. An *unknown flag* gets the stdlib `flag` error +
  usage, exit `2`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the `-install` facts schema and the
deployment lifecycle, [`DIVERGENCES.md`](DIVERGENCES.md) for the full divergence
catalog and rules, and [EXAMPLES.md](EXAMPLES.md) for runnable snippets.
