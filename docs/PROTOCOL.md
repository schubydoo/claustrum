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
  neither struct) is ignored by both. (Relatedly, on a pathological over-long
  `path` claustrum returns the empty `exists:false` result where the reference
  surfaces a `-32603` stat error.) These only surface under adversarial params —
  a real client never sends them; accepted divergence, found by differential
  fuzzing.
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
    - `repoSlug` is the `owner/repo` parsed from `remote.origin.url`. It accepts
      the scp-like (`git@host:owner/repo.git`), scheme (`https://`, `ssh://`),
      userinfo, and trailing-slash forms and strips a single trailing `.git`, but
      is populated **only when the path after the host is exactly two segments** —
      a GitLab subgroup URL (`host/group/sub/proj`) has three and yields `""`.
      Owner/repo characters are preserved verbatim (case, `-`, `_`, `.`).
    - `defaultBranch` is what `refs/remotes/origin/HEAD` points to (e.g. `main`);
      empty when origin/HEAD is unset.

#### git.status

`{path}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M a.txt","?? new"]}` (porcelain lines)

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
- When `sourceBranch` is omitted it defaults to the repo's current branch (and
  is echoed back). On an **unborn HEAD** (empty repo) the source resolves to
  empty, the add infers an orphan branch and still succeeds, and `sourceBranch`
  is omitted from the result.

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

- Best-effort, **fire-and-forget**; tears down the whole child tree — signals the
  process group on Unix, terminates the Job Object on Windows. Does not wait for
  the child to actually exit (contrast `process.killAndWait`).
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
- On a process that dies within the grace (cooperative, or a hard `signal:"KILL"`)
  → `{"found":true,"died":true}` with no `escalated`.

#### process.reattach

`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`

- Replays buffered frames with **seq > fromSeq** (exclusive) to this
  connection, (re)subscribes it for future frames, then returns the result.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- **`stdinApplied` (added `7c2f88d`).** The process's cumulative applied-stdin
  byte count (§`process.stdin`), always present after `lastSeq`. A reconnecting
  client resumes stdin from this offset so no bytes are re-applied or dropped.
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
- Each stdout/stderr frame carries at most one **32 KiB** read (the streaming
  read buffer); larger output is split across frames. A client reassembles by
  concatenating `data` in `seq` order.
- Exact frame *boundaries* depend on pipe scheduling and are not stable — only
  the reassembled bytes are. (Both the 32 KiB cap and the `-1` signal code are
  probe-verified against the reference.)
- The replay buffer retains frames for the life of the process, so
  `reattach{fromSeq:0}` replays everything.
- A process **survives** the disconnect of the connection that spawned it;
  another connection can pick it up via `reattach`. This is the multi-attach /
  reconnect mechanism.

## Daemon lifecycle (flags)

One binary, five modes. Everything below is probe-verified against the
reference unless marked **claustrum-only**.

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] [-keep-children]
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
- *(Implementation note: shutdown teardown now runs synchronously on the main
  goroutine so the kill-or-keep decision reliably completes before the process
  exits — it previously ran in a goroutine that could lose the race to the accept
  loop's return, skipping child teardown entirely.)*

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
