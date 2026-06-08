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
comes from `-token-file` (read once at startup, then **unlinked**) or the
`CLAUDE_RPC_TOKEN` environment variable. A bad or missing token →
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
| `-32001` | unauthorized |

**Validation precedence (probe-verified):** a request is checked in the order
**parse → auth → version → method → params**. Auth is validated *before* the
`jsonrpc` version, so a request that fails both (e.g. no `auth` and a missing/wrong
`jsonrpc`) reports `-32001 Unauthorized`, not the version error. Only once auth
passes is `jsonrpc == "2.0"` enforced.

**Params presence and typing:** every `files.*` / `git.*` / `process.*` method
requires a `params` object to be present. An absent `params` → `-32602 Invalid
params`, checked *after* method existence (so an unknown method is `-32601`
regardless). An empty `{}` is accepted and runs the method's own validation.
`params` that is present but **mistyped** — a wrong field type (e.g.
`"maxBytes":"4"` or `"path":123`) or a non-object value (`"params":"x"` / `[…]`) —
is also `-32602 Invalid params`; the daemon does not silently coerce or ignore the
decode error. (Unknown extra fields *are* ignored, by both daemons.) `server.*`
methods take no params, so a mistyped `params` on them is ignored and the call
succeeds.

## Methods (18)

`server.capabilities` self-describes the set. Order as returned:

```
server.ping  server.version  server.capabilities  server.shutdown
files.list   files.validate  files.stat  files.read  files.extract_tar
git.info     git.status      git.list_branches  git.worktree_create  git.worktree_remove
process.spawn  process.stdin  process.kill  process.reattach
```

### server.*

| method | params | result |
|---|---|---|
| `server.ping` | — | `{"pong":true}` |
| `server.version` | — | `{"version":"<id>","platform":"<goos>","arch":"<goarch>"}` |
| `server.capabilities` | — | `{"version":"<id>","methods":[…18…]}` |
| `server.shutdown` | — | *no response* — the daemon stops and the connection closes |

### files.* (param: `path`)

