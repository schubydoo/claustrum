# claustrum protocol reference

claustrum uses newline-delimited JSON-RPC 2.0 over an `AF_UNIX`
`SOCK_STREAM` socket. [Methods (20)](#methods-20) lists the page of each method.
The three `server.*` methods share one page. This
document holds the rules that more than one method shares, and the flags. The life
of the daemon off the wire is on [Running the daemon](DAEMON.md). The pinned
reference build is `5fd08069`. A rule with no mark matches the reference as
measured on `5fd08069` or on an earlier build.
Where a section names a build in its first lines, that build holds for the
section. A rule marked "claustrum's own" is not a parity claim: the reference has
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
  - The daemon logs `[Server] Parse error: <json error>` or
    `[Server] Unauthorized request: method=…, id=…` before it writes the error
    reply, and the reference logs the same text (measured on Linux). When the
    reply write fails, the failure line comes after the parse-error line
    (measured on Linux). In a Go type-mismatch error, the type name differs
    from the reference (measured on Linux).
- At a client EOF the daemon logs `[Server] Connection closed: <addr>`, then
  closes the socket (measured on Linux).
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
`-32001 Unauthorized: invalid or missing auth token`. It also logs
`[Server] Unauthorized request: method=…, id=…`.

`server.shutdown` is not authenticated, as on the reference. A shutdown frame that
the daemon reads stops it, with any `auth` member or none. `-stop` sends no `auth`
member. Desktop stops the daemon with `server --stop --socket <sock>` from a bare
SSH command line, with no `CLAUDE_RPC_TOKEN` in its environment. The exemption
covers auth only (see [Validation precedence](#validation-precedence)).

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
under [Stream notifications](#stream-notifications), is the wire contract.

### Error codes

| code | meaning |
|---|---|
| `-32700` | parse error, a malformed JSON line (response `id` is `null`) |
| `-32600` | `Invalid JSON-RPC version`, meaning `jsonrpc` is absent or not `"2.0"` |
| `-32601` | `Invalid method format: <m>` (method has no `.`), `Unknown namespace: <ns>` (well-formed but unknown namespace), or `Unknown method: <ns>.<m>` (known namespace, unknown method) |
| `-32602` | invalid params (see per-method messages) |
| `-32603` | internal error, for example `open <path>: no such file or directory`. A recovered handler panic also gives this code, with `recovered panic: <v>` |
| `-32002` | `stdin backpressure: queue full`, from `process.stdin` when the per-process async stdin queue is already full (16 MiB). The write is rejected, not blocked. |
| `-32003` | `stdin offset gap: offset ahead of applied bytes`, from `process.stdin` with an `offset` past the applied high-water |
| `-32004` | `the managed launcher cannot be used: <reason>`, from `process.spawn` when the checks refuse its `launcher` param, and for any `launcher` on Windows |
| `-32005` | `the managed launcher <path> could not be started: <reason>`, from `process.spawn` when the launcher passes the checks but does not start |
| `-32006` | `Refused: this server could not confirm that the caller is in its own namespaces`, on Linux with `CLAUDE_SSH_PEER_CHECK=1` (see [A refused caller](protocol/server.md#a-refused-caller-linux)) |
| `-32001` | `Unauthorized: invalid or missing auth token` |

### Error-string catalogue

The method-level error texts, verbatim. A text that only one method sends is on the
page of that method, and a row of the method links there. The method pages under
`docs/protocol/` give the trigger and the result shape. A text with no code and no
"in `error`" note is a JSON-RPC error `-32602`. "in `error`" is the `error` member of
the result. A row that says "in the frame of" a method, or "The same frames", is
`-32603` on `git.info`, `git.status` and `git.list_branches`. On `git.worktree_create`
and `git.worktree_remove` it is in `error`. A remove can wrap such a text in a longer
one, as the table in [Git-directory trust check](#git-directory-trust-check) shows. A
row with no mark holds a text of the reference daemon, measured on a Linux VM against a
build up to `5fd08069`. A "measured on macOS" or "measured on Windows" mark covers the
sentence that it ends, and that sentence was measured on that system only. A sentence
that names systems without "measured" holds on each one. The [reference build history](REFERENCE-BUILDS.md) has the builds.

| namespace / context | error string | code / notes |
|---|---|---|
| protocol | `Invalid JSON-RPC version` | -32600 |
| protocol | `Invalid method format: <m>` / `Unknown namespace: <ns>` / `Unknown method: <ns>.<m>` | -32601 |
| protocol | `Invalid params` | -32602 (absent/mistyped `params`) |
| protocol | `Unauthorized: invalid or missing auth token` | -32001 |
| protocol | `recovered panic: <v>` | -32603 (claustrum-only, see [Handler panic recovery](#handler-panic-recovery)) |
| files.stat | The texts that only this method sends | On the page: [files.stat → Errors](protocol/files-stat.md#errors) |
| files.read | The texts that only this method sends | On the page: [files.read → Errors](protocol/files-read.md#errors) |
| files.list | The texts that only this method sends | On the page: [files.list → Errors](protocol/files-list.md#errors) |
| files.validate | The texts that only this method sends | On the page: [files.validate → Response](protocol/files-validate.md#response) |
| files.extract_tar | The texts that only this method sends | On the page: [files.extract_tar → Response](protocol/files-extract-tar.md#response) |
| git.status | The texts that only this method sends | On the page: [git.status → Errors](protocol/git-status.md#errors) |
| git.status / git.list_branches | `<go error>`, for example `exit status 128` | -32603. The git call failed |
| git.list_branches / git.info / git.status / git.worktree_create | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | -32603 on the other methods. Create puts it after the text of the stopped step, as `git worktree add failed: ` or the `mkdir_failed` text of the parent step. A stopped repository test gets it alone, with `worktree_add_failed`. While a request with `"shadowDirOff":true` runs, the text says `after 30 minutes` (claustrum's own). See [Time limits of git calls](#time-limits-of-git-calls) |
| the four methods | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed`, after `git worktree add failed (checkout): ` for the checkout listing |
| the four methods | `this repository's git config cannot be used: git did not finish reading "<path>" within 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed` |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "<value>" is not a count` | in the frame of each method, when the daemon's own `GIT_CONFIG_COUNT` does not parse. See [The daemon's own git environment](#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair <n> is incomplete` | in the frame of each method, when a pair below the daemon's `GIT_CONFIG_COUNT` is not set. See [The daemon's own git environment](#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: <git text>` | in the frame of each method, for a `GIT_CONFIG_KEY_<n>` of the daemon that git cannot read. Linux, macOS and Windows. See [The daemon's own git environment](#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` | Windows only. In the frame of `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`. The `path` of the first two, or the `baseRepo` of the last two, exists and git cannot start in it, for example a regular file. See [Git-directory trust check](#git-directory-trust-check) |
| git.* | `git cannot run on this host; git not run: <exec error>: <git text>` | in the frame of each method, when the configuration listing fails and `git version` fails too. The exec error and the text are those of `git version`. Linux, macOS and Windows. It also follows a failed read of the test repository's config in [the start test](#the-start-test), and a failed [call about the repository](#the-call-about-the-repository), when `git version` fails too. For `git.worktree_remove` see the table in [Git-directory trust check](#git-directory-trust-check) |
| git.* | `this repository's git config cannot be used: <detail>` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. The details are in [Repository config refusals](#repository-config-refusals) |
| git.* | `a file in this repository's .git directory cannot be used: <detail>` | The same frames. See [Repository config refusals](#repository-config-refusals) |
| git.* | `Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. See [The settings copy](#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>` | The same frames. The daemon cannot make the folder of the copy |
| git.* | `Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again` | The same frames. See [The settings copy](#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again` | The same frames. Measured on `git.status` only. From the code: a checkout puts it after `git worktree add failed (checkout): `. See [The settings copy](#the-settings-copy) |
| git.* | `git could not check this repository on the remote machine: at "<root>": exit status <n>` | The same frames, for a failed call about the repository. See [The call about the repository](#the-call-about-the-repository) |
| git.* | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` | The same frames, for a call about the repository that the daemon ended |
| git.* | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again` | The same frames, for a call about the repository that names another path |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` | in the frame of each method, for a configuration key over 1048576 bytes. Measured on `git.info`. See [Hardened git calls](#hardened-git-calls) |
| git.* | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` / `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` | in the frame of each method, for more than 1024 hook names, or names over 65536 bytes in sum. Measured on `git.info`. See [Hardened git calls](#hardened-git-calls) |
| git.* | `the repository's git directory could not be trusted; git not run: commondir not as git writes it: <S1, S2 or S3>` | in the frame of each method, for a stray `commondir`. [Git-directory trust check](#git-directory-trust-check) gives the three texts |
| git.worktree_create / git.worktree_remove | `baseRepo is inside a managed worktrees directory (beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees marker), or could not be validated as a trust root` | in `error`. Create adds `errorCode:"nested_base_repo"`, and remove has none. It answers a `baseRepo` under a managed worktrees tree, or one that fails claustrum's own trust-root test. No git runs. See [git.worktree_create](protocol/git-worktree-create.md) and [git.worktree_remove](protocol/git-worktree-remove.md#where-the-paths-can-be) |
| git.worktree_create | The texts that only this method sends | On the page: [git.worktree_create → Error texts](protocol/git-worktree-create.md#error-texts) |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <p> is not <worktree location>/<directory>/<name> beneath <root>` | in `error`, as are the rows below it up to `passes through too many symbolic links`. `<p>` and `<root>` as sent. Create adds `errorCode:"unsafe_path"` in each of these rows that names no other code |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <p> uses the reserved name <name> for a directory under the worktree location` | `<name>` is `.git` or `.claude-managed-worktrees`, in any letter case |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> has a component named <name>, which is reserved; …` | A `.claude-managed-worktrees` component is not measured |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> would place worktrees in <dir>, which contains the repository <repo>; choose a location the repository is not inside` | |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> would place worktrees in <dir>, which is (or is inside) <wt>, a worktree of the repository <repo>; …` | |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> resolves to the filesystem root; …` | |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> passes through too many symbolic links (a loop?)` | Create: `errorCode:"unsafe_path"`. Linux and macOS, with `worktreeRoot`. A symlink loop or too many links lie in the path of the root. claustrum uses the kernel limit (not measured). `<root>` is `worktreeRoot` without a trailing slash. claustrum also drops a `//` here (not measured). Nothing is created or removed |
| git.worktree_remove | The texts that only this method sends | On the page: [git.worktree_remove → Error texts](protocol/git-worktree-remove.md#error-texts) |
| process.spawn | The texts that only this method sends | On the page: [process.spawn → Response](protocol/process-spawn.md#response) |
| launcher.resolve | The texts that only this method sends | On the page: [launcher.resolve → Request](protocol/launcher-resolve.md#request) |
| process.stdin | The texts that only this method sends | On the page: [process.stdin → Errors](protocol/process-stdin.md#errors) |
| process.spawn / process.killAndWait / process.reattach | `Process ID is required` | `id` is absent or empty |
| plugins.prune | The texts that only this method sends | On the page: [plugins.prune → Errors](protocol/plugins-prune.md#errors) |

`-install` reports failures inside the `__INSTALL_RESULT__` facts line as
`cliError` strings, not through an exit code. They are catalogued in
[-install](INSTALL.md#-install-ensure-the-agent-cli).

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
  macOS VMs.

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

### Stream notifications

A `process.*` client supplies its own `id`, which is any string. The daemon
delivers output as id-less stream notifications, and it buffers them for a
later replay.

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
- `signal` and `killedBy` appear on the `exit` frame only, both after `exitCode`,
  and both are omitempty, so a normal exit stays byte-identical. `signal` is the
  SIG-prefixed name of the terminating signal from the wait status, such as
  `SIGTERM` or `SIGKILL`. It is omitted on a normal exit. It is omitted always on
  Windows (measured on Windows). `killedBy` names who asked the daemon to kill the
  process: `client` for `process.kill` and `process.killAndWait`, and `shutdown`
  for the shutdown and `killAll` sweep. It is emitted on every OS. Signal names
  other than `SIGTERM` and `SIGKILL` are not measured. The `shutdown` value is not
  measured, because that exit frame races the close of the connection.
- The `exit` frame waits at most 5 seconds after the process exits for
  stdout/stderr to reach EOF. The daemon then closes the read ends and emits the
  frame anyway. This matters when a grandchild holds the same pipe, as
  `npm run dev &` does. Output that the grandchild writes after the cap is lost,
  because that write fails `EPIPE`.
- Inside that drain window `process.reattach` reports `running: false`. claustrum
  flips it at the reap. The reference is measured `false` one second into the
  drain.
- Inside the same window `process.stdin` answers `-32602 Process not running` for
  a write that carries fresh bytes, so `applied` and `stdinApplied` do not count
  bytes that the closed pipe discarded. An offset gap still answers `-32003`, and
  a wholly duplicate write still answers `{"success":true,…,"duplicate":true}`.
  The acknowledgement caveat of `stdinApplied` (see
  [process.reattach](protocol/process-reattach.md)) still applies to a write
  accepted while the process was live.
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

## Shared rules of the git methods

This section holds the rules that more than one `git.*` method uses.

### Git-directory trust check

Every `git.*` method checks the git directory of the folder that it runs git in,
before git runs there. That folder is `path` for `git.info` and
`git.list_branches`. It is `baseRepo` for `git.status`, `git.worktree_create` and
`git.worktree_remove`. The check has three outcomes. "Trusted" lets git run. "No
repository" answers as if the folder held no repository. "Refused" answers with
one of the texts M2 to M5 and S1 to S3, and git does not run.

#### How the daemon finds the git directory

When the daemon's own environment sets neither `GIT_DIR` nor `GIT_COMMON_DIR`,
the daemon walks up from the request folder:

- The walk starts at the request folder with symlinks resolved. A refusal names
  the resolved path, never the alias.
- On Windows the walk resolves no junction before the last component of the
  path. It starts at the path as spelled, and it takes a `..` by text. Take a
  repository `<P>` with a junction `<P>\J` into a repository `<R>`. For
  `<P>\J\sub` each git call carries `GIT_COMMON_DIR=<P>\.git`. Through a second
  junction `<KP>` to `<P>`, the pin is `<KP>\.git`. A junction as the last
  component gives the pin of `<P>` too. A folder symlink in the place of the
  junction resolves, and the pin is `<R>\.git`. A junction outside any repository
  gives "no repository", and each git call carries `GIT_DIR=NUL`.
- On Windows the resolve of a folder path can fail, for example at a missing name
  before `..`. The walk then starts at the spelled path too (from
  the code). Only `git.info` reaches that start (from the code).
  `git.list_branches` and `git.status` resolve the path first, and so do
  `git.worktree_create` and `git.worktree_remove` for a `baseRepo` that exists
  (from the code). If that resolve fails, they answer there (from the code).
- On Windows a refusal of a config on `git.info` follows the spelled start too. In
  the state `shadow` without `"shadowDirOff":true`, it is the class 1 text of
  [Repository config refusals](#repository-config-refusals) (from the code). A
  config that git refuses and the daemon's read accepts, such as an `include.path`
  with no value, gets the hooks refusal (from the code). So does any refused
  config outside that state (from the code).
- `process.spawn` does not use the walk.
- A `.git` folder that passes the git-directory test is the git directory.
- A `.git` folder that fails the test ends the walk. If it holds a `commondir`
  entry of any type, it is the git directory, and the rules of a stray
  `commondir` judge it. Otherwise the answer is "no repository". Git itself walks
  on to an outer repository here, so this answer differs from git's own.
- A `.git` regular file starts with the exact bytes `gitdir:` and holds at most
  1 MiB. The rest is trimmed of white space. A relative target is taken relative
  to the folder that holds `.git`. Any other `.git` file means "no repository".
  `GITDIR:` in upper case is one example.
- A `.git` of another type, such as a FIFO, is skipped, and the walk goes on.
- With no `.git`, the folder itself is the git directory if it passes the test.
  That is a bare repository. Nothing found up to the root means "no repository".
- A request folder that does not exist is left to git.

The git-directory test has three parts. `objects` and `refs` exist, of any type.
`HEAD` passes. A regular `HEAD` passes when its first 255 bytes start with `ref:`,
then any spaces, tabs, CRs or LFs, then `refs/`. It also passes when it starts
with 40 hex digits in either case. A symlink `HEAD` passes when its target text
starts with `refs/`. Any other `HEAD` fails at once, a FIFO for example.

#### How the daemon judges the git directory

The daemon then judges the git directory G:

- G is a linked-worktree entry when its parent is named `worktrees`, its own name
  is not `.git`, and its grandparent passes the git-directory test. The entries of
  a main repository, a bare repository and a submodule all count.
- G is not an entry and holds no `commondir`: trusted.
- G is not an entry and holds a `commondir`: the rules of a stray `commondir`
  judge it. So the honest `commondir` (`../..`) of an entry whose grandparent fails
  the test gets S1 (not measured).
- G is an entry without `commondir`: M3.
- G is an entry whose `commondir` is not a regular file of at most 1048576 bytes:
  M4. A symlink is followed only when it is relative and stays inside the entry.
- Otherwise the content is trimmed of spaces, tabs, CRs and LFs. It is taken
  relative to G when it is not absolute, and cleaned lexically. It has to equal
  the grandparent as a string. No symlink in it is resolved, so naming the
  repository through an alias fails. On Linux and macOS a backslash is an
  ordinary character. On Windows the comparison ignores letter case and slash
  direction. A `\\?\` prefix or a path without a drive letter still fails there.
  Any other value gets M2. That covers another repository, a missing path, an
  empty file and a NUL byte.
- A trusted git directory runs git with `GIT_COMMON_DIR` pinned to the repository
  git directory. For an entry that is the grandparent. For any other git
  directory it is that directory itself. White space around `../..` therefore
  passes the check, although git itself rejects that file.
- The pin also keeps git from walking past a trusted nested `.git`. A nested
  `.git` whose `objects` is a regular file therefore answers "not a repository"
  from git on Linux and macOS. Git for Windows answers for the nested repository.
- An entry folder that is gone means "no repository". On Linux and macOS an entry
  replaced by a regular file means the same. On Windows it gets M3.
- A `.git` file that names a plain folder, another repository's git directory or
  the main git directory is not refused. Git answers for itself there.
- On Windows the daemon does not expand 8.3 short names in G. A `.git` file or a
  daemon `GIT_DIR` can name an entry as `…\GIT~1\WORKTR~1\<name>`. That G is not
  an entry, so its `commondir` gets S1 for the content `../..` (the S1 text there
  is not measured).
- On Windows the daemon resolves G when a component of G is a symlink. Else it
  keeps G as spelled, also with a junction before its last component. For a
  junction `<KP>` to a repository, the pin is `<KP>\.git`.

#### A stray `commondir`

A stray `commondir` is one in a git directory G that is not an entry: a main
`.git`, a bare repository or a submodule's git directory. The daemon judges it in
these steps:

1. A `commondir` symlink that leaves G is not followed. A dangling symlink whose
   target is relative and stays in G counts as none when G passes the
   git-directory test. G is then trusted, and git itself fails to read the file.
   The method answers what git's failure gives: `git.info` with no `branch`,
   `git.list_branches` `-32603 "exit status 128"`, and the failed add on
   `git.worktree_create`. With `worktreeRoot`, `git.worktree_remove` answers
   `cannot list the repository's worktrees: exit status 128` and deletes nothing
   (measured on Linux). Without `worktreeRoot` it answers
   `{"success":true,"branchKept":true}`. It deletes the worktree folder and its
   entry, and the branch stays (measured on Linux). If the folder is gone before
   the request, it deletes the entry (measured on Linux). When G fails the
   git-directory test, that symlink gets S3 (measured on Linux).
2. A `commondir` that is not a regular file of at most 1048576 bytes gets S2 with
   its reason. A relative symlink that stays in G is followed. A dangling symlink
   that is absolute, or that leaves G, gets S2 with `openat commondir: path
   escapes from parent` (measured on Linux).
3. The content with every trailing CR and LF removed is exactly `.` or `./`. A
   space or a tab is not trimmed. Any other content gets S1, which quotes it. So
   ` .`, `. `, `.<TAB>`, `./x`, `..`, an empty file, `.\` and the absolute path of
   G itself are all refused.
4. G then passes the git-directory test, or it gets S3. When both S2 and S3 apply,
   which one answers is not measured.
5. Else G is trusted, and git runs with `GIT_COMMON_DIR` pinned to G.

#### The daemon's `GIT_DIR` and `GIT_COMMON_DIR`

The daemon's own environment changes the check as follows:

- A `GIT_DIR` names the git directory to judge, for every request folder, even a
  plain one. A relative value is taken relative to the request folder. A
  `GIT_DIR` that names a gone entry gets M5. Any other `GIT_DIR` that does not
  exist is left to git.
- On Linux and macOS a `GIT_DIR` that names a `.git` file means "no repository".
  On Windows that `.git` file is trusted and pinned to itself. Without
  `"shadowDirOff":true` class 2 of
  [Repository config refusals](#class-2-files-of-the-git-directory) then answers.
- Git then runs with `GIT_COMMON_DIR` pinned to that `GIT_DIR`, unless the
  daemon's environment sets `GIT_COMMON_DIR` (measured on Linux and Windows). A
  relative value is first joined to the request folder (measured on Linux and
  Windows). On Windows `/dev/null` names nothing, so it becomes
  `<request dir>\dev\null`.
- A `GIT_COMMON_DIR` turns the check off for `git.info` and `git.list_branches`.
  The other three methods still run it. `git.worktree_create` then prefixes the
  refusal with `git worktree add failed: cannot locate the repository's git
  directory: `.
- `GIT_CONFIG`, `GIT_CONFIG_PARAMETERS` and `GIT_CEILING_DIRECTORIES` do not
  change the check.

#### Trust refusal texts and frames

M2 to M5 are texts of `f6010b97`, and S1 to S3 are texts of `89cb6289`.
M2 to M4 and S1 to S3 start with `the repository's git directory could not be
trusted; git not run: commondir not as git writes it: `. Each `%q` of a path is Go
quoting of the path cut to its first 300 runes. The content in S1 is cut to its
first 60 runes instead. A non-ASCII path is cut by runes, not bytes. An invalid
UTF-8 byte stays a byte and prints as `\xff`. The rest of the text is never cut.

| id | text after the prefix | operands |
|---|---|---|
| M2 | `%q does not name the entry's own repository, %q; restore the file to read ../.. or remove that worktree entry and add the worktree again` | `<entry>/commondir`, `<repo git dir>` |
| M3 | `worktree entry %q has none (git always writes one there, containing ../..); the entry is damaged or was not made by git — recreate the worktree with git worktree add, or remove the entry` | `<entry>` |
| M4 | `%q is not a small plain file (<reason>); remove it, or remove that worktree entry and add the worktree again so git rewrites it` | `<entry>/commondir` |
| S1 | `%q reads %q; git writes that file only inside .git/worktrees/<name>/, and Claude Code's sandbox may leave one containing just "."; anything else sends git's configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did` | `<G>/commondir`, the content |
| S2 | `%q is not a small plain file (<reason>); git writes that file only inside .git/worktrees/<name>/; one here can send git's configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did` | `<G>/commondir` |
| S3 | `%q sits in a git directory git would not take as a repository (its HEAD, objects or refs missing or invalid), so git would look for one somewhere else; repair that directory (usually its HEAD file)` | `<G>/commondir` |

`<name>` in S1 and S2 is literal text, and the JSON encoder writes it as
`\u003cname\u003e`.

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
| `git.info` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"repoSlug":"","defaultBranch":""[,"settingsControl":"<state>"]}` |
| `git.list_branches` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"branches":[]}` |
| `git.status` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"clean":false}` |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}` | `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}` |
| `git.worktree_remove` | without `worktreeRoot`, the lock-check refusal for a worktree directory that exists, and `could not check whether <p> is locked (<text>); retry` for a gone one. A failed listing in a folder that exists gives `could not check whether <p> is locked: <text>`. With it, the work-tree refusal and `<text>` | without `worktreeRoot`, the lock-check refusal for a worktree directory that exists, and `{"success":true}` for a gone one. With it, the work-tree refusal and `exit status 128` |

The lock-check refusal is
`{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`.
It has no `errorCode`, and the path is neither quoted nor cut. The work-tree
refusal is `{"success":false,"error":"failed to remove worktree: cannot determine the
repository's work tree: <reason>"}`, also with no `errorCode`. With `worktreeRoot`,
the listing classes below give their own reason. With `worktreeRoot` and no `git`
on `PATH`, the exec error replaces both texts of the table (class 1 below).

#### A folder with no repository

Where the trust check finds no repository, the daemon still runs the configuration
listing in that folder. In a plain folder it carries `GIT_DIR=<null>` (see
[The daemon's own git environment](#the-daemons-own-git-environment)). The frames
are then those of the "no repository" column. A listing that fails by class 3
below gives the "refused" column. For `git.worktree_remove` with `worktreeRoot`
this is measured on Linux and macOS, because Windows refuses every `worktreeRoot`
first.

A daemon `GIT_DIR` that names a regular file differs by system. On Linux and macOS
no listing runs, and the "no repository" column answers, also with an empty
`GIT_CONFIG_KEY_0` (measured for `git.info` on Linux and macOS). The other methods
are from the code. On Windows a file with no `gitdir:` line gets the refusal of a
config on the five methods: `this repository's git config cannot be used: open
<GIT_DIR>: not a directory` (measured on Windows). With a git older than 2.56, a
`.git` file to a gone git dir keeps the "no repository" frame of `git.info`
(measured on Linux and macOS).

Two states are from the code: a daemon `GIT_COMMON_DIR`, whose listing gets no
`GIT_DIR=<null>`, and on Linux and macOS a `.git` file that names a regular file.
In both, `git.worktree_create` runs no listing where the trust check finds no
repository, and it keeps the "no repository" column (from the code).
`git.worktree_remove` keeps that column unless its listing fails (from the code).
For the `.git` file, `git.info` and `git.list_branches` run no listing either
(from the code). The listing of `git.status` still carries `GIT_DIR=<null>`, and
its failure is the refusal of `git.status` (from the code).

#### A failed configuration listing

When the configuration listing fails, the daemon sorts the failure into a class.
The classes come in this order:

1. No `git` on `PATH`: "no repository", and no process starts.
   `git.list_branches`, `git.status` and `git.worktree_create` answer their "no
   repository" shapes, the create with and without `worktreeRoot` (measured on
   Linux). On a repository the start test of
   [the settings copy](#the-start-test) comes first for these three methods,
   unless the request has `"shadowDirOff":true` (from the code).
   `git.worktree_remove` without `worktreeRoot` answers
   `{"success":true,"branchKept":true}` for a gone worktree with a `branchName`,
   and the lock-check refusal for a worktree directory that exists (measured on
   Linux). With `worktreeRoot` and a `baseRepo` that exists, it answers the
   work-tree refusal with the exec error alone, before the trust check: `exec:
   "git": executable file not found in $PATH` (measured on Linux). A `baseRepo`
   that does not exist gets no work-tree refusal (see
   [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks)).
   A refused daemon `GIT_CONFIG_COUNT` answers before the exec error when `stat`
   reads `baseRepo` (not measured).
2. The listing exits 128, and its stderr starts with `fatal: not a git
   repository`. Leading white space is dropped first, and letter case does not
   count. The answer is "no repository". Exit 1 with that text is not this class.
   With a `branchName`, `git.worktree_remove` without `worktreeRoot` keeps the
   branch, and a gone worktree answers `{"success":true,"branchKept":true}`. For a
   `.git` file to a gone git dir, git 2.56 says `gitfile does not point to a valid
   repository` instead (measured on Linux and Windows). A gone entry under
   `.git/worktrees` then answers "no repository" on each method but
   `git.worktree_remove` (measured on Linux and Windows). Another gone git dir
   answers the error of
   [the call about the repository](#the-call-about-the-repository) in the folder
   that holds `.git` (measured on Linux and Windows). With `"shadowDirOff":true`
   that git dir gets the hooks refusal on every method (measured on Linux).
3. Any other failure, a failed start included. The daemon runs `git version`. It
   runs in `/` on Linux and macOS. On Windows it runs in the daemon's working
   folder (from the code). If that call fails too, the text is `git cannot run on this
   host; git not run: <exec error>: <git text>`. Else it is the hooks refusal.

The `git version` call of class 3 has the profile of the failed listing (see
[Hardened git calls](#hardened-git-calls)). It gets no `-c` option and no `GIT_CONFIG_COUNT`,
`GIT_CONFIG_KEY_<n>`, `GIT_CONFIG_VALUE_<n>` or `GIT_CONFIG_GLOBAL` of the daemon.
claustrum matches these names by their exact upper-case letters, so on Windows a
lower-case name stays on the call (from the code). claustrum passes the daemon's
`GIT_CONFIG_SYSTEM` and `GIT_CONFIG_NOSYSTEM` on (not measured). It does not give
the call the daemon's own `LC_ALL` and `LANGUAGE` back (claustrum's own, not
measured).

#### Where git cannot start

On Linux and macOS git cannot start in a folder that is a file, or that the
daemon cannot search (mode 0600 or 0000). The listing fails at the chdir, the
daemon runs `git version`, and the method goes on:

- If `git version` works, a regular file gets the "no repository" frames of
  `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`, also
  with an empty `GIT_CONFIG_KEY_0`. A folder of mode 0000 gets the same frames for
  the last three methods, as a plain folder and as a repository (measured on
  Linux).
- If `git version` fails too, the method answers the "cannot run" text. That holds
  for `git.info` on a regular file and on a folder of mode 0000, and for the other
  three on a regular file (the create in `error`, with and without `worktreeRoot`).
  It holds for those three on a folder of mode 0000 too, as a plain folder and as a
  repository (measured on Linux).
- A path that does not exist keeps its frames.
- On a folder of mode 0000 `git.worktree_remove` without `worktreeRoot` answers
  `failed to remove worktree: open <baseRepo>: permission denied` (measured on
  Linux).

On Windows a `git version` that fails gives the "cannot run" text too, for
`git.info`, `git.list_branches`, `git.status` and `git.worktree_create` on a plain
folder, and for `git.info` on a repository and on a regular file. `git.info` on a
path that does not exist keeps its frame.

On Windows git cannot start in some paths that exist, and the listing fails by
class 3 with the start error `fork/exec <git.exe path>: The directory name is
invalid.`. `<git.exe path>` is the git that `PATH` resolves. The hooks refusal
with that error answers these requests in the "refused" frame of
[Trust refusal texts and frames](#trust-refusal-texts-and-frames),
and nothing changes on disk:

- `git.info` with `path` a regular file in a plain folder or in a repository, a
  file symlink, `NUL`, the name of a regular file with one more dot at its end, or
  a folder with a full path of 296 characters
- `git.list_branches` with `path` a regular file
- `git.status` with `baseRepo` a regular file, and `path` that file or a
  repository
- `git.worktree_create` with `baseRepo` a regular file

A `git version` that exits 1 and writes `boom` turns the `git.info` answer on a
regular file into `{"error":{"code":-32603,"message":"git cannot run on this host;
git not run: exit status 1: boom"}}`.

Other Windows frames stay as they are:

- `git.status` with the file as `path` and a repository as `baseRepo` answers
  `{"isRepo":false,"clean":false}`.
- `git.worktree_remove` with a regular-file `baseRepo` answers `failed to remove
  worktree: open <baseRepo>: not a directory`, also with an empty
  `GIT_CONFIG_KEY_0`.
- `git.info` answers "no repository" for a path that does not exist, a path under
  a regular file and a junction whose target is gone.
- With `GIT_CONFIG_COUNT=x` the count refusal answers the regular file.

On Windows, if the listing does not start in a path that `stat` reads as not a
folder, claustrum refuses (from the code).

A Windows folder can have an access entry that denies read data and traverse to
the user of the daemon. It keeps the `git.info` frame of a plain folder. With a
daemon token that has no privilege, the daemon refuses there with the hooks
refusal and `exit status 128: fatal: unable to access 'NUL/config': Permission
denied`. A folder with no deny entry keeps its frame under that token.

#### The "cannot run" and hooks refusal texts

The "cannot run" text is `git cannot run on this host; git not run: <exec error>:
<git text>`. It carries the exec error and the stderr of `git version`, not those
of the listing. `git.worktree_remove` with `worktreeRoot` gives it too (measured
on Linux and macOS).

The hooks refusal is `config-defined hooks could not be pinned off; git not run:
listing the configuration in force: <exec error>: <git text>`.

claustrum makes `<git text>` of both texts with the text rule of
`git.worktree_create`. The rule takes git's raw stderr and keeps its first 512
bytes. It drops invalid UTF-8, turns each non-printable rune into one space, and
then trims spaces. If nothing is left, the text is the error of the git process,
for example `exit status 128`. The cap comes before the trim, so leading white
space counts toward the 512 bytes. A tab becomes one space. For the hooks refusal
this rule is measured on Windows. For the "cannot run" text it is claustrum's own.

With the `GIT_COMMON_DIR` pin, git names a corrupt config by its absolute path
(measured on Linux and macOS). On claustrum a syntax error in a config file of the
repository gets the class 1 text first: see
[Repository config refusals](#repository-config-refusals).

### A FIFO where the daemon reads a git file

On Linux and macOS the daemon opens three files of its own reads without
blocking: a `.git` file, the `commondir` file of a worktree entry and the `gitdir`
record of a worktree entry. It reads a regular file only. Anything else there, a
FIFO for example, is no usable file, and the read does not wait. Windows has no
FIFO in the file system. Each rule below is measured on Linux and macOS, with a
git that walks past a FIFO `.git`:

- A `.git` that is a FIFO, a socket (measured on Linux only) or a symlink to a FIFO
  or to `/dev/null` is skipped by the walk. For a FIFO, `git.info`,
  `git.list_branches`, a create and a remove then answer for the repository above
  it. That remove deletes the folder, the entry and the branch. For a socket and a
  symlink to `/dev/null`, `git.info` and a create do. For a symlink to a FIFO, a
  create does. A create there succeeds. `git.status` with that folder as `path`
  and the repository above as `baseRepo` answers
  `{"isRepo":false,"clean":false}`. With no repository above, the answer is "no
  repository", and a create makes nothing.
- A `commondir` FIFO in the entry of a linked `baseRepo` gives M4 with
  `commondir is not a regular file`.
- A `commondir` FIFO in the entry of the worktree that a remove names: without
  `worktreeRoot` the remove succeeds and deletes the folder, the entry and the
  branch. A locked worktree there gets the locked refusal, and nothing is deleted.
  With `worktreeRoot` the remove gives no answer in 40 s, and nothing is deleted. It
  waits in `git worktree list --porcelain -z`.
- A remove of a worktree whose entry has a `commondir` regular file of 2 MiB
  succeeds and deletes the folder, the entry and the branch (measured on Linux and
  macOS).
- A `gitdir` record FIFO of the entry of the worktree: the remove without
  `worktreeRoot` succeeds, the folder and the branch go, and the entry stays. A
  locked worktree there gets the same success, and the entry and its `locked` file
  stay.
- A `gitdir` record FIFO of another entry: a remove of a worktree succeeds and
  deletes its folder, its own entry and its branch, and that other entry stays. A
  create in that repository waits in git's own `git worktree add`. It also waits
  with `timeoutMs` 3000 (measured on Linux only).

The [time limit](#time-limits-of-git-calls) of the add ends that create (from the
code). A device file or a socket as a `commondir` file or a `gitdir` record is no
usable file (from the code). claustrum reads nothing from a FIFO that a writer
opens during the request (from the code). After `git worktree add`, a `commondir`
of the git directory of `baseRepo` that is not a regular file counts as none, so
that git directory is its own common directory (from the code). A FIFO as the
config is in
[Repository config refusals](#repository-config-refusals).

### Repository config refusals

The daemon reads the git config of a repository itself, and it tests some files of
the git directory. If it does not accept one of them, the git method answers with a
refusal. A rule with no mark follows `5fd08069` as measured on Linux. A mark `(also <system>)`
means the rule was measured there too. A rule that names its systems
was measured there. Most rules were measured on
`git.info` and `git.status`.

The read runs only in the state `shadow` of [the settings copy](#the-settings-copy).
In the state `fallback-git` the daemon makes no read and gives no refusal. Git 2.13.7 is the
oldest git that ran with the refusal. The daemon
follows each `include.path` setting itself, and for a linked worktree it reads the
config of the repository of that worktree. A refusal is not kept (also macOS and
Windows), and an accepted config is read once and again after one of its files
changes (the change test and the bound of 256 git directories are claustrum's own,
see [claustrum's own choices](#claustrums-own-choices)). With a config that is a link
to a pty slave, the daemon stays up after the pty master closes.

A refusal is `<class>: <detail>`, shown as `<text>` in these frames.

| Method | Answer |
|---|---|
| `git.info`, `git.status`, `git.list_branches` | `{"jsonrpc":"2.0","id":N,"error":{"code":-32603,"message":"<text>"}}` |
| `git.worktree_create` | `{"result":{"success":false,"error":"<text>","errorCode":"worktree_add_failed"}}`. Nothing is made |
| `git.worktree_remove` | `{"result":{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked: <text>"}}`. The worktree stays, and no branch is deleted |
| `git.worktree_remove` with `worktreeRoot` | `{"result":{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}}`. Nothing is removed |

On `git.worktree_remove` the refusal also comes for a worktree folder that does not
exist, and the branch `branchName` stays (also macOS and Windows). That holds in a
bare repository too (also Windows). With `worktreeRoot` the refusal comes for a
worktree that exists and for a missing one (also macOS).

#### Paths in the texts

A path in quotes uses the separators of the system, and Go's `%q` quotes it. A path
of more than 300 characters is cut to its first 300 characters, with no mark of the
cut. On Windows each backslash is doubled inside the quotes, and the JSON encoding
doubles it again. The path of the `open <file>:` text is not in quotes, and it is not
cut. The `<worktreePath>` of the remove text is as sent, and it is not cut.

On Linux and macOS the path of the config is the resolved path, so a request through
a symbolic link gets the real path. On Windows a request through a junction keeps the
spelling of the junction, and a request in another letter case gets the real spelling.

The path of an included file is the folder of the file that holds the setting, one
separator, and the value as written. The Windows `is outside` text is the exception:
it names the cleaned path. On Windows `path = <dir>/inc.cfg` gives
`<git dir>\<dir>/inc.cfg`. The folder of the holding file stays as written too: with
`include.path = a/../b/inc.cfg`, a `path = x.cfg` in that file names
`<git dir>/a/../b/x.cfg` (also macOS).

#### Turning the read off

`"shadowDirOff": true` in the request turns the read off for that request, on the
five methods (also macOS and Windows). A git request that starts while it runs has the
switch too. The request then gets the answer that it gets with no read. A FIFO as the
config then gets no answer in 10 s (also macOS). The member name matches without
regard to letter case, so `"SHADOWDIROFF": true` is the switch too (also macOS and Windows). The values
`"true"`, `1`, `null` and `false` act as no member (also macOS and Windows).

#### Class 1: the git config

Class 1 is `this repository's git config cannot be used: <detail>`. The format
version and the extensions of an included file are not tested.

| Detail | When |
|---|---|
| `the repository uses format version "<v>", which this version of Claude does not support` | The last value of `core.repositoryformatversion`, as git lists it, is not exactly the text `0` or `1`. A config with no such key is not refused. `<v>` is cut to its first 20 characters, with no mark (also macOS and Windows) |
| `the repository uses git extension "<name>", or a value of it, that this version of Claude does not support` | The format version is `1`, and an `extensions.<name>` setting is refused (see "Extensions" below). `<name>` is in lower case. It is cut to its first 60 characters, with no mark (also macOS and Windows) |
| `"<file>" includes "<value>", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full` | An include value of the form `~user/…` or `%(prefix)/…`, in an `include` section or in an `includeIf` section. The condition of the `includeIf` section does not have to match. `<file>` is the file that holds the setting. `<value>` is cut to its first 100 characters (also macOS) |
| `its includes go past what Claude follows (16 files, nested 3 deep)` | More than 16 included files, or an include chain of more than 3 levels below the config. A missing file counts, and so does the file of an `includeIf` section, also a missing one. A file that includes itself gets this text too |
| `"<file>" is not a regular file` | The config or an included file is a FIFO or a directory. A config that is a symbolic link to the null device gets it, and so does an included file that is a symbolic link to a FIFO. `include.path = ~` gets it with the path of the home folder. So does a `config.worktree` of the git directory with `extensions.worktreeConfig` on (also macOS). The daemon's own read does not wait on a FIFO |
| `"<file>" is larger than 8 MiB` | The config, an included file or that `config.worktree` has more than 8388608 bytes |
| `"<file>" has a setting in section "<section>" whose name is longer than 1024 bytes` | The full name of a setting, with its section and subsection, has more than 1024 bytes. The sections are `core`, `remote`, `lfs`, `url`, `filter`, `extensions`, `include`, `includeif`, `index`, `feature` and `checkout` (the last three also macOS and Windows). `<section>` is in lower case, without the subsection. The bound holds in an included file, with the path of that file |
| `"<file>" has more settings than Claude reads (8 MiB); remove repeated ones` | See "The count of settings" below |
| `open <file>: <reason>` | The daemon cannot open the config, an included file or that `config.worktree`. A file of mode 000 gives `permission denied`. A socket gives `no such device or address` on Linux. As the config, a socket gives `operation not supported on socket` on macOS |
| `<error lines of git>` | The listing of the config exits non-zero while a child holds its stderr. The lines read until then are joined by one space (also macOS and Windows) |
| `fatal: bad config line <n> in "<file>"` | The config or an included file has a syntax error. So does the `config.worktree` file of the git directory with `extensions.worktreeConfig` on. This text comes before an extension refusal. The text is that of git, with the path in quotes |
| `"<included file>" is outside "<repo>", and on Windows Claude reads a repository's settings only from inside it; if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead` | Windows: a relative include that leaves the repository. For a bare repository `<repo>` is its git directory |
| `"<file>" includes "<value>", and on Windows Claude follows an include only if its path is relative and stays inside the repository; if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead` | Windows: an absolute include value, also to a missing file, `~/x` and `~root/x`. An absolute value and `~/x` get it also in an `includeIf` section whose condition does not match. `<value>` is cut to its first 300 characters |
| `"<file>": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to` | Windows: the config is a file link. Also Windows, on every git method that reads the config: the git directory is a junction or a directory symbolic link, and `<file>` is `<git dir>\config` |

No refusal comes from a config that is missing, empty, a dangling symbolic link or a
symbolic link to a regular file. A NUL byte in a value, an empty `include.path` and
one with no value give none either. On Linux the daemon follows a relative include,
also one that leaves the git directory. It also follows an absolute include, and
`~/x` with a home folder.

Two more kinds of file get the kind test and the size test, with their own path in
the text. A format version `2` in such a file is not refused.

- The file of an `includeIf` section, whatever its condition is (also macOS and
  Windows). Its filter settings count for the filter rule of the checkout.
- The `config.worktree` file of the linked worktree that the trust check of the
  request finds. On `git.status` it is the worktree that `baseRepo` names. If the
  config turns `extensions.worktreeConfig` on, the file gets the tests. `true`, `yes`,
  `on` and `1` turn it on, and so do `TRUE`, `ON`, `Yes`, `On`, `tRuE` and the key
  with no value. `false`, `no` and `0` do not (also macOS and Windows, with
  `git.worktree_remove` on Linux only).

#### Extensions

Under format version `1` the daemon refuses an extension other than
`worktreeConfig` in two cases. [The copied settings file](#the-copied-settings-file)
has no rule for its key, or the rule does not accept its value. Refused are
`preciousObjects = 1234567890`, `objectFormat = sha512`, `refStorage` with no value and
`partialClone = a/-b`. Not refused are `worktreeConfig = maybe`, `objectFormat = SHA256`, `refStorage = FILES`,
`preciousObjects = yes` and `preciousObjects = 1` (also macOS and Windows). An
extension in a config with no format version key is not refused. Under version `1`
the rules accept `objectFormat = sha256` and `refStorage = reftable`, so a sha256 or
a reftable repository is not refused. A value that git itself refuses but the copy
leaves out, such as `extensions.objectformat = md5` under version `0`, gets no
refusal of the daemon. The method answers as git does. With `objectFormat = SHA256`
under version `1`, `git.status` of `5fd08069` answers `exit status 128`.

#### The count of settings

Each setting of a counted section costs the bytes of its full name, the bytes of its
value, and 96. The config has its own sum, and the included files have one sum
between them. The file in which a sum passes 8388608 is refused. The counted sections
are `core`, `remote`, `extensions`, `include`, `branch`, `lfs`, `index`, `url`,
`filter`, `feature`, `checkout` and `includeif`. The sections `x` and `user` do not
count. This arithmetic is fitted to the measured bounds (also macOS and Windows).

#### Class 2: files of the git directory

Class 2 is `a file in this repository's .git directory cannot be used: <detail>`.
The files are `shallow`, `info/attributes` and `info/exclude`, and the git directory
itself. The daemon tests them in each request.

| Detail | When |
|---|---|
| `"<file>" is not a regular file` | `info/attributes` or `info/exclude` is a FIFO or a directory. A symbolic link there passes, also one to the null device. `shallow` is a FIFO, a directory or a symbolic link to the null device. The daemon does not wait on a FIFO |
| `"<file>": it is larger than 4 MiB` | `info/attributes` or `info/exclude` has more than 4194304 bytes |
| `"<file>" is larger than 4 MiB` | `shallow` has more than 4194304 bytes |
| `open <git dir>: not a directory` | Windows: a daemon `GIT_DIR=.git` names the `.git` file of a linked worktree, on the five methods |
| `open <file>: <reason>` | `shallow` has mode 000 (`permission denied`) or is a socket (`no such device or address`). The path is not in quotes. An `info/attributes` or `info/exclude` of mode 000 is not refused |
| `"<file>" holds something other than object ids` | A line of `shallow` is not 40 or 64 hex characters in lower case. That rule is fitted to the measured states. A blank line, an id with a space after it and a file of one line end are refused. A line end of CRLF, a last line with no newline and an empty file pass |

#### Which answer comes first

A refused config comes first. A refused `config.worktree` file of a linked worktree
comes next (from the code), and then a refused file of the git directory. In class 2, `shallow` comes before `info/attributes` (also macOS and Windows),
and `info/attributes` comes before `info/exclude`. From the code: a refusal of the
trust check or of the daemon's own `GIT_CONFIG_COUNT` comes before them all, and on
`git.worktree_remove` with `worktreeRoot` the read comes before the configuration
listing.

#### A folder that is not a repository

A folder with no repository keeps its "not a repository" answers, and the daemon reads
no config there. That holds for a `HEAD` that is no ref and no object id, also with
format version `2`. It also holds for a repository of another owner with no
`safe.directory` (also macOS and Windows). Such an answer is not kept, so a new
`safe.directory` line counts at the next request (also macOS). After a change of the
owner inside the 5 seconds of a kept good answer, a request answers normally. The
first request after that time answers "not a repository".
[The call about the repository](#the-call-about-the-repository) decides that state
and how long an answer is kept.

#### claustrum's own choices

These rules are from the code and are not measured on `5fd08069`:

- With a daemon `GIT_COMMON_DIR`, `git.info` and `git.list_branches` read nothing.
- `extensions.worktreeConfig` starts the `config.worktree` test with values that no
  run had, such as `oN` or `007`. The last such setting of the config counts.
- Under format version `1` the value rule refuses extension values that no run had,
  such as `preciousObjects = -1`.
- The cuts of the format version value and of the extension name count characters.
- The `config.worktree` open error and size bound hold on `git.status`,
  `git.list_branches` and `git.worktree_remove` too, and every kind on Windows.
- The name bound and the count of settings are tested on a whole file before an
  include of that file is followed. So 200000 include lines of a missing file get the
  "more settings" text, as measured. The two include limits come before the open of an included
  file.
- A read with a failed open or read of the daemon is not kept, also when it accepts
  the config. So the next request reads again.
- The change test misses a change that keeps the size, modification time, mode and
  identity, where the system gives one, of each file of the read.
- An open error of the file of an `includeIf` section gives no refusal. An open
  error other than the measured ones gets the same `open <file>: <reason>` form.
- A `~/x` include is joined to the home folder and cleaned. With no home folder it
  gets the "expands neither" refusal. No daemon starts with no home folder. On Windows a `%(prefix)/…` value gets the
  "expands neither" text, with the cut at 100 characters.
- The listing that the read takes from git for one config file has a bound of 64 MiB.
  The read tests the name bound and the count on the first 64 MiB of a longer
  listing. If neither refuses, the read gives no verdict.
- If the read gives no verdict, the method goes on as with no read. A failed git call
  gives none, except for a syntax error, the error lines above and the stop at 30
  seconds ([Time limits of git calls](#time-limits-of-git-calls)). So a git that does
  not start gives none, and so does a failed call with no holder of stderr. A config
  file that the daemon opens and cannot read gives none too.
- A bare repository whose config the daemon refuses, on a host where git refuses bare
  repositories (`safe.bareRepository = explicit`), gets the refusal.
- An `includeIf` line in a file three levels below the config gets the "includes go
  past" text.
- On Windows the daemon refuses a link at a config file and at an included file, and a
  junction or a directory symbolic link at the git directory itself. It tests no other
  folder above the file. It also refuses an include value that starts with a slash or
  a drive.

### The settings copy

The daemon keeps its own copy of the git settings of a repository. The copy is in a
folder `git-shadow` beside the socket. The git calls of the five git methods name
that copy, so git reads the copy and not the config of the repository. A rule with
no mark and no system was measured against `5fd08069` on Linux. A rule that names a
system was measured on that system.

#### The states and `settingsControl`

`git.info` reports the state in its last member, `settingsControl`. No other method
sends it, and an error reply has none (Linux, macOS and Windows).

| `settingsControl` | When | What it changes |
|---|---|---|
| no member | No start test of the daemon gave a state yet | Nothing. A folder with no repository, or a `HEAD` that is no ref, starts no test. A `git.info` on a `.git` file to a missing repository starts it |
| `"shadow"` | The start test passed | The git calls name the copy. Before them the daemon asks git about the repository |
| `"fallback-git"` | The git of the host does not work on such a copy, for example git 2.11.4 and 2.12.5 | The git calls name the git directory. The daemon reads no repository config, gives no config refusal, makes no copy and does not ask git about it |
| `"off-switch"` | A request with `"shadowDirOff":true` runs (the hold) when the reply is made | A request that starts then names the git directory, with no test, folder or config read |

- After the test the "no repository" reply has the member too:
  `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`.
  That reply waits for a running test and has its state. After a failed test it has
  no member. A path that does not exist gets the same, but starts no test itself. On
  a folder with no repository the other methods answer at once.
- As the first git request of a daemon, a `git.info` on a folder whose `.git` file
  names a missing path runs the test and gives that reply. A missing entry under
  `worktrees` of an existing git directory runs no test, and the reply has no member
  (Linux, macOS and Windows). A missing entry in a missing repository runs the test.
- With no git in the daemon's `PATH`, a `git.info` on a repository answers "no
  repository" with no test. The daemon still makes its folder, with the lock file
  only. The member follows the daemon's state (not measured).
- A `git.info` on a regular file gets the gate of the repository of its folder.
  Outside any repository it starts no test. As the first git request of a daemon its
  reply then has no member on Linux and macOS. On Windows it is the hooks refusal,
  with `fork/exec <git.exe>: The directory name is invalid.`
- A request with `"shadowDirOff":true` gets `"off-switch"` also as the first request
  of a daemon, in a "no repository" reply and while a failed test is kept.
  [Turning the read off](#turning-the-read-off) has the values that count as the
  switch.
- During a hold, each later command that starts its own config listing names the
  git directory (Linux, macOS and Windows). The config check and the call after it
  keep the copy, as does a command that already runs. On Linux, after the hold a
  later command runs the test and takes the copy. A create in a hold makes no copy.
- During a hold, `git.status`, `git.list_branches` and `git.worktree_remove` get no
  config refusal. On a repository of format version 3 they answer
  `{"isRepo":false,"clean":false}`, `{"isRepo":false,"branches":[]}` and the
  lock-check text that ends `(its registrations could not be examined); retry`, and
  the remove deletes nothing. On a config that only the daemon refuses, the remove
  deletes. macOS and Windows are not measured for these replies and removes.
- A held request keeps no hold for later requests. A `worktree add` that started in
  the hold is not stopped at 2 minutes during the hold. It is not stopped at 2
  minutes also after the holder answers (claustrum's own, not measured). A held call
  that starts after that answer has the 2 minute limit (claustrum's own, not
  measured).
- In the state `fallback-git` the test runs again on the first request more than 10
  minutes after the last test. In the state `shadow` no second test ran up to 720 s.
- The daemon logs one `[GitHandler]` line at the first decision of the state and one
  at a change.

#### The start test

The first request without the member that reaches a repository, or a `git.info` on a
`.git` file to a missing repository, runs the test. On a `.git` file that names a
missing path the steps after the test differ from `5fd08069` (measured on macOS and
Windows), with equal frames (see
[UPSTREAM-TRACKING.md](UPSTREAM-TRACKING.md#triage-gotchas-when-a-probe-result-is-misleading)). The
test makes a small repository and a copy of it in a folder `.check-<n>` of the
daemon, and runs git calls there. One of them is the read of the test repository's
config. It runs in `/`, and on Windows in the working folder of the daemon (from the
code). The folder is gone at the reply. If the test fails, the request gets this
text:

`Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.`

`git.info`, `git.status` and `git.list_branches` answer it as a `-32603` error.
`git.worktree_create` answers it in `error`, with the `errorCode`
`worktree_add_failed`. `git.worktree_remove` answers it after `failed to remove
worktree: could not check whether <worktreePath> is locked: `, with no `errorCode`.

A failed read of the test repository's config is followed by one `git version` call. If it fails
too, the text is `git cannot run on this host; git not run: <exec error>: <git text>`
alone, in the same frames. The exec error and the text are those of `git version`. A
remove deletes nothing there (Linux, macOS and Windows).

| `<cause>` | When |
|---|---|
| `git <verb> failed under "<folder of the daemon>": <error lines of git, space-joined>` | A test call other than the read of the test repository's config failed. Go's `%q` quotes the folder |
| `git <verb> failed under "<folder>": signal: killed` | The daemon ended such a test call after 30 seconds (Linux and macOS). The reply comes near 31 s (Linux). On Windows the end is `exit status 1` |
| the same with ` (git version <v>)` at its end | The failed or ended call was one of the calls that make the test repository (measured for `init` and `mktree`) |
| `this repository's git config cannot be used: <error lines of git>` | The read of the test repository's config failed, and the plain `git version` after it works |
| `this repository's git config cannot be used: git did not finish reading "<config of the test repository>" within 30 seconds; try again` | The read of the test repository's config ran, or held its stdout, past 30 seconds. The reply comes near 31 s. With stderr alone held, a read that exits 1 gives the lines read before the cut (not measured) |
| `mkdir <path>: <reason>` or `open <path>: <reason>`, with no quotes | A folder or the lock file of the daemon cannot be made |

If git refuses the copy and the same call works without it, the state is
`fallback-git` and not a failure. A test call that works and answers wrongly gives
it too. At the last call, which is a `worktree add`, a `No space left on device` error or an
end by a signal fails the test. If two calls fail, the cause names the first.

The daemon keeps a failure for 10 seconds from the end of the failed test, also
after its cause is gone. A request in that time gets the kept text with no git call.
A request with `"shadowDirOff":true` works in that time.

#### The folders

The folder of one daemon is `<socket folder>/git-shadow/<host form>-<pid>-<16 hex>`.
`<host form>` is the host name with each character outside `A-Z`, `a-z`, `0-9` and `.`
written as `_` (measured for `-` and `.` on Linux and macOS). On Linux and macOS that
folder and each folder of a copy have mode `0700`, and the files of a copy have
`0600`. The daemon makes the folder at the first request that runs the test. It holds
an exclusive lock on the file `lock` in it for its whole life. On Windows the hold is
an open handle with no sharing.

- At `-stop` and at `server.shutdown` the daemon removes its folder. The empty
  `git-shadow` stays (Linux, macOS and Windows).
- If the folder is removed while the daemon runs, the next request makes it again
  with another hex part (Linux and macOS).
- The folder of one repository is `<folder of the daemon>/<32 hex>`. The name is the
  first 32 hex digits of the sha256 of the path of the git directory. A linked
  worktree uses the folder of its main repository.
- That folder holds `HEAD`, `config`, the empty folders `objects` and `refs`, and
  `info/exclude` where the repository has it. On Linux and macOS it holds a link
  `worktrees` to the `worktrees` folder of the git directory. On Windows it has no
  such entry.
- More copies follow the state of the repository: `shallow`, `info/attributes`,
  `rebase-merge/head-name`, `rebase-apply/head-name` and `BISECT_START`.
- The daemon makes the folder at the first request that accepts the config of the
  repository. After a change of a file, a later request writes its copy again.
- [The call about the repository](#the-call-about-the-repository) names a folder
  `.probe` of the daemon.
- One request also keeps short-lived entries in the folder of the daemon:
  `.gitdir-<n>` for a `git.status`, and `.index-<n>` and `.worktreeinclude-<n>` for a
  create. Also `.config-<n>` for a create in a repository with a `config.worktree`
  file, and `.s<n>` on Windows. In the state `fallback-git` the first three are there
  too (`.gitdir-<n>` on Linux and macOS, `.index-<n>` on Windows, and
  `.worktreeinclude-<n>` from the code). With `"shadowDirOff":true` the first three
  are in the temporary folder, as `claude-ssh-gitdir-<n>`, `claude-ssh-index-<n>` and
  `claude-ssh-worktreeinclude-<n>`. On Linux and macOS the index name is as on
  `89cb6289`. On Windows it is claustrum's own (not measured).
- On Windows, if the folder of the daemon denies new folders, a create still works
  with the backup and restore privileges on. With both off it fails at `.s<n>`.

If the folder of a repository cannot be made, the request gets this text in the
three shapes of the start test:

`Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>`

The cause is the `mkdir` error of the folder. The same request again gets the same
line with no git call. On Windows a held copy gets 2 s of retries, then `rename
<temp file> <copy>: Access is denied.` with full paths.

The request that makes the folder of the daemon also sweeps `git-shadow` (measured
on Linux and macOS, and in part on Windows). If the lock file of an entry can be
taken and its name has the host form of this host, the entry goes. An entry with
another name must also be more than 7 days old (the exact bound is not measured). A
held lock keeps the folder of a live daemon.

- On Linux and macOS the age is read after the lock step, so a folder with no lock
  file gets one and stays. On Windows the age is read before, and such a folder of 8
  days goes.
- On Windows the held lock keeps the folder of a live daemon from a remove by hand,
  so no later request sweeps again.
- A lock error other than a held lock keeps the entry. A folder with no lock file
  that is less than 1 minute old stays and gets none. Both are claustrum's own, not
  measured.
- A planted link, or a junction on Windows, goes as a link, and claustrum makes no
  lock file in its target ([D8](DIVERGENCES.md#d8)).

The folder of the daemon keeps 128 repository folders. A new repository removes the
folder with the oldest use that no git call holds. A git call holds its folder until
the wait for its pipes ends (claustrum's own, not measured). With all 128 held, a
request gets this text and nothing goes:

`Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again`

A folder goes between the git calls of a request, never during one. The next call
makes it again with no new config read, except a call right after its own config
listing (claustrum's own, not measured). A failed remake gets the `cannot write its
copy` text above, or the 128 text when all 128 folders are held (from the code). A call whose folder went and is not made
again, or whose daemon shut down or replaced it, fails alone (from the code). It gets
this text, and a listing puts `listing the configuration in force: ` before it (Linux
and Windows):

`Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again`

#### The copied settings file

The `config` of the copy is the daemon's own text. Each carried setting is one block
of two lines, `[<section>]` and `<key> = "<value>"`, in the order of the file of the
repository. The last `core.bare` of `config.worktree` follows, then the `[url]`
blocks of rewritten URLs. The file ends with
`[lfs]\n\trepositoryformatversion = 0\n`.

- 41 keys are carried, each with one rule of accepted values (`gitshadowconfig.go`).
  Only some of those rules are measured.
- `remote.<name>.url`, `.lfsurl`, `.promisor` and `.partialclonefilter`,
  `lfs.<url>.access` and `branch.<name>.remote` are carried too. The length of a
  rewritten URL is not measured.
- Every other key is dropped, such as `core.hooksPath` and `user.*`.
- Outside the version and extension refusals, a value that its rule does not accept
  is left out with no refusal. One example is `core.filemode = maybe`.
- Where the copy carries a value that git refuses, `git.info` answers "no
  repository" with the member (Linux, macOS and Windows). One example is
  `extensions.objectformat = sha256` in a sha1 repository.
- A setting of an `[include]` file is carried at the place of the include line. Its
  format version, `extensions.*`, `core.bare` and `filter.*` (not measured) are
  dropped. A setting of an `includeIf` file is not carried.

#### The call about the repository

In the state `shadow` the daemon asks git about the repository before the calls of
the method. The call is `-c core.hooksPath=<null device> -c core.fsmonitor=false
rev-parse --git-dir`, in the root of the work tree. It names
the folder `.probe` of the daemon, and the daemon writes an emptied `.probe/config`
again when it changed. The call drops the daemon's `GIT_NAMESPACE`,
`GIT_INDEX_FILE`, `GIT_DISCOVERY_ACROSS_FILESYSTEM` and
`GIT_ALTERNATE_OBJECT_DIRECTORIES`, and keeps its `GIT_CONFIG_COUNT` pairs (Linux,
macOS and Windows). So a pair such as `safe.directory` changes the answers (from the
code). A good answer is kept 5 seconds from the end of the call. A change of the root
folder or of its `.git` entry makes the daemon ask again.

| Answer of git | Reply |
|---|---|
| exit 128 with a text that starts with `fatal: not a git repository` in any letter case, or that holds `detected dubious ownership` or `cannot use bare repository` | The "no repository" answer of each method. No config is read, and no copy is made. git 2.56's text for a `.git` file to nowhere, `unsafe repository`, and the two phrases at other places of the text get the same answer (claustrum's own, not measured) |
| exit 1, exit 128 with no text, a signal (`signal: terminated`) or a git that cannot start (Windows) | `git could not check this repository on the remote machine: at "<root>": exit status <n>` |
| exit 1 or exit 2 with a text, or exit 128 with another text | The same text with git's text in place of `exit status 1`. Two lines stand there with one space between them. `warning: w` before `fatal: not a git repository` gets this reply, and so does `fatal: error reading '<repo>/.git'` (Windows). This row holds on all five methods |
| either of the two rows above | One `git version` call follows. If it fails, the reply is `git cannot run on this host; git not run: <exec error>: <git text>` in the frame of each method. A remove makes two such calls and deletes nothing. Not measured: a git that cannot start, `git.info` and `git.worktree_create` after exit 128 with no text or a signal, a remove with `worktreeRoot` |
| another path than the git directory, in two calls | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again`. `<path>` is the path as git printed it: with forward slashes on Windows |
| no answer in 30 seconds | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` (Linux, macOS and Windows) |

The three texts have the shapes of the start test. A bad answer is not kept, and a
remove asks twice. For a repository of another owner with no `safe.directory`,
`git.worktree_remove` answers the lock-check refusal that ends `(its registrations
could not be examined); retry`. With a child that holds the pipes, an exit 0 counts
by its output so far (measured on `git.status` only).

#### The git calls

In the state `shadow` each hardened call carries `GIT_COMMON_DIR=<copy>` and
`GIT_OBJECT_DIRECTORY=<git directory>/objects`. A call with no `--git-dir=` in its
arguments also carries `GIT_DIR` and `GIT_WORK_TREE` (Linux, macOS and Windows). The
four come right after the profile values, in that order (order measured on Linux and
macOS).
[Hardened git calls](#hardened-git-calls) has the other points of a call.

#### `config.worktree`

The copy has no `extensions.worktreeConfig` setting, so git reads no
`config.worktree` file in the state `shadow`. The daemon does two things in its
place, in a repository with that extension and a `config.worktree` file. The
`worktreeConfig` values `2` and `10` turn that extension on.

- Each key of the copied settings file but the format version, `extensions.*` and
  `core.bare` goes before the option list of the git calls as one `-c` item, with the value of the copy,
  sorted by key. Six keys are measured. Other keys and a repeated key are not
  measured. A call on a linked worktree gets the items of its own file.
- A new worktree of `git.worktree_create` gets its own `config.worktree` file. See
  [git.worktree_create](protocol/git-worktree-create.md).

### Hardened git calls

The git methods run most git steps as hardened calls. A hardened call is a git call
with a fixed list of `-c` options and a fixed set of environment variables. The lists
and the variables follow `5fd08069`. The hook pins were measured against `f6010b97`,
and their limits against `89cb6289`. A rule that names no system was measured on
Linux, macOS and Windows. `githarden.go` holds the lists, and
`githarden_shape_test.go` tests them.

Each hardened call passes no `-C`. Before each one, the daemon runs a config
listing (`git config -z --list`) with the profile of that call. Before its first
listing, a request with no cached value runs the excludes read, `git config
--includes --path core.excludesFile`, in the temporary directory. It adds only
`GIT_DIR=<null>`. The probe `git --attr-source=<empty tree> version` adds no
variable. There are two profiles:

- The heavy profile forbids every transport protocol
  (`GIT_ALLOW_PROTOCOL=denied_by_claude_ssh`). `git.info`, `git.list_branches` and
  `git.status` use it.
- The light profile allows `https:ssh`. Most calls of `git.worktree_create` use it.
  In the states `shadow` and `fallback-git` without `"shadowDirOff":true`, the light
  calls of a create carry the heavy variables, except the checkout and its listings
  (measured on Linux and macOS in the state `shadow`, and on Windows in the state
  `fallback-git`).

[The daemon's own git environment](#the-daemons-own-git-environment) gives the
variables of the daemon that change these values. A variable that the daemon adds
wins over the same variable of the daemon's own environment (measured on Linux and
macOS). On Windows claustrum gives a git child its environment in name order. That
is [D23](DIVERGENCES.md#d23).

A client sees the hardening in these places:

- `git.status` reads the real `HEAD` commit, also where `refs/replace` names another
  (see [git.status](protocol/git-status.md#what-the-status-reads)).
- The failure texts of `git.worktree_create` hold no graft-file `hint:` lines: the
  light list turns them off. No `git.status` reply holds one either, because its
  calls give only stdout to the reply.
- On Windows a create with a tree path of 270 characters succeeds.
- The records of a new worktree hold absolute paths, also where the user config sets
  `worktree.useRelativePaths` (measured on macOS and Windows).
- Each call passes the user's global excludes file as `core.excludesFile`. When
  the user has none, the value is the null device. On Windows the `status`,
  `ls-files` and `diff-index` calls of `git.status` pass `/dev/null` there. That is claustrum's
  own, see [D16](DIVERGENCES.md#d16).
- The config listings run with `LC_ALL=C` and `LANGUAGE=C`. git then writes its
  listing errors in English (measured on Linux). The one exception is the second
  listing before the checkout of a create in the state `shadow`, which adds
  `LANGUAGE=C` alone. The call after a listing keeps the daemon's `LC_ALL`,
  `LANGUAGE` and `LANG`. The checkout in the state `shadow` is an exception too: it
  ends with `LANGUAGE=C`.
- On Linux and macOS, with `"shadowDirOff":true`, a create can answer the text
  `open <tmp>/claude-ssh-index-<n>/index: no such file or directory`. `<n>` is
  decimal digits.

#### Refusal texts

The first config listing of a method is the hooks refusal check. It is not an extra
call. Each method answers these texts in its own error frame (see the
[Error-string catalogue](#error-string-catalogue)):

| Cause | Text |
|---|---|
| The listing fails | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: <exec error>: <git text>`. When git wrote no text on standard error, the text ends after `<exec error>`. [Git-directory trust check](#git-directory-trust-check) gives the classes of the failure |
| The listing fails, and `git version` fails too | `git cannot run on this host; git not run: <exec error>: <git text>`, with the detail of `git version` |
| A config key over 1048576 bytes | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` |
| More than 1024 hook names | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` |
| Hook names over 65536 bytes in sum | `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` |

A config value has no cap: 2 MiB passes. In some sections a key over 1024 bytes is
refused before the listing (see [Repository config refusals](#repository-config-refusals)).
The limits refuse at the first listing only. In a folder that holds no repository, the
first listing follows [The daemon's own git environment](#the-daemons-own-git-environment).
There a limit refuses each method except `git.status`. That rule is from the code.

#### Hook pins

Each hardened call pins off the hooks that the listing right before it names (from
the code). A key
`hook.<name>.<var>` names a hook. The name is the text between the first and the last
dot, and it can be empty or hold dots. `hook.<var>` names no hook, and a hook key
inside a value pins nothing. The call gets `hook.enabled=false` and an empty
`hook.event`, and for each name `hook.<name>.enabled=false` and an empty
`hook.<name>.event`. The pins go in through `GIT_CONFIG_COUNT` (see
[The daemon's own git environment](#the-daemons-own-git-environment)). Only
`command` and `event` were measured as `<var>`.

- A later listing that exceeds a limit does not refuse its call. The call then gets
  only `hook.enabled=false` and an empty `hook.event` (from the code).
- A later listing that fails refuses in three places:
    - the two later listings of `git.status` (see
      [git.status](protocol/git-status.md#errors)). For the listing in the git
      directory, this is measured on Linux. For the listing in the temporary
      folder, it is from the code.
    - the two listings before the checkout of a create in the state `shadow`
      (measured on Linux and macOS, see
      [git.worktree_create → Error texts](protocol/git-worktree-create.md#error-texts))
    - any listing that its time limit stopped (measured on Linux for the listing
      of the checkout)
- Two names that differ only in letter case get two pins. A name under two keys
  counts once toward the limits. The count is tested before the byte sum. These
  three rules are from the code.

### The daemon's own git environment

This section names the variables of the daemon's own environment that reach the
calls of the git methods. It also gives the refusals of that environment. A rule
here holds on Linux, macOS and Windows, unless it names fewer systems.

#### Variables that the git calls keep or drop

The repository calls are the configuration listings, the hardened calls,
`worktree add`, the checkout and `git status`. The excludes read and the
`--attr-source` probe get the daemon's environment as it is, plus their own
additions.

- The daemon's `PATH`, `GIT_EXEC_PATH`, `GIT_SSH` and loader variables are
  cleaned for the repository calls and the calls of the start test. The calls of
  the start test also get no `GIT_` variable of the daemon.
- `GIT_CONFIG` and `GIT_CONFIG_PARAMETERS` do not reach a repository call. The
  names match exactly, in upper case. So on Windows `git_config` and
  `Git_Config_Parameters` reach git.
- A listing gets the daemon's `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` as they are. It keeps a name with a leading-zero index,
  such as `GIT_CONFIG_KEY_01`, in its place (measured on Linux and macOS).
- A hardened call gets `GIT_CONFIG_COUNT` first, with the number of all the pairs.
  The daemon's pairs `0` to `count-1` follow it, in index order. The
  [hook pins](#hook-pins) follow them, from the pair `count` on. The
  `GIT_INDEX_FILE` of the checkout and the `GIT_OPTIONAL_LOCKS` of the `status`,
  `ls-files` and `diff-index` calls follow the pins. The order is measured on Linux
  and macOS.
- A hardened call gets no other `GIT_CONFIG_KEY_<digits>` or
  `GIT_CONFIG_VALUE_<digits>`. A leading-zero index such as `GIT_CONFIG_KEY_01`
  is removed too. Other names, such as `GIT_CONFIGX` or `GIT_CONFIG_S4`, reach
  git.
- The daemon's `GIT_ALLOW_PROTOCOL` sets the light value. Unset, it is
  `https:ssh`. Else the daemon splits it on `:` and keeps each entry that is
  exactly `https` or `ssh`, once, in its order. The match is case-sensitive, with
  no trim. With no entry left, it is `denied_by_claude_ssh`. The heavy value stays
  `denied_by_claude_ssh`. On Windows a lower-case `git_allow_protocol` sets it
  too.

The daemon reads each value that it parses from the standard output of git (from
the code). The reference does so for the branch of `git.info` and the repository
test of a create (measured on Linux).

[Git-directory trust check](#git-directory-trust-check) gives the `GIT_DIR` of the
listing in a folder with no repository, and its frames.

#### The check of `GIT_CONFIG_COUNT`

The git methods check the daemon's `GIT_CONFIG_COUNT`, with no git call except
the excludes read. The check reads the count and its pairs by their exact
upper-case names. On Windows it does not see a lower-case `git_config_count` or
`git_config_key_0`.

- If the count is not set, there is no check.
- An empty value counts as `0`. Else the check skips leading space, tab, `\n`,
  `\v`, `\f` and `\r` bytes. One optional `+` and one or more digits follow, and
  then the end. So `\t1`, ` +1` and `01` count as `1`, and `00` counts as `0`.
  Other values are refused. Examples are `-1`, `1\n`, `+ 1`, `++1`, a lone tab and
  a no-break space before the digit. So are `0x1`, `x` and `99999999999999999999`.
  The text is `inherited GIT_CONFIG_COUNT "<value>" is not a count`, with the value
  in Go `%q` quotes.
- For each index from `0` to `count-1`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` must both be set. `<n>` is the plain decimal index, so
  `GIT_CONFIG_KEY_01` is not pair `1`. An empty value counts as set. The first
  index where one is not set is refused with
  `inherited GIT_CONFIG pair <n> is incomplete`.
- An empty key is not refused here. git then fails the listing, also in a folder
  that holds no repository (see the next section).

`GIT_CONFIG_PARAMETERS='x` changes no frame in a plain folder.

#### Configuration listings that git cannot parse

With a key that git cannot read, git fails each configuration listing with exit
status 128. `<text>` is then `config-defined hooks could not be pinned off; git not
run: listing the configuration in force: exit status 128: <git text>`. A good pair
before the broken pair changes nothing. `<git text>` comes from git, for example:

| Key | `<git text>` |
|---|---|
| empty | `error: empty config key fatal: unable to parse command-line config` |
| `nodot` | `error: key does not contain a section: nodot fatal: unable to parse command-line config` |
| `a.b c` | `error: invalid key: a.b c fatal: unable to parse command-line config` |

A `GIT_CONFIG_GLOBAL` that names a file git cannot parse fails each listing too.
With the content `[broken`, `<git text>` is `fatal: bad config line 1 in file
<file>` (measured on Linux and macOS). The `git version` call after a failed
listing gets no `GIT_CONFIG_GLOBAL` and passes, so the text stays the hooks
refusal (measured for `git.info` and `git.worktree_create`).

This refusal needs a folder where git starts. A path that does not exist answers
as it does with nothing set (measured on Linux and macOS, and for `git.info` on
Windows). On Linux and macOS a plain folder of mode 0000 answers so too. A plain
folder gets the refusal. Nothing changes on disk.

#### Where each method answers the refusal

Both refusals start with `config-defined hooks could not be pinned off; git not
run: `, and each method answers them in one frame. A refusal of the git-directory
trust check comes first, with the trust text in the frame. A remove of a present
worktree gets the lock-check text instead. For the count, the trust-check order
is measured on Linux and macOS, in a remove only for a gone worktree without
`worktreeRoot` or a `baseRepo` that exists with it. On Windows it is measured in
`git.info`, `git.status` and the remove of a gone worktree. For a listing that
git cannot parse it is from the code. "The count" below is the check of
`GIT_CONFIG_COUNT`, and "the listing" is a listing that git cannot parse.

| Method | Frame | Where |
|---|---|---|
| `git.info` | `{"error":{"code":-32603,"message":<text>}}` | Also a plain folder. The listing also refuses a folder with an empty `.git` folder, and from the code the count does too. The count also refuses a missing folder and a file |
| `git.list_branches` | the same | Also a plain folder. The count also refuses a file and an empty or absent `path`, with a repository or a plain folder as the daemon's working directory. A `path` that is not empty and does not resolve, or that is inside a managed worktrees directory, gets `{"isRepo":false,"branches":[]}` before the count, with no git call |
| `git.status` | the same | Also a plain-folder `baseRepo`. The count also refuses a `path` that is a plain folder, the repository, missing or a file. A `baseRepo` that does not resolve gets `{"isRepo":false,"clean":false}` before the count, with no git call. See [git.status](protocol/git-status.md#baserepo). From the code: a `baseRepo` in a managed worktrees tree answers the same way. That case is not measured with a refused count |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, and no folder is made | Also a plain-folder `baseRepo`. The listing refuses it without and with `worktreeRoot`, and from the code the count does too. Without the refusal, a create with `worktreeRoot` there on Windows answers `not_a_repo`. The count also refuses a missing `baseRepo`. It comes before the refusals of a relative `worktreePath`, a `..` component, a path outside the repository and a path that exists. Three frames come before it: the `-32602` frame of a missing `branchName`, the `nested_base_repo` frame of a `baseRepo` inside a managed worktrees directory, and on Windows the frame of claustrum's own trust-root test. On Windows the count also comes before the `worktreeRoot` refusal |
| `git.worktree_remove` | Without `worktreeRoot`, for a gone worktree: `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`. With `worktreeRoot`: `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}` (measured on Linux and macOS) | A plain-folder `baseRepo`. The list below has the other cases |

The other cases of `git.worktree_remove`:

- These refusals come first, with their usual frames: a relative path, a path
  outside the repository, the repository itself and a `..` component. With
  `worktreeRoot` they are also a path outside the root, a `..` `baseRepo` and a
  root in `baseRepo` (measured on Linux and macOS). On Windows the `worktreeRoot`
  refusal comes first. For a listing that git cannot parse, that is from the
  code.
- On Windows a `baseRepo` that fails claustrum's own trust-root test gets the
  managed-worktrees refusal first.
- Without `worktreeRoot`, `open <baseRepo>: not a directory` comes first for a
  regular-file `baseRepo`. So does `statat .claude: permission denied` for a
  `baseRepo` with mode 0600 and a path under `.claude` (measured on Linux and
  macOS).
- Without `worktreeRoot`, the count also refuses a missing `baseRepo` in the
  gone-worktree frame. For a worktree directory that is present, the count puts
  `its registrations could not be examined` in place of `<text>`. On Linux and
  macOS a `baseRepo` of `<T>/missing/..`, which does not resolve, gets that frame
  too. On Windows that `baseRepo` gets the managed-worktrees refusal first.
- With `worktreeRoot`, the count also refuses a `baseRepo` that is a repository in
  the work-tree frame. A trust refusal puts its own text there instead. For a
  missing absolute `baseRepo` without a `..` component, the count answers in the
  gone-worktree frame. A worktree directory that is present then gets `the
  repository at <baseRepo> could not be read` in place of `<text>`. If the root is
  missing too, the "not reachable" refusal comes first. Each rule of this bullet
  is measured on Linux and macOS.

These states are not measured. The rules are from the code.

- A listing in a folder that holds no repository passes, but it exceeds a limit
  of the hook pins (see [Hardened git calls](#hardened-git-calls)).
  `git.info`, `git.list_branches` and `git.worktree_create` answer the limit
  refusal in their "refused" frames. `git.worktree_remove` answers it as `<text>`
  of its two frames in the table above. `git.status` answers
  `{"isRepo":false,"clean":false}`. It refuses in that folder only for a listing
  that fails.
- A `.git` file with no `gitdir:` line. Its listing carries `GIT_DIR=<null>`, so
  the folder answers as a plain folder does.

### Time limits of git calls

On `git.list_branches`, `git.info`, `git.status` and `git.worktree_create` most
calls end at 2 minutes. Config listings before a git call have a 30 second limit (the one before the
checkout: Linux and macOS VMs).
So do the repository config read (`git.status`, Linux VM) and each
`config.worktree` listing (Linux VM). A create with the filter rule and no
settings copy runs claustrum's own `--show-scope` listing, with the 2 minute
limit and the `timeoutMs`. A stopped call of the top folder pair stops the create
with the stop text (claustrum's own). The scan of ignored files ends at 10
minutes (Linux and macOS VMs). Read-tree checkouts have no such limit.

- The stop is SIGTERM, then SIGKILL 2 s later. On Windows the process ends at
  once. Config file listings get SIGKILL alone, not the listing before the
  read-tree call (Linux and macOS VMs).
- Before a good add no call follows a stopped call. A stopped add removes an
  empty leaf. A stopped repository test of a create answers the stop text alone,
  with `worktree_add_failed` (Linux VM). After a good add the rollback runs its
  own calls (Linux and macOS VMs). A stopped scan copies nothing, and the create succeeds.
- While a `"shadowDirOff":true` request runs, no call of that request stops at
  2 minutes. No call of another request stops at 2 minutes then either (Linux
  VM). claustrum's own limit is then 30 minutes.
- `timeoutMs` does not end the add. With `"shadowDirOff":true` it does not end
  the listing before the checkout either (Linux VM).
- With no `timeoutMs` a reply waits 2 s after git exits for a child that holds
  its output and gets no signal. Listings before a call wait until stdout
  closes, also past the limit. The `status` scan waits too, within its limit
  (Linux VM). Held past 30 s, the
  config read on standard input ends near 31 s (Linux VM), and `config.worktree`
  reads too (not measured). A holder of stderr alone delays a config listing
  about 2 s and that read 1 to 1.5 s (Linux and macOS VMs).
- `git.worktree_remove` gets none of these limits. Its first call has the 30 s
  bound of the call about the repository (Linux VM).

### The branch step

The branch step deletes a branch only when another local branch or a
remote-tracking ref reaches its tip. Else it keeps the branch.
`git.worktree_remove` runs it after the worktree directory and its registration
go. The `git.worktree_create` rollbacks run it on the branch that the call made.
`<b>` is `branchName` as sent. `<tip>` is the object name in the listed record
of `refs/heads/<b>`. A rule of this section with no other mark is measured
against `89cb6289` on a Linux VM.

The step runs `for-each-ref`, `rev-parse --verify`, `rev-list` and
`update-ref`. Each runs in `baseRepo` after its own configuration listing. It
carries the light `-c` list and the six values of the heavy profile, in an order
of their own (see [Hardened git calls](#hardened-git-calls)) (Linux, macOS and
Windows VMs).

#### Which branch the step deletes

- The step runs no git call for an empty `branchName`, or for one that starts
  with `-` or `+`. The branch stays.
- Another local branch at or past the tip reaches it, and so does a
  remote-tracking ref. Then the step deletes `refs/heads/<b>`. A tag, a detached
  HEAD, a symbolic ref and a branch at a parent commit do not reach it.
- A branch that is checked out in another worktree gets no guard of its own.
- If `branchName` names a symbolic ref, the step deletes the symbolic ref only.
- The delete passes the tip that the check saw as the old value. So git refuses
  the delete if the branch moved after the check.
- If the branch list is read and `rev-parse --verify` reports the branch absent,
  the step deletes nothing and keeps nothing (Linux, macOS and Windows VMs).
- The step keeps the branch in each of these cases (Linux, macOS and Windows
  VMs where not marked):
    - The branch holds a commit that no other ref reaches.
    - The branch list cannot be read, or its read is stopped (Linux VM, and the
      stop on macOS and Windows VMs). A configuration listing that answers
      `fatal: not a git repository` keeps the branch too (Linux and macOS VMs).
    - The repository has 10001 local branches or more. With 10000 the full check
      runs.
    - Another local branch has the same name in another letter case. claustrum
      folds case with Go's `strings.EqualFold`, also outside ASCII (claustrum's
      own).
    - The check of the commits fails or is stopped (Linux VM).
    - The delete fails or is stopped.
    - A list that does not parse, and a branch that is not listed but that
      `rev-parse --verify` does not report as absent (claustrum's own).
    - In a create rollback after a failed `config.worktree` step, the daemon
      reads the config of the repository again. A refusal there keeps the
      branch (`5fd08069`, Linux and macOS VMs).

#### Time limits of the branch step

These bounds are always on. They are not a divergence.

- One bound of 60 s covers the branch list and the commit check together. At the
  bound the running call is killed, with no SIGTERM first (Linux, macOS and
  Windows VMs). The bound starts before the configuration listing of
  `for-each-ref` and also covers `rev-parse` (claustrum's own). A stopped
  `rev-parse` is a failure and keeps the branch, also on Windows, where the kill
  gives exit code 1 (claustrum's own).
- `update-ref` gets SIGTERM 5 s after it starts, on Linux and macOS. A call that
  ignores it is killed 5 s later. On Windows it is killed at 5 s. That holds for
  a remove and for a create rollback (Linux and macOS VMs). The kill after
  SIGTERM in a rollback is claustrum's own.
- Only the git process gets the SIGTERM or the kill, not its children
  (claustrum's own).
- The `timeoutMs` of the caller does not stop the step.

#### Two branch steps at once

- Within one daemon, the steps of one repository run one at a time, also when
  one goes through a linked worktree. If two removes have branches that share a
  unique commit, one branch stays, and the second reply has
  `"branchKept":true` (Linux and macOS VMs). claustrum keys this lock by the
  common git dir, also in a rollback (claustrum's own).
- The steps of different repositories run at the same time.
- claustrum runs two removes of one `baseRepo` one after the other (claustrum's
  own).
- A remove deletes its worktree directory while the `worktree add` of a create
  in the same git directory runs. Its registration, branch and reply wait for
  the add (`5fd08069`, Linux and macOS VMs). They do not wait for the checkout
  (Linux VM).

#### The branch step in a remove reply

The remove frame never gains text from this step. Each case of the keep list
that a remove can reach adds `"branchKept":true`. A skipped name adds no member.

#### The branch step in an undo text

A create rollback that keeps the branch adds a part after `; and the undo could
not finish for <leaf>: `. If the branch holds a commit that no other ref
reaches, it also sets `"branchKept":true` after `errorCode`:

| outcome | part | member |
|---|---|---|
| the branch holds a commit that no other ref reaches | `the worktree itself was removed, but branch <b> was left in place: it now has commits no other branch or remote-tracking ref reaches. To keep that work, push the branch or rename it. To discard it, run git branch -D <b>. This branch name cannot be reused until the branch is renamed or deleted` (Linux, macOS and Windows VMs) | `"branchKept":true` |
| the same, after a failed rmdir of the leaf | `<rmdir text>; branch <b> was left in place: it now has commits …` with no `the worktree itself was removed, but ` (Linux and Windows VMs) | `"branchKept":true` |
| 10001 branches, a case twin, a list that does not parse, or a `rev-parse` that finds the branch (exit 0) | `branch <b> still exists; delete it by hand (for example: git branch -d <b>, which git refuses if that would lose commits) before retrying this branch name: could not check whether another branch or remote-tracking ref reaches its commits` (Linux, macOS and Windows VMs, but the list and `rev-parse` cases are claustrum's own) | none |
| a call of the check failed or was stopped | the same, then a space and the process error in parentheses, for example ` (exit status 128)` or ` (signal: killed)` (a failed `rev-parse` is claustrum's own) | none |
| after a failed `config.worktree` step, the config is refused at a second read | the same, with that refusal in parentheses (`5fd08069`, Linux and macOS VMs) | none |
| the delete failed or was stopped | `branch <b> still exists; delete it by hand (for example: git branch -D <b>) before retrying this branch name: <process error>`, for example `exit status 128` or `signal: terminated` (Linux and macOS VMs) | none |
| the same, with `refs/heads/<b>.lock` present | `… before retrying this branch name. A lock refs/heads/<b>.lock is also present: if another git process is running against this repository, the lock may be live and clears on its own; if none is, it is stale debris of the interrupted delete — remove the lock file by hand too: <process error>` | none |
| the name starts with `+`, so no call runs | `branch <b> left in place (its name is unsafe to pass to update-ref); delete it by hand` | none |
| the step deleted it (Linux, macOS and Windows VMs), or the branch is absent at the check | none | none |

- The dash in the lock text is the raw UTF-8 em dash U+2014.
- claustrum adds the lock part when the file `<common git dir>/refs/heads/<b>.lock`
  exists after the failure, also after a stop (claustrum's own).
- A name that starts with `-` gets the text of a `+` name (claustrum's own). A
  create does not reach it, because git refuses `-b -<name>` (host git, not
  measured).
- When claustrum's home or identity guard skips the leaf, the leaf stays. A kept
  branch then gets its text without `the worktree itself was removed, but `
  (claustrum's own).
- On Windows a stopped call ends with exit code 1. A Windows rollback text then
  ends `exit status 1` or ` (exit status 1)` where Linux and macOS show a signal
  (the Windows texts are not measured).
- A rollback that fails in two places, other than after a failed rmdir, joins
  the two parts with `; ` in the same order (claustrum's own).

## Daemon lifecycle (flags)

One binary, six modes: `-serve`, `-bridge`, `-stop`, `-version`, `-install` and
`-probe-cli`. `-install` and `-probe-cli` are on
[their own page](INSTALL.md).

### Flags and config keys

[DIVERGENCES.md → Conventions for opt-in
divergences](DIVERGENCES.md#conventions-for-opt-in-divergences) has the default,
the precedence of a flag and its key, and the bypass rule.

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
| `-cli-keep <n>` | none | `3` | version files of the cli-dir to retain on prune, the new CLI among them. The new CLI always stays. `0` and a negative value remove every version file but the new CLI | -install |
| none (config only) | `version-override` | none | `-version` stdout rebrand (CT-3) | -version |

Bool keys accept `true/1/yes/on` and `false/0/no/off`. Any other bool value, and a
negative or unparseable number, leaves the key unset, so the flag value or the
default stands.

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] \
          [-keep-children] [-listen-pipe] [-wire-log <p> [-wire-log-max-string <n>]] \
          [-max-extract-bytes <n>]
```

The binary self-daemonizes. It re-execs itself as a detached process, which
reparents to init, and that process runs the RPC server.
[Daemon startup](DAEMON.md#daemon-startup-serve) has what the start does, the ready line,
and the exit codes and texts of the launcher. Unless a rule here says otherwise,
it is measured against `89cb6289` on Linux and macOS. The flags under
[Flags of claustrum alone](#flags-of-claustrum-alone) have no reference run.
`process.spawn` has [the login-shell
PATH](protocol/process-spawn.md#the-login-shell-path-linux-and-macos) of the
children.

#### The token source

A token source is required. The detached daemon tests for it, not the launcher.
The daemon writes its reason to its own stderr, which is the
[daemon log](DAEMON.md#daemon-log-remote-serverlog), and exits. The launcher then gives up at
12 s, as under [The launcher wait](DAEMON.md#the-launcher-wait). This is deliberate parity.

- With neither flag the reason is
  `claustrum: daemonized child requires --token-file or --token-fd` (not
  measured).
- A zero-byte `-token-file` behaves the same way, with the reason
  `claustrum: token is empty`. The daemon removes the file first.
- A `-token-file` that cannot be read gives `claustrum: read --token-file: <err>`
  (from the code).
- The daemon reads the token as a line. It strips every trailing `\r` and `\n` (a single
  `\n` or `\r\n` is measured). It keeps other whitespace verbatim (from the code). It then removes the
  `-token-file`, as under [Authentication](#authentication).
- `-token-fd <n>` is claustrum-only. On Windows the start fails (from the code, not
  measured). On Linux and macOS the launcher reads the token from an
  already-open fd, where `0` is stdin, and forwards it to the daemon over an
  inherited pipe. So this handoff never touches disk. A failed read there gives
  `claustrum: read --token-fd: <err>` on the stderr of the launcher, with exit `1`
  (from the code). An empty token there is no read failure. The daemon then logs
  `claustrum: read --token-file: open : <err>`, and the launcher gives up at 12 s
  (from the code).

#### The re-exec marker

The re-exec marker is `CLAUSTRUM_DAEMON_CHILD`, not the reference's
`CLAUDE_SSH_DAEMON_CHILD`. [ARCHITECTURE.md](ARCHITECTURE.md) has the reason.
claustrum unsets its own marker before it spawns. The daemon still sets
`CLAUDE_SSH_DAEMON_CHILD=1` in its environment, so `process.spawn` children
inherit it as on the reference.

#### Flags of claustrum alone

These flags are off the wire. `-keep-children` and `-listen-pipe` have an entry in
[`DIVERGENCES.md`](DIVERGENCES.md). `-metrics-addr` and `-wire-log` have none:
- `-metrics-addr <a>` serves Prometheus counters at
  `http://<a>/metrics`, covering connections, spawns and exits, reattaches, and
  stream and stdin bytes. It is off by default, with no listener. It counts only,
  and it has no auth, so bind it to loopback. The daemon logs a bind failure
  (`[Server] metrics: …`), which is non-fatal.
- `-wire-log <p>` appends every JSON-RPC frame, in both directions, to
  `<p>` as JSONL. It observes already-marshaled bytes, so a daemon that logs
  emits frames byte-identical to one that does not. It is off by default, with no
  file and no work. `-wire-log-max-string <n>` bounds each string value. The
  default is 512, and `0` keeps whole payloads, which is needed to reconstruct a
  session from stream frames. Each record carries the frame as a decoded `body`,
  truncated per value, where field order is not significant. At
  `-wire-log-max-string=0` the record carries the frame as `raw` instead,
  verbatim, with the field order and number formatting of the wire. An
  unopenable path is fatal. [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md) has the redaction and
  the file mode.
- `-keep-children` is [CT-2](DIVERGENCES.md#ct-2), off by default. Without it, a
  graceful shutdown ends the children: the whole tree on Linux and macOS, the
  direct child on Windows. With it, the children stay alive across a restart,
  and the daemon logs `[Server] -keep-children: leaving <n> running child
  process(es) alive across shutdown`. A survivor loses its stdio. Its stdin
  reaches EOF, and a stdout or stderr write gets SIGPIPE, or EPIPE for a child
  that ignores SIGPIPE, such as Node. On Windows the flag keeps the whole tree
  alive after `-stop` and after `server.shutdown`, the direct child too
  (measured on Windows). The stdio of a survivor is not measured there.
- `-listen-pipe` is CT-5 and Windows-only. See [Named-pipe
  transport](#named-pipe-transport-windows-opt-in). The daemon logs a setup failure
  (`[Server] named-pipe transport: …`), which is non-fatal. The socket still serves.

The opt-in divergence of this mode on the wire is `-max-extract-bytes`
([D3](DIVERGENCES.md#d3)). Off is parity. Its frame is in
[files.extract_tar](protocol/files-extract-tar.md).

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

A simple relay. It is the thing an SSH session attaches to. It adds no auth. The
client that speaks through it supplies `"auth"` itself. It is strict, so a dial
failure is a hard error: `claustrum: dial server: <err>` on stderr, exit `1`.

At stdin EOF the bridge half-closes its socket write side, so the daemon reads
EOF, and the bridge keeps copying the socket to stdout. It relays the
parse-error frame of a final partial line, and then exits `0` (measured on Linux
and Windows). It prints every frame that arrived before the daemon closed the
connection, and it exits `0` also with stdin still open (measured on Linux). If the half-close fails, the claustrum bridge closes the socket fully.

On Windows the daemon can miss the half-close of the bridge (see
[Transport](#transport)). The bridge then does not exit.

### -stop — ask a running daemon to shut down

```text
claustrum -stop -socket <p>          # no token needed, and none is read
```

Unless a rule says otherwise, the rules of `-stop` are measured on Linux and
macOS, and the steps after a successful connect on Windows too. A rule marked claustrum's own is not measured.

`-stop` sends `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}` and a
newline. It sends no `auth` member, because that method is not authenticated
(see [Authentication](#authentication)). Then it reads once, with a 2 s deadline,
and discards what it reads. It never relays a frame to stdout. A live daemon's
reply does not always arrive (see [The reply races the close](protocol/server.md#the-reply-races-the-close)). A reply and an EOF lead to
the same next step. claustrum treats every other read result the same way
(claustrum's own).

`-stop` prints one word on stdout, then a newline, and exits `0` in every state
in this table:

| word | state |
|---|---|
| `stopped` | The connect succeeded. On Linux and macOS `daemon.lock` also came free within 3 s, or there is no usable `daemon.lock` (claustrum's own). On Windows the word comes at once. |
| `none` | The connect failed, and `daemon.lock` is free or missing. If the lock cannot be opened or locked, claustrum prints `none` too. |
| `terminated` | The connect failed, or the lock was still held 3 s after a successful connect. A live serve daemon of this binary for this socket holds `daemon.lock`. `-stop` sent `SIGTERM`, and the lock came free within 4 s. |
| `killed` | As `terminated`, but the lock was still held 4 s after `SIGTERM`. `-stop` sent `SIGKILL`, and the lock came free within 1.5 s. |
| `survivor` | A live process that is not that daemon holds `daemon.lock`, and `-stop` does not signal it. If the lock is still held 1.5 s after `SIGKILL`, the word is `survivor` too, and `-stop` does not wait longer. If a signal cannot reach a holder that is still there, claustrum prints it too. |

After a successful connect, on Linux and macOS, `-stop` tries `daemon.lock` every 50 ms for up
to 3 s. A lock that comes free gives `stopped` and no signal. A lock still held
after 3 s gets the signal steps of a failed connect. A holder whose record names
no daemon role gets no signal. `-stop` tries the lock for 0.5 s more, and then
prints `survivor`. A lock that comes free in that time gives `stopped`
(claustrum's own).

The lock check uses the holder test of the serve eviction. See [Run-dir
lock](DAEMON.md#run-dir-lock-daemonlock). On Linux the holder test of the reference is
not isolated (not measured). Before `SIGKILL`, `-stop` tests the holder again. A
lock that another process took during the `SIGTERM` wait gives `survivor`. The
new holder gets no signal (measured on Linux after a successful connect. After a
failed connect it is claustrum's own). Windows has no run-dir lock, so a
failed connect there always prints `none`. On macOS claustrum does not signal a
holder that is not a serve process, and prints `survivor`
([D15](DIVERGENCES.md#d15)).

The lock steps write these lines to stderr. claustrum adds its own level tag
before the reference text, as it does for the serve eviction lines:

```text
<ts> INFO  [daemon] stop: run dir is held by a live daemon, pid <N> (instance "<hex32>"); sending SIGTERM
<ts> INFO  [daemon] stop: previous daemon pid <N> (instance "<hex32>") exited after SIGTERM
<ts> WARN  [daemon] stop: WARNING previous daemon pid <N> (instance "<hex32>") ignored SIGTERM for 4s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)
<ts> WARN  [daemon] stop: WARNING <dir>/daemon.lock is held by a live process we will not signal (pid <N> is not a <basename> --serve process for <sock>); leaving it alone
<ts> WARN  [daemon] stop: WARNING previous daemon pid <N> (instance "<hex32>") survived SIGKILL (uninterruptible?); proceeding without run-dir ownership
```

A daemon that has not bound yet has a short record. That holder gives the first
two lines with `pid <N>` and no instance (measured on Linux). claustrum
writes the other lines with `(instance "")` then (claustrum's own).

A holder with no daemon role, after a successful connect, gives this line:

```text
<ts> WARN  [daemon] stop: WARNING <dir>/daemon.lock is held by a live process we will not signal (its holder is not a daemon (role "<role>")); leaving it alone
```

A lock that another daemon took during the `SIGTERM` wait gives this line, with
the pid and instance of the new holder:

```text
<ts> WARN  [daemon] stop: <dir>/daemon.lock changed hands during eviction (now pid <N> (instance "<id>")); leaving the new holder alone
```

This line is claustrum's own. It comes from the second holder test:

```text
<ts> WARN  [daemon] stop: pid <N> is no longer our serve holder before SIGKILL (<reason>); leaving it alone
```

`-stop` never creates or removes `daemon.lock`. It writes a record into it only
after a successful connect that led to `terminated` or `killed`. Then it writes
`{"pid":<pid>,"role":"stop","node":"<node>"}` and empties the file again before
it prints the word.

After a successful connect, `-stop` removes the socket and `daemon.token` only
after `terminated` or `killed` (from the code). A live daemon removes both files itself at its stop. After
a failed connect, `-stop` removes the socket path and the `daemon.token` beside
it, stale or not, unless the word is `survivor`. The socket unlink removes a
stale socket, a regular file and an empty directory. It keeps a non-empty
directory, and a path in a directory that the user cannot write.

The unlink does not check what the path is. So, unless the word is `survivor`, a
live foreign listener that refuses the connect with `EACCES` loses its socket
path, and a client that dials by path cannot reach it. A variant that removes
only a socket is under [Candidates considered but not
taken](DIVERGENCES.md#candidates-considered-but-not-taken).

`-version`, `-install` and `-probe-cli` win over `-stop`. `-bridge` and `-serve`
win too. With `-stop -bridge` claustrum runs the bridge, and with `-stop -serve`
it runs serve.

A daemon of a build before the auth exemption of `server.shutdown` keeps running
while `-stop` prints `stopped`. [IMPROVEMENTS.md](IMPROVEMENTS.md) item 37 has
the upgrade step.

### -version

```text
claustrum -version                   # → claustrum <id> (built <time>)
```

With `version-override` set in `claustrum.conf` ([CT-3](DIVERGENCES.md#ct-3)),
the output is:

```text
claustrum -version                   # → claude-ssh <sha> (via Claustrum <id>, built <time>)
```

The desktop client decides whether to upload the reference again from a
`<bin> --version` output that matches `/claude-ssh\s+(\S+)/`. The override
changes this stdout only. No JSON-RPC frame changes, and `server.capabilities`
still reports claustrum's own `<id>`.

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

See [-install](INSTALL.md#__install_result__-facts) for the facts schema and
[ARCHITECTURE.md](ARCHITECTURE.md) for the deployment lifecycle. See [`DIVERGENCES.md`](DIVERGENCES.md) for the full
divergence catalog and rules. See [EXAMPLES.md](EXAMPLES.md) for runnable
snippets.
