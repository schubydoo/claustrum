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
  the pipe name the same way.
- **Owner-only + local.** The pipe is local by two independent mechanisms: an
  owner-only DACL (SDDL `D:P(A;;GA;;;<current-user-SID>)` — GENERIC_ALL to the
  daemon user's SID and to no world principal), the named-pipe analogue of the
  socket's `0600` mode; **and** remote-client rejection at pipe creation
  (`FILE_PIPE_REJECT_REMOTE_CLIENTS`, set by go-winio's `ListenPipe`), so a client
  reaching it over SMB is refused regardless of the DACL. See
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

## Authentication

Every request carries a top-level `"auth":"<token>"`. The server's expected token
comes from `-token-file` (read once at startup, then **unlinked**) or `-token-fd`
(read from an open descriptor, forwarded to the daemon over a pipe — no temp
file); for the `-bridge`/`-stop` clients it comes from the `CLAUDE_RPC_TOKEN`
environment variable. A bad or missing token →
`-32001 Unauthorized: invalid or missing auth token` (also logged
`[Server] Unauthorized request: method=…, id=…`).

The `-bridge` relay does **not** inject auth — whatever speaks through it must
include `"auth"` itself.

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
user can read. That refusal is a claustrum-only hardening on an attack path the
reference was not measured on; on every honest path the two are identical.

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
| `-32603` | internal error (e.g. `open <path>: no such file or directory`) |
| `-32003` | `stdin offset gap: offset ahead of applied bytes` — `process.stdin` with an `offset` past the applied high-water (added in `7c2f88d`) |
| `-32001` | unauthorized |

### Validation precedence (probe-verified)

A request is checked in the order **parse → auth → version → method → params**:

- Auth is validated *before* the `jsonrpc` version: a request that fails both
  (no `auth` *and* a missing/wrong `jsonrpc`) reports `-32001 Unauthorized`,
  not the version error.
- Only once auth passes is `jsonrpc == "2.0"` enforced.

### Params presence and typing

Every `files.*` / `git.*` / `process.*` method requires a `params` object:

- **Absent** `params` → `-32602 Invalid params` — checked *after* method
  existence, so an unknown method is `-32601` regardless.
- An **empty** `{}` is accepted and runs the method's own validation.
- **Mistyped** `params` — a wrong field type (`"maxBytes":"4"`, `"path":123`)
  or a non-object value (`"params":"x"` / `[…]`) — is also
  `-32602 Invalid params`; the daemon does not silently coerce or ignore the
  decode error.
- **Unknown extra fields** *are* ignored — with one divergence in *how strictly*.
  claustrum binds `params` into one struct per namespace (`pathParams`,
  `gitParams`), so a field that is valid for the *namespace* but unused by *this*
  method still participates in decoding: a **type-mismatched** value there →
  `-32602` (e.g. `files.stat {"maxBytes":"{"}`, `git.status {"baseRepo":[1,2]}`).
  The reference binds only the field the specific method reads and ignores the
  rest regardless of type, so it runs with defaults. A genuinely unknown key (in
  neither struct) is ignored by both. This only surfaces under adversarial params
  — a real client never sends them; accepted divergence, found by differential
  fuzzing.

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

> **Divergence — auth on `server.shutdown`.** claustrum validates the auth token
> for *every* method (§Auth), so an unauthenticated or wrong-token
> `server.shutdown` is rejected with `-32001 Unauthorized` and the daemon stays
> up. The **reference does not authenticate `server.shutdown`** — it stops on the
> request regardless of the token (no reply either way). Honest callers are
> byte-identical: `-stop` and the real client always send a valid token
> (`bridge.go`), and a valid-auth shutdown matches exactly (no response, the
> connection closes). Only an adversarial wrong/missing-auth shutdown differs,
> and there claustrum is the safer of the two (a bare socket peer can't kill it).
> Surfaced by differential fuzzing; intentional hardening.

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
  figure is a fallback, not a ceiling.
- **Any non-regular file → `-32602 files.read: not a regular file`.** This is an
  intentional divergence, and the only one on this method — see below.

##### Non-regular files (intentional divergence)

claustrum refuses to read anything that is not a regular file. The reference does
not, and the difference is visible two ways (probe-measured against `5db5e4a`):

| path | reference | claustrum |
|---|---|---|
| a FIFO | **never replies** — no frame at all, the request hangs forever | `-32602 files.read: not a regular file` |
| `/dev/null` | `{"content":"","exists":true}` | `-32602 files.read: not a regular file` |

The FIFO case is why the guard exists. A read of a FIFO with no writer blocks
indefinitely, and because the reference emits no frame at all, a frame-diffing
comparison cannot even see it — the request goroutine is simply consumed, and a
client waiting on that `id` waits forever. Refusing the read converts an
unbounded hang into an immediate, actionable error.

The `/dev/null` row is the cost of that guard rather than a second decision: the
check is `Mode().IsRegular()`, so it also rejects character devices the reference
reads happily. Narrowing it to permit *some* character devices would reopen the
same hazard on others — `/dev/zero` and `/dev/random` are unbounded reads, and
the reference has no protection against those either.

So a client that needs `/dev/null` semantics should treat an empty file as the
portable spelling. Everything else about `files.read` is byte-identical.

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
- Non-absolute/root `destDir` → `{success:false,error:"destDir must be an
  absolute, non-root path: …"}` — rejected before the archive is opened, so the
  archive is **not** consumed.
- Bad gzip → `{success:false,fileCount:0,error:"gzip: …"}`.
- An entry whose path escapes `destDir` ("zip slip") →
  `{success:false,fileCount:0,error:"unsafe path in archive: <entry>"}`; a `../`
  that resolves back inside `destDir` is allowed.
- A non-regular/non-directory entry (symlink, hardlink, device, fifo) →
  `{success:false,fileCount:0,error:"unsupported tar entry type <c>: <entry>"}`
  — `<c>` is the tar typeflag char (symlink=`2`, hardlink=`1`).
- destDir clean/mkdir or marker-write failures → `clean destDir: …` /
  `mkdir destDir: …` / `write .synced: …`.

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
- Copy failures are best-effort and never fail the request.
#### git.worktree_remove

`{baseRepo,worktreePath}` → `{"success":true}` (lenient)

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
      are honored verbatim (50 ms → ~50 ms, 8000 ms → ~8 s). claustrum caps an
      absurd value at 600000 ms so a signal-ignoring child can't wedge a request
      forever — the reference clamps too, above the ~90 s ceiling we could observe.
    - **`escalate`** (default `true`) decides what happens if the process is still
      alive after the grace. `true` → **escalate** to `SIGKILL`, wait for the reap,
      and add `"escalated":true` to the reply. `false` → leave the process running
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
  connection, (re)subscribes it for future frames, then returns the result.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- **Exited processes are retained for 15 minutes, then dropped** — with their
  replay buffers — so an id last seen longer ago than that answers exactly like
  an unknown one, and `process.kill` on it still reports `{"success":true}`. A
  **running** process is never dropped, however old. The sweep runs on a 60-second
  timer *and* inline on every `process.spawn`, so an idle daemon prunes too.
  Probe-verified against the reference at `5db5e4a`. A client that reconnects
  after a long gap must therefore treat `found:false` as "finished and forgotten",
  not as "never existed".
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
- The replay buffer is **bounded at 16 MiB** of base64 `data` per process
  (probe-verified against the reference). Frames are dropped oldest-first, whole
  frames at a time, once a new frame would exceed the cap; at least one frame is
  always retained, even one larger than the cap. So `reattach{fromSeq:0}` replays
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
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] [-keep-children] [-listen-pipe]
```

Self-daemonizes (reparents to init / detached), extracts the login-shell PATH
(Unix), then runs the RPC server. On success it prints
`Claustrum remote server listening on <socket>` to stdout.

**Token source** — required, and checked *before* the socket:

- Missing both flags →
  `claustrum: daemonized child requires --token-file or --token-fd`, exit `1`.
- `CLAUDE_RPC_TOKEN` is **not** accepted for `-serve` (it is only for the
  `-bridge`/`-stop` clients) — the daemon never starts unauthenticated.
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
claustrum -stop -socket <p>          # auth read from CLAUDE_RPC_TOKEN
```

Sends `server.shutdown`.

- **Best-effort**: a missing or unreachable daemon is a silent no-op — exit
  `0`, no output. Only a live daemon's response (if any) is echoed to stdout.

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
claustrum's own `<id>`. The same file also carries `keep-children` and
`metrics-addr` defaults (precedence: explicit CLI flag > config > default). See
[IMPROVEMENTS.md](IMPROVEMENTS.md) CT-3 for the full contract, key list, and
hardening.

### -install — ensure the agent CLI

```text
claustrum -install -cli-dir <d> -cli-version <v> \
          [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>]
```

Download / verify / extract / prune, then print one `__INSTALL_RESULT__<json>`
facts line. `-install` itself always exits `0` — failures are reported inside
the facts (`cliError`), not via the exit code.

Checksum + error framing (probe-verified):

- `-cli-checksum` is verified on the download (`-cli-url`) path
  **unconditionally** — an empty `-cli-checksum` still fails
  (`checksum mismatch: expected=, actual=<sha>`).
- Input/decompress failures surface as `cliError` strings:
  `opening input: <err>` (zst read) and `decompressing: <err>` (bad zstd blob).

### Behavior shared by every mode

- **Default socket** — when `-socket` is omitted, `-serve`/`-bridge`/`-stop`
  fall back to `~/.claude/remote/rpc.sock`. The parent directory is **not**
  created, so `-serve` on a missing `~/.claude/remote` fails with
  `claustrum: listen unix: …: bind: no such file or directory`. (The deployment
  always passes `-socket`; this only matters for bare invocations.)
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