| method | result / notes |
|---|---|
| `files.stat{path}` | `{"exists","isDir","size","mode":"-rw-r--r--"}`; missing → `{exists:false,isDir:false,size:0,mode:""}` |
| `files.list{path}` | `{"entries":[{"name","path","isDir"},…]}` (name-sorted); `isDir` is resolved by **`Stat` (symlinks are FOLLOWED)** — a symlink to a directory is `isDir:true`, a dangling symlink is `isDir:false`; missing dir → `-32603 open …: no such file or directory` |
| `files.read{path[,maxBytes]}` | `{"content":"<raw text>","exists":true}`; missing file → `{content:"",exists:false}` (not an error); a directory → `-32602 files.read: path is a directory`; size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`. `content` is **raw text**, not base64. |
| `files.validate{path}` | `{"valid":bool,"isDir":bool[,"error"]}`; missing path → `{valid:false,isDir:false,error:"Path does not exist"}` |
| `files.extract_tar{archivePath,destDir}` | extracts a **gzip** tar → `{"success":true,"fileCount":<n>}`. **Side effects (not in the frame):** (1) **`destDir` is wiped** (`os.RemoveAll`) then recreated before unpacking — extraction is idempotent and destructive; (2) entries get **owner-only fixed modes** — files `0600`, dirs `0700` (an executable `0755` entry still lands `0600`); (3) on success an **empty `.synced` marker** is written at `destDir` root (not counted in `fileCount`); (4) **`archivePath` is consumed** — removed on *every* outcome once opened (success, bad gzip, or unsafe path). Errors: missing params → `-32602 archivePath and destDir are required`; non-absolute/root `destDir` → `{success:false,error:"destDir must be an absolute, non-root path: …"}` (rejected before the archive is opened, so it is **not** consumed); bad gzip → `{success:false,fileCount:0,error:"gzip: …"}`; an entry whose path escapes `destDir` ("zip slip") → `{success:false,fileCount:0,error:"unsafe path in archive: <entry>"}` (a `../` resolving back inside `destDir` is allowed); a non-regular/non-directory entry (symlink, hardlink, device, fifo) → `{success:false,fileCount:0,error:"unsupported tar entry type <c>: <entry>"}` where `<c>` is the tar typeflag char (symlink=`2`, hardlink=`1`); destDir clean/mkdir or marker-write failures → `clean destDir: …` / `mkdir destDir: …` / `write .synced: …` |

### git.* (param: `path` = repo dir; worktree ops use `baseRepo`)

| method | result / notes |
|---|---|
| `git.info{path}` | repo → `{"isRepo":true,"repo":"<dir>","branch":"<b>"}`; else `{"isRepo":false}`. `branch` is resolved via `symbolic-ref` so it works on an **unborn HEAD** (empty repo with no commits → the init branch name, e.g. `master`); a **detached HEAD** is reported as `branch:"detached:<short-sha>"`. |
| `git.status{path}` | clean → `{"isRepo":true,"clean":true}`; dirty → `{…,"clean":false,"changes":["M a.txt","?? new"]}` (porcelain lines); non-repo → `{"isRepo":false,"clean":false}` (full shape, unlike `git.info`'s bare `{"isRepo":false}`) |
| `git.list_branches{path}` | `{"isRepo":true,"branches":[…sorted…]}`; non-repo → `{"isRepo":false,"branches":[]}` |
| `git.worktree_create{baseRepo,branchName,worktreePath[,sourceBranch]}` | `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>"}`; missing `branchName` → `-32602 branchName is required`; resolved repo isn't a git repo → `{success:false,error:"not a git repository",errorCode:"not_a_repo"}` (checked before the add, so git's raw error isn't leaked); other failure → `{success:false,error:"git worktree add failed: …",errorCode:"worktree_add_failed"}`. The repo is **`baseRepo`** (not `path`); absent → the daemon's cwd repo. When `sourceBranch` is omitted it defaults to the repo's current branch (and is echoed back); on an **unborn HEAD** (empty repo) the source resolves to empty, the add infers an orphan branch and still succeeds, and `sourceBranch` is omitted from the result. |
| `git.worktree_remove{baseRepo,worktreePath}` | `{"success":true}` (lenient) |

### process.* (the agent/MCP-hosting core)

The client supplies its own `id` (any string). Output is delivered as id-less
stream notifications, **buffered** for later replay.

