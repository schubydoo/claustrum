# claustrum protocol reference

This page gives the rules that more than one method shares: the transport, the authentication, the message shapes, the error codes and the path handling. It lists the 20 methods with the page of each, and it links the pages for the error texts, the stream frames, the flags and the shared git rules.

An untagged rule on this page matches build `5fd08069` or an earlier build. A section that names its systems or its build keeps that scope.

claustrum uses newline-delimited JSON-RPC 2.0 over an `AF_UNIX`
`SOCK_STREAM` socket. [Methods (20)](#methods-20) lists the page of each method.
The three `server.*` methods share one page. This
document holds the rules that more than one method shares. The [error texts](protocol/errors.md),
the [stream frames](protocol/streams.md) and the [flags](protocol/flags.md) have their own pages. The shared
git rules have their own pages: see [Shared rules of the git methods](#shared-rules-of-the-git-methods). The life
of the daemon off the wire is on [Running the daemon](DAEMON.md). The pinned
reference build is `c8e2f083`.
A rule marked "claustrum's own" is not a parity claim: the reference has
no such rule, or it does something else. A D-number marks a deliberate
divergence, not the reference. A rule marked "not measured" or "from the code"
was read from claustrum's code, with no reference run. A mark "measured on
<system>" limits its sentence to that system. The divergences, their rules and
their reopen triggers are in [`DIVERGENCES.md`](DIVERGENCES.md).

## Transport

- One JSON object per line (NDJSON). No length prefix, no binary framing.
- One request line has a cap of 1 MiB. The daemon serves a line of up to 1048575
  bytes. A line of 1048576 bytes or more closes the connection with no reply.
  Send a large `process.stdin` payload in chunks below this cap.
- The `AF_UNIX` stream socket is created with mode `0600` (owner only).
- The connection is persistent. It stays open after a response, and id-less
  stream notifications arrive on it at any time.
- The daemon dispatches the requests of one connection concurrently. Responses
  can arrive out of request order. Match them by `id`.
- Two error replies skip the concurrent dispatch. The daemon writes them on its
  read path, before it reads the next line. The reference does the same
  (measured on Linux).
  - The parse error (`-32700`) covers any line that does not decode into a
    request. A partial object, a line of only spaces, `[1]`, `123`, `"x"` and a
    batch array all get it. The frame is
    `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`,
    76 bytes with its newline.
  - The auth error (`-32001`) covers a request with a missing or wrong `auth`.
    The lines `null`, `{}` and `{"jsonrpc":"2.0"}` get it too. The frame echoes
    the request `id`, re-encoded as in [Message shapes](#message-shapes). A
    `null` or missing `id` gives `"id":null`. An unauthenticated
    `server.shutdown` is not an auth error (see
    [Authentication](#authentication)).
  - The client can half-close its write side right after the line. These two
    frames still reach the wire before the close, with or without a final
    newline. A reply from the concurrent dispatch races the close.
  - A request whose handler blocks does not delay a later auth error on the
    same connection.
  - The daemon logs the parse error or the refusal ([texts](DAEMON.md#lines-of-connections))
    before it writes the error reply, as the reference does (measured on Linux). When the
    reply write fails, the failure line comes after the parse-error line
    (measured on Linux). In a Go type-mismatch error, the type name differs
    from the reference (measured on Linux).
  - A failed write of either reply closes the connection. Any other keeps it
    (build `c8e2f083`, measured on Linux).
- At a client EOF the daemon logs `[Server] Connection closed: <addr>`, then
  closes the socket (measured on Linux). Claustrum logs it only for a
  [marked](DAEMON.md#lines-of-connections) connection (claustrum's own, not measured).
- On Windows, when the client half-closes, the daemon does not wait for a request
  in flight. The reply to that request is lost, and a partial last line does not
  change that (measured on Windows).
- Input that ends without a final newline. The daemon reads the last bytes
  before EOF as one more line, and the rules above apply. A tail of only `\r`
  is a blank line and gets no reply.
  - If an earlier valid line came first, the reply to it races the close. The
    client gets the parse-error frame for the tail (measured on Windows), and it
    cannot count on the earlier reply.
  - On Windows this case has a timing difference, not parity. The reference
    sends the earlier reply in each measured run and closes later than claustrum.
    claustrum sends that reply in some runs only.
  - At the idle close, the daemon also reads the last bytes as a line. It
    dispatches a valid request. It writes the error for a bad tail on the read
    path, but the connection is already closed, so the wire carries no bytes.
- On Windows either end of an `AF_UNIX` connection can miss the close of the
  other end. A reader gets the bytes before the EOF and then blocks. A new read
  returns the EOF. A `-bridge` of claustrum or of the reference can hang in this
  way.

### Named-pipe transport (Windows, opt-in)

This is claustrum's own extension, off by default ([CT-5](DIVERGENCES.md#ct-5)).
With `-serve -listen-pipe`, or `listen-pipe = true` in `claustrum.conf`, the daemon
also serves the same JSON-RPC dispatch over a Windows named pipe, beside the
socket. The framing, the field order and the `"auth"` member are the same.

- **Windows-only.** Other platforms ignore the flag and log a warning.
- **Name and discovery.** The name is `\\.\pipe\claustrum-<random-instance-id>`,
  new at each start. The daemon writes it atomically to `rpc.pipe` in the socket's
  directory, before the pipe accepts and before the ready banner. A client reads
  the name from that file and does not build it.
- **Stale-file rule.** At start the daemon removes or replaces any `rpc.pipe`
  there, on every OS. At a graceful shutdown, if the file is still the one
  that it wrote (by identity, as for
  [`daemon.token`](DAEMON.md#token-persistence-daemontoken)), the daemon removes it. Any
  other file stays. So after a graceful shutdown or a new start no stale
  `rpc.pipe` stays, except a file whose identity the daemon could not record, and
  a successor keeps its own file. After a crash the file
  stays until the next start, and a dial of its name fails.
- **Owner-only and local.** The pipe has an owner-only DACL, the analogue of the
  socket's `0600`, and it refuses remote clients. See
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

## Authentication

Every request carries a top-level `"auth":"<token>"`. Every method but
`server.shutdown` rejects a request with a missing or wrong token with
`-32001 Unauthorized: invalid or missing auth token`. It also logs an
[Unauthorized line](DAEMON.md#lines-of-connections).

`server.shutdown` is not authenticated, as on the reference. A shutdown frame that
the daemon reads stops it, with any `auth` member or none. `-stop` sends no `auth`
member. Desktop stops the daemon with `server --stop --socket <sock>` from a bare
SSH command, with no `CLAUDE_RPC_TOKEN` in its environment
([driver claim](ARCHITECTURE.md#driver-claims-and-their-provenance)). The
exemption covers auth only ([Validation precedence](#validation-precedence)).

The server's expected token comes from `-token-file` or from `-token-fd`. With
`-token-file` the daemon reads the file once at startup and then unlinks it. With
`-token-fd` the daemon reads the token from an open descriptor and forwards it to
the detached child over a pipe. That handoff never touches disk.

No claustrum mode reads `CLAUDE_RPC_TOKEN`: not `-serve`, not `-bridge` and not
`-stop`. `-bridge` is a relay and adds no auth. The client that speaks through it
sends `"auth"` itself, from the [`daemon.token`](DAEMON.md#token-persistence-daemontoken) handshake or
from its launcher. claustrum unsets the variable before it daemonizes, and it strips the
variable from every spawned child. So a token never reaches a child through the
environment.

The files beside the socket and the rest of the daemon's life off the wire are on
[Running the daemon](DAEMON.md).

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

Results are ordered structs, never maps. The field order on each method page, and
under [Stream notifications](protocol/streams.md#stream-notifications), is the wire contract.

### Error codes

| code | meaning |
|---|---|
| `-32700` | parse error, a malformed JSON line (response `id` is `null`) |
| `-32600` | `Invalid JSON-RPC version`, meaning `jsonrpc` is absent or not `"2.0"` |
| `-32601` | `Invalid method format: <m>` (method has no `.`), `Unknown namespace: <ns>` (well-formed but unknown namespace), or `Unknown method: <ns>.<m>` (known namespace, unknown method) |
| `-32602` | invalid params (see per-method messages) |
| `-32603` | internal error, for example `open <path>: no such file or directory`. A recovered handler panic also gives this code, with `recovered panic: <v>` |
| `-32002` | a full stdin queue, from `process.stdin` (see [Errors](protocol/process-stdin.md#errors)) |
| `-32003` | an `offset` past the applied bytes, from `process.stdin` (see [Errors](protocol/process-stdin.md#errors)) |
| `-32004` | a refused `launcher`, from `process.spawn` (see [The managed launcher](protocol/process-spawn.md#the-managed-launcher)) |
| `-32005` | a launcher that does not start, from `process.spawn` (see [Errors](protocol/process-spawn.md#errors)) |
| `-32006` | `Refused: this server could not confirm that the caller is in its own namespaces`, on Linux with `CLAUDE_SSH_PEER_CHECK=1` (see [A refused caller](protocol/server.md#a-refused-caller-linux)) |
| `-32001` | `Unauthorized: invalid or missing auth token` |


### Handler panic recovery

The per-request goroutine recovers a panic in any handler, so the daemon does not
crash. The reply is `{"error":{"code":-32603,"message":"recovered panic: <v>"}}`,
and the daemon logs `[Server] recovered panic: method=<m> id=<id>: <v>`. A
recovered panic in `server.shutdown` writes no frame at all. Its only reply is
`{"ok":true}` (see [server.shutdown](protocol/server.md#servershutdown)).

The frame and the log line are claustrum's own, not a compatibility guarantee. No
input is known to reach a handler panic.

### Validation precedence

The daemon tests a request in the order parse, auth, version, method, params. So a
request with no `auth` and a missing or wrong `jsonrpc` gets `-32001 Unauthorized`,
not the version error. `server.shutdown` skips the auth test only. A shutdown frame
with a missing or wrong `jsonrpc` gets `-32600 Invalid JSON-RPC version`, and the
daemon stays up.

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
  The reference does the same on Linux, macOS and Windows.

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
are re-joined, and `git.info`'s `root` comes from the walk with links resolved, and on Windows from git's confirmation of it. On a
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
  macOS.

## Methods (20)

`server.capabilities` lists the methods in the order of this table. The
[reference build history](REFERENCE-BUILDS.md) records the builds that added or
removed a method.

| Method | Params → result | Page |
|---|---|---|
| `server.ping` | none → `{"pong":true}` | [server.*](protocol/server.md#serverping) |
| `server.capabilities` | none → `{"version","methods":[…20…],"instanceId","startedAt","features":[…],"peerCheck"[,"peerCheckBy"]}` | [server.*](protocol/server.md#servercapabilities) |
| `server.shutdown` | none → `{"ok":true}`, when the reply wins the race with the close | [server.*](protocol/server.md#servershutdown) |
| `files.list` | `{path}` → `{"entries":[{"name","path","isDir"},…]}` (name-sorted) | [files.list](protocol/files-list.md) |
| `files.validate` | `{path}` → `{"valid":bool,"isDir":bool[,"error"]}` | [files.validate](protocol/files-validate.md) |
| `files.stat` | `{path}` → `{"exists","isDir","size","mode":"-rw-r--r--"}` | [files.stat](protocol/files-stat.md) |
| `files.read` | `{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}` | [files.read](protocol/files-read.md) |
| `files.extract_tar` | `{archivePath,destDir}` → `{"success":true,"fileCount":<n>}` | [files.extract_tar](protocol/files-extract-tar.md) |
| `git.info` | `{path[,shadowDirOff]}` → repo: `{"isRepo":true,"repo":"<dir>","branch":"<b>","root":"<abs>","repoSlug":"<owner/repo>","defaultBranch":"<b>"[,"settingsControl":"<state>"]}` · non-repo: `{"isRepo":false,"repoSlug":"","defaultBranch":""[,"settingsControl":"<state>"]}` | [git.info](protocol/git-info.md) |
| `git.status` | `{path,baseRepo[,shadowDirOff]}` → clean: `{"isRepo":true,"clean":true}` · dirty: `{…,"clean":false,"changes":["M  a.txt"," M b.txt","?? new"]}` | [git.status](protocol/git-status.md) |
| `git.list_branches` | `{path[,shadowDirOff]}` → `{"isRepo":true,"branches":[…sorted…]}` | [git.list_branches](protocol/git-list-branches.md) |
| `git.worktree_create` | `{baseRepo,branchName,worktreePath[,sourceBranch][,existingBranch][,worktreeRoot][,timeoutMs][,shadowDirOff]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}` | [git.worktree_create](protocol/git-worktree-create.md) |
| `git.worktree_remove` | `{baseRepo,worktreePath[,branchName][,worktreeRoot][,shadowDirOff]}` → `{"success":true}`, or `{"success":true,"branchKept":true}` when the branch step keeps the branch | [git.worktree_remove](protocol/git-worktree-remove.md) |
| `launcher.resolve` | `{cliPath}` → one of four shapes | [launcher.resolve](protocol/launcher-resolve.md) |
| `process.spawn` | `{id,command[,args][,cwd][,env][,disableShellAgentSocket][,launcher][,wantPid]}` → `{"success":true}`, then stream frames | [process.spawn](protocol/process-spawn.md) |
| `process.stdin` | `{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}` | [process.stdin](protocol/process-stdin.md) |
| `process.kill` | `{id[,signal]}` → `{"success":true}` | [process.kill](protocol/process-kill.md) |
| `process.killAndWait` | `{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}` | [process.killAndWait](protocol/process-killandwait.md) |
| `process.reattach` | `{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}` | [process.reattach](protocol/process-reattach.md) |
| `plugins.prune` | `{[keep],[minAgeDays]}` → `{"root":"<per-install root>","pruned":[…],"prunedLegacy":[…],"staleArchives":0,"kept":0,"live":<n>,"young":<n>,"legacy":"<state>","minAgeDays":<clamped>}` | [plugins.prune](protocol/plugins-prune.md) |

## Shared rules of the git methods

The shared rules of the `git.*` methods are on four pages: [trust check](protocol/git-rules-trust.md), [config refusals](protocol/git-rules-config.md), [settings copy](protocol/git-rules-settings-copy.md) and [hardened calls](protocol/git-rules-calls.md).
