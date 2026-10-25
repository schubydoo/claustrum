# claustrum protocol reference

claustrum uses newline-delimited JSON-RPC 2.0 over an `AF_UNIX`
`SOCK_STREAM` socket. [Methods (20)](#methods-20) lists the page of each method.
The three `server.*` methods share one page. This
document holds the rules that more than one method shares, and the flags. The shared
git rules have their own pages: see [Shared rules of the git methods](#shared-rules-of-the-git-methods). The life
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
under [Stream notifications](#stream-notifications), is the wire contract.

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

### Error-string catalogue

The method-level error texts, verbatim. A text that only one method sends is on the
page of that method, and a row of the method links there. The method pages under
`docs/protocol/` give the trigger and the result shape. A text with no code and no
"in `error`" note is a JSON-RPC error `-32602`. "in `error`" is the `error` member of
the result. A row that says "in the frame of" a method, or "The same frames", is
`-32603` on `git.info`, `git.status` and `git.list_branches`. On `git.worktree_create`
and `git.worktree_remove` it is in `error`. A remove can wrap such a text in a longer
one, as the table in [Git-directory trust check](protocol/git-rules-trust.md#git-directory-trust-check) shows. A
row with no mark holds a text of the reference daemon, measured on Linux against a
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
| files.extract_tar | The texts that only this method sends | On the page: [files.extract_tar → Errors](protocol/files-extract-tar.md#errors) |
| git.status | The texts that only this method sends | On the page: [git.status → Errors](protocol/git-status.md#errors) |
| git.status / git.list_branches | `<go error>`, for example `exit status 128` | -32603. The git call failed |
| git.list_branches / git.info / git.status / git.worktree_create | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | -32603 on the other methods. Create puts it after the text of the stopped step, as `git worktree add failed: ` or the `mkdir_failed` text of the parent step. A stopped repository test gets it alone, with `worktree_add_failed`. While a request with `"shadowDirOff":true` runs, the text says `after 30 minutes` (claustrum's own). See [Time limits of git calls](protocol/git-rules-calls.md#time-limits-of-git-calls) |
| the four methods | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed`, after `git worktree add failed (checkout): ` for the checkout listing |
| the four methods | `this repository's git config cannot be used: git did not finish reading "<path>" within 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed` |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "<value>" is not a count` | in the frame of each method, when the daemon's own `GIT_CONFIG_COUNT` does not parse. See [The daemon's own git environment](protocol/git-rules-calls.md#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair <n> is incomplete` | in the frame of each method, when a pair below the daemon's `GIT_CONFIG_COUNT` is not set. See [The daemon's own git environment](protocol/git-rules-calls.md#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: <git text>` | in the frame of each method, for a `GIT_CONFIG_KEY_<n>` of the daemon that git cannot read. Linux, macOS and Windows. See [The daemon's own git environment](protocol/git-rules-calls.md#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` | Windows only. In the frame of `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`. The `path` of the first two, or the `baseRepo` of the last two, exists and git cannot start in it, for example a regular file. See [Git-directory trust check](protocol/git-rules-trust.md#git-directory-trust-check) |
| git.* | `git cannot run on this host; git not run: <exec error>: <git text>` | in the frame of each method, when the configuration listing fails and `git version` fails too. The exec error and the text are those of `git version`. Linux, macOS and Windows. It also follows a failed read of the test repository's config in [the start test](protocol/git-rules-settings-copy.md#the-start-test), and a failed [call about the repository](protocol/git-rules-settings-copy.md#the-call-about-the-repository), when `git version` fails too. For `git.worktree_remove` see the table in [Git-directory trust check](protocol/git-rules-trust.md#git-directory-trust-check) |
| git.* | `this repository's git config cannot be used: <detail>` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. The details are in [Repository config refusals](protocol/git-rules-config.md#repository-config-refusals) |
| git.* | `a file in this repository's .git directory cannot be used: <detail>` | The same frames. See [Repository config refusals](protocol/git-rules-config.md#repository-config-refusals) |
| git.* | `Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. See [The settings copy](protocol/git-rules-settings-copy.md#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>` | The same frames. The daemon cannot make the folder of the copy |
| git.* | `Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again` | The same frames. See [The settings copy](protocol/git-rules-settings-copy.md#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again` | The same frames. Measured on `git.status` only. From the code: a checkout puts it after `git worktree add failed (checkout): `. See [The settings copy](protocol/git-rules-settings-copy.md#the-settings-copy) |
| git.* | `git could not check this repository on the remote machine: at "<root>": exit status <n>` | The same frames, for a failed call about the repository. See [The call about the repository](protocol/git-rules-settings-copy.md#the-call-about-the-repository) |
| git.* | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` | The same frames, for a call about the repository that the daemon ended |
| git.* | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again` | The same frames, for a call about the repository that names another path |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` | in the frame of each method, for a configuration key over 1048576 bytes. Measured on `git.info`. See [Hardened git calls](protocol/git-rules-calls.md#hardened-git-calls) |
| git.* | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` / `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` | in the frame of each method, for more than 1024 hook names, or names over 65536 bytes in sum. Measured on `git.info`. See [Hardened git calls](protocol/git-rules-calls.md#hardened-git-calls) |
| git.* | `the repository's git directory could not be trusted; git not run: commondir not as git writes it: <S1, S2 or S3>` | in the frame of each method, for a stray `commondir`. [Git-directory trust check](protocol/git-rules-trust.md#git-directory-trust-check) gives the three texts |
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
| process.spawn | The texts that only this method sends | On the page: [process.spawn → Errors](protocol/process-spawn.md#errors) |
| launcher.resolve | The texts that only this method sends | On the page: [launcher.resolve → Errors](protocol/launcher-resolve.md#errors) |
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

The shared rules of the `git.*` methods are on four pages: [trust check](protocol/git-rules-trust.md), [config refusals](protocol/git-rules-config.md), [settings copy](protocol/git-rules-settings-copy.md) and [hardened calls](protocol/git-rules-calls.md).

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