| method | params | result / notes |
|---|---|---|
| `process.spawn` | `{id,command[,args][,cwd][,env]}` | `{"success":true}`, then stream frames. `args`: string[]. `env`: `{KEY:VAL}` merged over the daemon environment. Missing `id` → `-32602 Process ID is required`; missing `command` → `-32602 Command is required`. |
| `process.stdin` | `{id,data}` | `{"success":true}`. `data` is **base64** written to the child's stdin. Checks run in a fixed order (probe-verified): **decode → exists → running**. Invalid base64 → `-32602 Invalid base64 data` (returned *before* the process is even looked up, so an unknown id with a bad payload still reports the decode error); unknown id → `-32602 Process not found`; known but **exited** process → `-32602 Process not running`. |
| `process.kill` | `{id[,signal]}` | `{"success":true}` (best-effort; signals the process group on Unix). |
| `process.reattach` | `{id,fromSeq}` | replays buffered frames with **seq > fromSeq** (exclusive) to this connection, (re)subscribes it for future frames, then returns `{"found","running","firstSeq","lastSeq"}`. Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0}`. |

### Stream notifications

```jsonc
{"type":"stream","processId":"<id>","stream":"stdout","seq":1,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"stderr","seq":2,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":0}
```

- `seq` is **per-process**, starts at 1, monotonic across stdout/stderr/exit.
- `data` is base64 for stdout/stderr; the `exit` frame carries `exitCode` and no
  `data`. A signal-terminated child reports `exitCode: -1` (not `128+signo`).
- Each stdout/stderr frame carries at most one **32 KiB** read (the streaming
  read buffer); larger output is split across frames, so a client reassembles by
  concatenating `data` in `seq` order. Exact frame *boundaries* depend on pipe
  scheduling and are not stable — only the reassembled bytes are. (Both the cap
  and the `-1` signal code are probe-verified against the reference.)
- The replay buffer retains all frames for the life of the process (so
  `reattach{fromSeq:0}` replays everything). A process **survives** the
  disconnect of the connection that spawned it; another connection can pick it up
  via `reattach`. This is the multi-attach / reconnect mechanism.

## Daemon lifecycle (flags)

| flag(s) | role |
|---|---|
| `-serve -socket <p> -token-file <p>` | self-daemonize (reparent to init / detached), extract login-shell PATH (Unix), run the RPC server |
| `-bridge -socket <p>` | dumb stdio↔socket relay (no auth injection) — what an SSH session attaches to |
| `-stop -socket <p>` | send `server.shutdown` (auth from `CLAUDE_RPC_TOKEN`) |
| `-version` | print `claustrum <id> (built <time>)` |
| `-install -cli-dir <d> -cli-version <v> [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>]` | ensure the CLI is present (download/verify/extract/prune), print `__INSTALL_RESULT__<json>` facts |

**CLI-mode behavior (probe-verified):**
- **Default socket.** When `-socket` is omitted, `-serve`/`-bridge`/`-stop` fall
  back to `~/.claude/remote/rpc.sock` (the daemon does **not** create the parent
  directory, so `-serve` on a missing `~/.claude/remote` fails `claustrum: listen
  unix: …: bind: no such file or directory`). The deployment always passes
  `-socket`; this only matters for bare invocations.
- **`-serve` requires `-token-file`**, checked *before* the socket
  (`claustrum: daemonized child requires --token-file`, exit `1`). The token comes
  **only** from the file (read once, then unlinked); the `CLAUDE_RPC_TOKEN` env is
  **not** accepted for `-serve` (it is only for the `-bridge`/`-stop` clients), so
  the daemon never starts unauthenticated. The token is read as a **line**: a
  single trailing newline (`\n` or `\r\n`) from the uploaded file is stripped,
  but spaces and other surrounding whitespace are preserved verbatim
  (probe-verified — a token file ending in a newline still authenticates). A bad
  `-token-file` →
  `claustrum: read --token-file: <err>`, exit `1`. On success it prints
  `Claustrum remote server listening on <socket>` to stdout.
- **`-stop` is best-effort.** A missing or unreachable daemon is a silent no-op —
  exit `0`, no output. Only a live daemon's response (if any) is echoed to stdout.
- **`-bridge` is strict.** A dial failure is a hard error: `claustrum: dial
  server: <err>` on stderr, exit `1`.
- **No mode given** → `claustrum: one of --version/--install/--serve/--bridge/--stop
  is required` on stderr, exit `2` (no usage dump). An *unknown flag* still gets
  the stdlib `flag` error + usage and exit `2`.

**`-install` checksum + error framing (probe-verified):** `-cli-checksum` is
verified **only on the download (`-cli-url`) path, and there unconditionally** — an
empty `-cli-checksum` still fails (`checksum mismatch: expected=, actual=<sha>`).
The local `-cli-zst` (SFTP-upload) path is **not** checksum-verified at all: the
blob arrives over an already-authenticated channel, so a wrong/empty checksum is
ignored and it installs. Input/decompress failures surface as `cliError` strings
`opening input: <err>` (zst read) and `decompressing: <err>` (bad zstd blob);
`-install` itself always exits `0` and prints the facts.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the `-install` facts schema and the
deployment lifecycle, and [EXAMPLES.md](EXAMPLES.md) for runnable snippets.
