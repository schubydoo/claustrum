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

The method-level error texts, verbatim. A text that only `git.worktree_create` or
only `git.worktree_remove` sends is on the page of that method, and the row of the
method links there. The method pages under `docs/protocol/` give the trigger and
the result shape. A text with no code and no
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
| files.stat | `stat <path>: <reason>` | -32603 (any stat failure other than ENOENT) |
| files.read | `open <path>: <reason>` | -32603 (any open failure other than ENOENT) |
| files.read | `files.read: path is a directory` | |
| files.read | `files.read: file exceeds maxBytes` | |
| files.read | `files.read: not a regular file` | -32602. The path opens and is not a regular file. See [files.read](protocol/files-read.md) |
| files.list | `open …: no such file or directory` | -32603 (missing dir) |
| files.list | `open <p>: not a directory` | -32603. The path is not a directory |
| files.list | `open <p>: permission denied` | -32603 (an unreadable directory) |
| files.validate | `Path does not exist` | in `error` field, `valid:false` |
| files.extract_tar | `archivePath and destDir are required` | |
| files.extract_tar | `destDir must be an absolute, non-root path: …` | in `error` field, for a relative `destDir`. claustrum also refuses a file system root with it (not measured) |
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
| files.extract_tar | `mkdir parent <entry>: mkdirat <place>: file exists` | in `error` field. A file is at the place of the directory of a file entry. Match the prefix |
| files.extract_tar | `mkdir <entry>: mkdirat <place>: file exists` | in `error` field. A file is at the place of a directory entry |
| git.status | `baseRepo is required` | -32602 |
| git.status / git.list_branches | `<go error>`, for example `exit status 128` | -32603 (git failed, stdout parse) |
| git.list_branches / git.info / git.status / git.worktree_create | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | -32603 on the other methods. Create puts it after the text of the stopped step, as `git worktree add failed: ` or the `mkdir_failed` text of the parent step. A stopped repository test gets it alone, with `worktree_add_failed`. While a request with `"shadowDirOff":true` runs, the text says `after 30 minutes` (claustrum's own). See [Time limits of git calls](#time-limits-of-git-calls) |
| the four methods | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed`, after `git worktree add failed (checkout): ` for the checkout listing |
| the four methods | `this repository's git config cannot be used: git did not finish reading "<path>" within 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed` |
| git.status | `<file>: not a regular file` | -32603. claustrum's own text, from the copy into the temporary git folder after the gate passed. `<file>` is `info/sparse-checkout` when that entry is a folder or a FIFO. The reference is not measured there |
| git.status | `<file> is larger than <n> bytes` | -32603. claustrum's own text, for a copied file over its bound, also one that grew after the gate. `<n>` is 1073741824 for `index`, a `sharedindex.<40 or 64 hex>` file, `info/sparse-checkout` and a reftable file, and 1048576 for `HEAD` and `config.worktree`. The reference is not measured there |
| git.status | `<os error>` of the copy or of the temporary folder, for example `openat index: permission denied` or `stat <dir>: no such file or directory` | -32603. The raw Go error when a file of the entry cannot be copied, or a file of the temporary git folder cannot be made. With `"shadowDirOff":true`, and on a server with no run folder, a temporary folder that cannot be made answers the raw Go error too. The reference is not measured there. In the states `shadow` and `fallback-git` a `.gitdir-<n>` that cannot be made answers `{"isRepo":false,"clean":false}`: see [git.status → Errors](protocol/git-status.md#errors) |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "<value>" is not a count` | in the frame of each method, when the daemon's own `GIT_CONFIG_COUNT` does not parse. See [The daemon's own git environment](#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair <n> is incomplete` | in the frame of each method, when a pair below the daemon's `GIT_CONFIG_COUNT` is not set. See [The daemon's own git environment](#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: <git text>` | in the frame of each method, for a `GIT_CONFIG_KEY_<n>` of the daemon that git cannot read. A folder that holds no repository gets it too. Linux, macOS and Windows. See [The daemon's own git environment](#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` | Windows only. In the frame of `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`. The `path` of the first two, or the `baseRepo` of the last two, exists and git cannot start in it. That is a regular file, a file symlink or a folder with a path of 296 characters. The path `NUL` and a file name with one more dot at its end get it too. Only `git.info` is measured for the symlink, the long folder, `NUL` and the dot. See [Git-directory trust check](#git-directory-trust-check) |
| git.* | `git cannot run on this host; git not run: <exec error>: <git text>` | in the frame of each method, when the configuration listing fails and `git version` fails too. The exec error and the text are those of `git version`. Linux, macOS and Windows. It also follows a failed call 7 of [the start test](#the-start-test), and a failed [call about the repository](#the-call-about-the-repository), when `git version` fails too. For `git.worktree_remove` see the table in [Git-directory trust check](#git-directory-trust-check) |
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
| process.spawn | `Process ID is required` / `Command is required` | |
| process.spawn | `command must be an absolute path when a launcher is given` | with a `launcher` param |
| process.spawn | `the managed launcher cannot be used: <reason>` | -32004. The reasons are the argv[0] and no-launcher [texts of launcher.resolve](protocol/launcher-resolve.md#unusable-reasons), and `a launcher cannot be applied on a Windows host` |
| process.spawn | `the managed launcher <path> could not be started: <reason>` | -32005 |
| launcher.resolve | `cliPath is required` | `{}`, `null` params, a `null` or empty `cliPath`, or a wrong key |
| process.stdin | `Invalid base64 data` / `Process not found` / `Process not running` | [process.stdin → Errors](protocol/process-stdin.md#errors) gives the order of the tests |
| process.stdin | `stdin offset gap: offset ahead of applied bytes` | -32003 |
| process.stdin | `stdin backpressure: queue full` | -32002 (queue full, ~16 MiB) |
| process.killAndWait / process.reattach | `Process ID is required` / `Invalid params` | |

`-install` reports failures inside the `__INSTALL_RESULT__` facts line as
`cliError` strings, not through an exit code. They are catalogued in the
`-install` section below.

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

## Shared rules of the git methods

This section holds the rules that more than one `git.*` method uses.

### Git-directory trust check

Every `git.*` method checks the git directory of the folder that it runs git in,
before git runs there. That folder is `path` for `git.info` and
`git.list_branches`. It is `baseRepo` for `git.status`, `git.worktree_create` and
`git.worktree_remove`. The check has three outcomes. "Trusted" lets git run. "No
repository" answers as if the folder held no repository. "Refused" answers with
one of the texts M2 to M5 and S1 to S3, and git does not run.

An unmarked rule is measured against `f6010b97` on a Linux VM. Where the case can
exist, macOS and Windows VMs measured it too. Three groups of rules are measured
against `89cb6289` on Linux, macOS and Windows VMs. They are a stray `commondir`,
a failed configuration listing and a folder where git cannot start. A rule that
names one system or another build was measured there.

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
  gives "no repository", and each git call carries `GIT_DIR=NUL`. `89cb6289` does
  the same on a Windows VM.
- From the code: on Windows the resolve of a folder path can fail, for example at
  a missing name before `..`. The walk then starts at the spelled path too.
  Only `git.info` reaches that start. `git.list_branches` and `git.status` resolve
  the path first, and so do `git.worktree_create` and `git.worktree_remove` for a
  `baseRepo` that exists. If that resolve fails, they answer there.
- On Windows a refusal of a config on `git.info` follows the spelled start too
  (`89cb6289`, Windows VM). From the code: in the state `shadow` without
  `"shadowDirOff":true`, it is the class 1 text of
  [Repository config refusals](#repository-config-refusals). A config that git
  refuses and the daemon's read accepts, such as an `include.path` with no value,
  gets the hooks refusal. So does any refused config outside that state.
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
starts with `refs/`. Any other `HEAD` fails at once. A FIFO `HEAD` is one example,
and git itself blocks on it.

#### How the daemon judges the git directory

The daemon then judges the git directory G:

- G is a linked-worktree entry when its parent is named `worktrees`, its own name
  is not `.git`, and its grandparent passes the git-directory test. The entries of
  a main repository, a bare repository and a submodule all count.
- G is not an entry and holds no `commondir`: trusted. This is a normal main, bare
  or submodule git directory.
- G is not an entry and holds a `commondir`: the rules of a stray `commondir`
  judge it. Git writes that file only in an entry. An entry whose grandparent
  fails the test is not an entry. So its honest `commondir` (`../..`) gets S1.
  That follows from the rules and is not measured on `89cb6289`.
- G is an entry without `commondir`: M3.
- G is an entry whose `commondir` is not a regular file of at most 1048576 bytes:
  M4. A symlink is followed only when it is relative and stays inside the entry.
  Exactly 1 MiB passes.
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
  passes the check, although git itself rejects that file. The frames that follow
  come from git, so they depend on the git version.
- The pin also keeps git from walking past a trusted nested `.git`. A nested
  `.git` whose `objects` is a regular file therefore answers "not a repository"
  from git on Linux and macOS. Git for Windows accepts that layout and answers for
  the nested repository.
- An entry folder that is gone means "no repository". On Linux and macOS an entry
  replaced by a regular file means the same. On Windows it gets M3.
- A `.git` file that names a plain folder, another repository's git directory or
  the main git directory is not refused. Git answers for itself there.
- On Windows the daemon does not expand 8.3 short names in G. A `.git` file or a
  daemon `GIT_DIR` can name an entry as `…\GIT~1\WORKTR~1\<name>`. That G is not
  an entry, because its parent is not named `worktrees`. So its `commondir` gets
  S1 for the content `../..`. The S1 text there is not measured on `89cb6289`.
- On Windows the daemon resolves G when a component of G is a symlink. Else it
  keeps G as spelled, also with a junction before its last component. For a
  junction `<KP>` to a repository, the pin is `<KP>\.git`, as on `89cb6289`
  (Windows VM).

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
   `cannot list the repository's worktrees: exit status 128` and deletes nothing.
   Without `worktreeRoot` it answers `{"success":true,"branchKept":true}`. It
   deletes the worktree folder and its entry, and the branch stays. If the folder
   is gone before the request, it deletes the entry. These remove rules are
   measured on a Linux VM. When G fails the git-directory test, that symlink gets
   S3 (Linux VM).
2. A `commondir` that is not a regular file of at most 1048576 bytes gets S2 with
   its reason. A relative symlink that stays in G is followed. A dangling symlink
   that is absolute, or that leaves G, gets S2 with `openat commondir: path
   escapes from parent` (Linux VM).
3. The content with every trailing CR and LF removed is exactly `.` or `./`. A
   space or a tab is not trimmed. Any other content gets S1, which quotes it. So
   ` .`, `. `, `.<TAB>`, `./x`, `..`, an empty file, `.\` and the absolute path of
   G itself are all refused.
4. G then passes the git-directory test, or it gets S3. The content test comes
   first. When both S2 and S3 apply, which one answers is not measured.
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
  daemon's environment sets `GIT_COMMON_DIR`. A relative value is first joined to
  the request folder. On Windows `/dev/null` names nothing, so it becomes
  `<request dir>\dev\null`. Measured on Linux and Windows VMs.
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
listing in that folder. In a plain folder it carries `GIT_DIR=<null>`. The frames
are then those of the "no repository" column. A listing that fails by class 3
below gives the "refused" column. `89cb6289` answers so for a plain folder and for
a folder with an empty `.git` folder, on Linux, macOS and Windows VMs.
`git.worktree_remove` with `worktreeRoot` is measured on Linux and macOS VMs only,
because Windows refuses every `worktreeRoot` first. See
[The daemon's own git environment](#the-daemons-own-git-environment).

A daemon `GIT_DIR` that names a regular file differs by system. On Linux and macOS
no listing runs, and the "no repository" column answers. `89cb6289` answers so
for `git.info` on a plain folder and on a repository, also with an empty
`GIT_CONFIG_KEY_0` (Linux and macOS VMs). Other methods are from the code. On
Windows a file with no `gitdir:` line gets the refusal of a config on the five methods: `this repository's git config
cannot be used: open <GIT_DIR>: not a directory` (`5fd08069`, Windows VM). With a
git older than 2.56, a `.git` file to a gone git dir keeps the "no repository"
frame of `git.info` (Linux and macOS VMs).

Two more states are not measured, so these rules are from the code. The first is
a daemon `GIT_COMMON_DIR`, where a listing gets no `GIT_DIR=<null>`. The second is
a `.git` file that names a regular file, on Linux and macOS. In both states
`git.worktree_create` runs no listing in a folder where the trust check finds no
repository, and it keeps the "no repository" column. `git.worktree_remove` keeps
that column unless its listing fails. In the second state `git.info` and
`git.list_branches` run no listing either, and they keep that column. With a
daemon `GIT_COMMON_DIR` those two methods skip the trust check, and the listing
classes below answer. In both states the listing of `git.status` still carries
`GIT_DIR=<null>`. A listing that fails there is the refusal of `git.status`, as in
a plain folder.

#### A failed configuration listing

When the configuration listing fails, the daemon sorts the failure into a class.
The classes come in this order:

1. No `git` on `PATH`: "no repository", and no process starts.
   `git.list_branches`, `git.status` and `git.worktree_create` answer their "no
   repository" shapes, the create with and without `worktreeRoot`.
   `git.worktree_remove` without `worktreeRoot` answers
   `{"success":true,"branchKept":true}` for a gone worktree with a `branchName`,
   and the lock-check refusal for a worktree directory that exists. With
   `worktreeRoot` and a `baseRepo` that exists, it answers the work-tree refusal
   with the exec error alone: `exec: "git": executable file not found in $PATH`.
   That answer comes before the trust check. A `baseRepo` that does not exist gets
   no work-tree refusal (see
   [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks)).
   The methods other than `git.info` are measured on a Linux VM. In claustrum a
   refused daemon `GIT_CONFIG_COUNT` answers before the exec error when `stat`
   reads `baseRepo` (not measured).
2. The listing exits 128, and its stderr starts with `fatal: not a git
   repository`. Leading white space is dropped first, and letter case does not
   count. The answer is "no repository". Exit 1 with that text is not this class.
   With a `branchName`, `git.worktree_remove` without `worktreeRoot` runs a second
   listing for the branch step and keeps the branch. A gone worktree then answers
   `{"success":true,"branchKept":true}`. For a `.git` file to a gone git dir, git
   2.56 says `gitfile does not point to a valid repository` instead. A gone entry
   under `.git/worktrees` then answers "no repository" on each method but
   `git.worktree_remove`. Another gone git dir answers the error of
   [the call about the repository](#the-call-about-the-repository) in the folder
   that holds `.git`. With `"shadowDirOff":true` that git dir gets the hooks
   refusal on every method (Linux VM). The other git 2.56 rules are measured on
   `5fd08069`, on Linux and Windows VMs.
3. Any other failure, a failed start included. The daemon runs `git version` with
   no `-c` option. It runs in `/` on Linux and macOS, and in the daemon's working
   folder on Windows (that folder is from the code). If that call fails too, the
   text is `git cannot run on this host; git not run: <exec error>: <git text>`.
   Else it is the hooks refusal.

The `git version` call of class 3 gets no `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>`,
`GIT_CONFIG_VALUE_<n>` or `GIT_CONFIG_GLOBAL` of the daemon. claustrum passes the
daemon's `GIT_CONFIG_SYSTEM` and `GIT_CONFIG_NOSYSTEM` on (not measured).
claustrum matches these names by their exact upper-case letters. On Windows a
lower-case name, such as `git_config_global`, stays on the call (from the code).
`git version` does not get the daemon's own `LC_ALL` and `LANGUAGE` back
(claustrum's own, not measured). With `worktreeRoot`, when git cannot start in
`baseRepo`, `git version` gets the light environment (Linux VM).

With `"shadowDirOff":true` the `git version` call has the six heavy values for
`git.info` and the four light values for `git.worktree_create`. `5fd08069` does
so on Linux, macOS and Windows VMs. Without the member the call of a create
carries six values (`5fd08069`, Linux VM). From the code: claustrum sends those
six in the state `shadow`.

#### Where git cannot start

On Linux and macOS git sometimes cannot start in the folder itself. That is a
file, or a folder that the daemon cannot search (mode 0600 or 0000). The listing
fails at the chdir. That is no hostile configuration, and the method goes on. The
daemon still runs `git version` there, and the frames do not change. A regular
file gets the "no repository" frames of `git.info`, `git.list_branches`,
`git.status` and `git.worktree_create`, also with an empty `GIT_CONFIG_KEY_0`. A
folder of mode 0000 gets the same frames for the last three methods, as a plain
folder and as a repository (Linux VM).

If that `git version` fails too, the method answers `git cannot run on this host;
git not run: <exec error>: <git text>` with the detail of the version call:

- `git.info` on a regular file and on a folder of mode 0000 (Linux and macOS VMs).
- `git.list_branches`, `git.status` and `git.worktree_create` on a regular file.
  The create has the text in `error`, with and without `worktreeRoot` (Linux and
  macOS VMs).
- The same three methods on a folder of mode 0000, a plain folder and a
  repository (Linux VM).

A path that does not exist keeps its frames there (Linux and macOS VMs). On a
folder of mode 0000 `git.worktree_remove` without `worktreeRoot` answers `failed
to remove worktree: open <baseRepo>: permission denied` (Linux VM).

On Windows a `git version` that fails gives the "cannot run" text too. That holds
on a plain folder for `git.info`, `git.list_branches`, `git.status` and
`git.worktree_create`. It holds on a repository and on a regular file for
`git.info`. `git.info` on a path that does not exist keeps its frame.

On Windows git cannot start in some paths that exist. Such a path is a refusal by
class 3. The start error of the listing is `fork/exec <git.exe path>: The directory name
is invalid.`. `<text>` is the hooks refusal with that error, and `<git.exe path>`
is the git that `PATH` resolves. `89cb6289` and claustrum answer so on a Windows
VM:

| request | frame |
|---|---|
| `git.info`, `path` is a regular file in a plain folder or in a repository | `{"error":{"code":-32603,"message":<text>}}` |
| `git.info`, `path` is a file symlink, `NUL`, or the name of a regular file with one more dot at its end | the same |
| `git.info`, `path` is a folder with a full path of 296 characters | the same |
| `git.list_branches`, `path` is a regular file | the same |
| `git.status`, `baseRepo` is a regular file, and `path` is that file or a repository | the same |
| `git.worktree_create`, `baseRepo` is a regular file | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}` |
| the `git.info` request on a regular file, with a `git version` that exits 1 and writes `boom` | `{"error":{"code":-32603,"message":"git cannot run on this host; git not run: exit status 1: boom"}}` |

Nothing changes on disk there. Other frames stay as they are on a Windows VM.
`git.status` with the file as `path` and a repository as `baseRepo` answers
`{"isRepo":false,"clean":false}`. `git.worktree_remove` with a regular-file
`baseRepo` answers `failed to remove worktree: open <baseRepo>: not a directory`,
also with an empty `GIT_CONFIG_KEY_0`. `git.info` answers
`{"isRepo":false,"repoSlug":"","defaultBranch":""}` for a path that does not
exist, a path under a regular file and a junction whose target is gone. With
`GIT_CONFIG_COUNT=x` the count refusal answers the regular file. From the code: if
the listing does not start in a path, and `stat` reads the path as not a folder,
claustrum refuses. For the folder with a path of 296 characters, the trust check
finds no repository, and the listing does not start there.

A folder can have an access entry that denies read data and traverse to the user
of the daemon. It keeps the `git.info` frame of a plain folder. With a daemon token
that has no privilege, `89cb6289` and claustrum refuse there. The text is the
hooks refusal with `exit status 128: fatal: unable to access 'NUL/config':
Permission denied`. The folder beside it with no deny entry keeps its frame under
that token (Windows VM).

#### The "cannot run" and hooks refusal texts

The "cannot run" text carries the exec error and the stderr of `git version`, not
those of the listing. Take a listing that exits 128 on an empty
`GIT_CONFIG_KEY_0`, and a stub `git version` that exits 1. `89cb6289` then answers
`git cannot run on this host; git not run: exit status 1: <stderr of git version>`.
A plain folder carries the same text on Linux, macOS and Windows VMs. So does
`git.worktree_remove` with `worktreeRoot` on Linux and macOS VMs. claustrum makes
`<git text>` with the text rule below. From the code: each listing of a git method that answers this text takes it
from the version call.

The hooks refusal is `config-defined hooks could not be pinned off; git not run:
listing the configuration in force: <exec error>: <git text>`. claustrum makes
`<git text>` with the text rule of `git.worktree_create`. The rule takes git's raw
stderr and keeps its first 512 bytes. It drops invalid UTF-8, turns each
non-printable rune into one space, and then trims spaces. If nothing is left, the
text is the error of the git process, for example `exit status 128`. The cap comes
before the trim, so leading white space counts toward the 512 bytes. A tab becomes
one space. A stub git on a Windows VM measured this rule.

With the `GIT_COMMON_DIR` pin, git names a corrupt config by its absolute path.
That holds in the frames of all five methods (Linux and macOS VMs). On claustrum a
syntax error in a config file of the repository gets the class 1 text first: see
[Repository config refusals](#repository-config-refusals).

### A FIFO where the daemon reads a git file

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

### Repository config refusals

The daemon reads the git config of a repository itself, and it tests some files of
the git directory. If it does not accept one of them, the git method answers with a
refusal. A rule that is not marked "claustrum's own" or "from the code" is measured
against `5fd08069` on a Linux VM. A rule that names macOS or Windows was measured there.

The read runs only in the state `shadow` of [the settings copy](#the-settings-copy).
In the state `fallback-git` the daemon makes no read and gives no refusal. On git
2.11.4 and 2.12.5 `5fd08069` is in that state and gives no refusal. Git 2.13.7 is
the oldest git that ran with the refusal. claustrum is not measured on those gits.

A refusal is `<class>: <detail>`, shown as `<text>` in these frames. Most rules were
measured on `git.info` and `git.status`. Four states ran on all five methods. They
are format version `2`, a config of 20 MiB, an `info/exclude` of 20 MiB and a
`shallow` file with a text line.

| Method | Answer |
|---|---|
| `git.info`, `git.status`, `git.list_branches` | `{"jsonrpc":"2.0","id":N,"error":{"code":-32603,"message":"<text>"}}` |
| `git.worktree_create` | `{"result":{"success":false,"error":"<text>","errorCode":"worktree_add_failed"}}`. Nothing is made |
| `git.worktree_remove` | `{"result":{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked: <text>"}}`. The worktree stays, and no branch is deleted |
| `git.worktree_remove` with `worktreeRoot` | `{"result":{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}}`. Nothing is removed |

On `git.worktree_remove` the refusal also comes for a worktree folder that does not
exist, and the branch `branchName` stays. That holds on Linux, macOS and Windows VMs.
Linux and Windows VMs ran it in a bare repository too. With `worktreeRoot` the
refusal comes for a worktree that exists and for a missing one (Linux and macOS VMs).

#### Paths in the texts

A path in quotes uses the separators of the system, and Go's
`%q` quotes it. A path of more than 300 characters is cut to its first 300
characters, with no mark of the cut. On Windows each backslash is doubled inside the
quotes, and the JSON encoding doubles it again. The path of the `open <file>:` text is not in quotes, and it is not cut. The `<worktreePath>` of the
remove text is as sent, and it is not cut.

On Linux and macOS the path of the config is the resolved path. A request through a
symbolic link gets the real path. On Windows a request through a junction keeps the
spelling of the junction, and a request in another letter case gets the real spelling.

The path of an included file is the folder of the file that holds the setting, one
separator, and the value as written. The Windows `is outside` text is the exception:
it names the cleaned path. On Windows `path = <dir>/inc.cfg` gives
`<git dir>\<dir>/inc.cfg`. The folder of the holding file stays as written too. With
`include.path = a/../b/inc.cfg`, a `path = x.cfg` in that file names
`<git dir>/a/../b/x.cfg` (Linux and macOS VMs).

#### Turning the read off

`"shadowDirOff": true` in the request turns the read off for that
request, on the five methods. A git request that starts while it runs has the switch
too. The request then gets the answer that it gets with no read. A FIFO as the config
then gets no answer in 10 s (Linux and macOS VMs). The member name matches without
regard to letter case, so `"SHADOWDIROFF": true` is the switch too. The values
`"true"`, `1`, `null` and `false` act as no member. The switch, its letter case and
these values hold on Linux, macOS and Windows VMs. A request that starts while it
runs ran on a Linux VM. With `worktreeRoot` and the switch, a remove of a worktree with a
config of 20 MiB removes the worktree (macOS VM).

#### How the daemon reads a config

The daemon gives each config file to git on standard input, with one
call for each file: `git config --no-includes --file - --list -z`. git follows no
include there. The daemon follows each `include.path` setting itself. If the request
names a linked worktree, the daemon reads the config of the repository of that
worktree. With a daemon `GIT_COMMON_DIR`, `git.info` and `git.list_branches` read
nothing (claustrum's own).

From the code: the call gets no `GIT_CONFIG` and no `GIT_CONFIG_PARAMETERS` of the
daemon, and it runs with `GIT_DIR=<null device>`, `LC_ALL=C` and `LANGUAGE=C`. On Linux and macOS the
daemon opens each file of the read with no controlling terminal. A config can be a
link to a pty slave.

With a config that is a link to a pty slave, `5fd08069` stays up after the pty
master closes (Linux VM).

#### The kept verdict

A verdict with a refusal is not used again. Each later request reads
again, as on `5fd08069` (Linux, macOS and Windows VMs). claustrum keeps the verdict of
a read that accepted the config, for 256 git directories at most. `5fd08069` also
reads an unchanged accepted config only once. If each file of that read is unchanged,
a later request starts no config call. An unchanged file has the same size,
modification time and mode, and the same identity where the system gives one. A file
that was missing must still be missing. This test and the number 256 are claustrum's
own.

From the code: the identity test is Go's `os.SameFile`. On Linux and macOS that is the
device and the inode. This page claims no identity test on Windows. The test misses a
rewrite in place that keeps the size inside one step of the file time. It also misses
a change during the read. A path that the daemon cannot examine has the state of a
missing file.

After a failed open or read of the daemon, claustrum keeps no verdict of that read. So
a refusal with an `open <file>:` text is read again at the next request. An error of
the daemon itself, such as too many open files, then answers one request only. That
rule is claustrum's own. The tests of `shallow`, `info/attributes` and `info/exclude`
run in each request, and they are no part of the kept read.

#### Class 1: the git config

Class 1 is `this repository's git config cannot be used: <detail>`. The format
version and the extensions of an included file are not tested.

| Detail | When |
|---|---|
| `the repository uses format version "<v>", which this version of Claude does not support` | The last value of `core.repositoryformatversion`, as git lists it, is not exactly the text `0` or `1`. A config with no such key is not refused. `<v>` is cut to its first 20 characters, with no mark (Linux, macOS and Windows VMs) |
| `the repository uses git extension "<name>", or a value of it, that this version of Claude does not support` | The format version is `1`, and an `extensions.<name>` setting is refused (see "Extensions" below). `<name>` is in lower case. It is cut to its first 60 characters, with no mark (Linux, macOS and Windows VMs) |
| `"<file>" includes "<value>", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full` | An include value of the form `~user/…` or `%(prefix)/…`, in an `include` section or in an `includeIf` section. The condition of the `includeIf` section does not have to match. `<file>` is the file that holds the setting. `<value>` is cut to its first 100 characters (Linux and macOS VMs) |
| `its includes go past what Claude follows (16 files, nested 3 deep)` | More than 16 included files, or an include chain of more than 3 levels below the config. A missing file counts, and so does the file of an `includeIf` section, also a missing one. A file that includes itself gets this text too |
| `"<file>" is not a regular file` | The config or an included file is a FIFO or a directory. A config that is a symbolic link to the null device gets it. So does an included file that is a symbolic link to a FIFO. `include.path = ~` gets it with the path of the home folder. So does a `config.worktree` of the git directory with `extensions.worktreeConfig` on (Linux and macOS VMs). The daemon's own read does not wait on a FIFO |
| `"<file>" is larger than 8 MiB` | The config, an included file or that `config.worktree` has more than 8388608 bytes |
| `"<file>" has a setting in section "<section>" whose name is longer than 1024 bytes` | The full name of a setting, with its section and subsection, has more than 1024 bytes. The sections are `core`, `remote`, `lfs`, `url`, `filter`, `extensions`, `include`, `includeif`, `index`, `feature` and `checkout`. The last three are measured on macOS and Windows VMs too. `<section>` is in lower case, without the subsection. The bound holds in an included file, with the path of that file |
| `"<file>" has more settings than Claude reads (8 MiB); remove repeated ones` | See "The count of settings" below |
| `open <file>: <reason>` | The daemon cannot open the config, an included file or that `config.worktree`. A file of mode 000 gives `permission denied`. A socket gives `no such device or address` on Linux. As the config, a socket gives `operation not supported on socket` on macOS |
| `<error lines of git>` | The listing of the config exits non-zero while a child holds its stderr. The lines read until then are joined by one space. One `git version` call follows (Linux, macOS and Windows VMs) |
| `fatal: bad config line <n> in "<file>"` | The config or an included file has a syntax error. So does the `config.worktree` file of the git directory with `extensions.worktreeConfig` on. This text comes before an extension refusal. The text is that of git, with the path in quotes |
| `"<included file>" is outside "<repo>", and on Windows Claude reads a repository's settings only from inside it; if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead` | Windows: a relative include that leaves the repository. For a bare repository `<repo>` is its git directory |
| `"<file>" includes "<value>", and on Windows Claude follows an include only if its path is relative and stays inside the repository; if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead` | Windows: an absolute include value, also to a missing file, `~/x` and `~root/x`. An absolute value and `~/x` get it also in an `includeIf` section whose condition does not match. `<value>` is cut to its first 300 characters |
| `"<file>": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to` | Windows: the config is a file link. Also Windows, on every git method that reads the config: the git directory is a junction or a directory symbolic link, and `<file>` is `<git dir>\config` |

No refusal comes from a config that is missing, empty, a dangling
symbolic link or a symbolic link to a regular file. A NUL byte in a value, an empty
`include.path` and one with no value give none either. On Linux the daemon follows a
relative include, also one that leaves the git directory. It also follows an absolute
include, and `~/x` with a home folder.

Two more kinds of file get the kind test and the size test, with their own path in
the text. A format version `2` in such a file is not refused.

- The file of an `includeIf` section, whatever its condition is (Linux, macOS and
  Windows VMs). Its filter settings count for the filter rule of the checkout.
- The `config.worktree` file of a linked worktree. If the config turns
  `extensions.worktreeConfig` on, that file gets the tests. `true`, `yes`, `on` and
  `1` turn it on, and so do `TRUE`, `ON`, `Yes`, `On` and `tRuE`. So
  does the key with no value. `false`, `no` and `0` do not (Linux, macOS
  and Windows VMs). The trust check of the request finds that linked worktree. On
  `git.status` it is the worktree that `baseRepo` names. All five methods ran, and
  `git.worktree_remove` on a Linux VM only.

#### Extensions

Under format version `1` the daemon refuses an extension other than
`worktreeConfig` in two cases. [The copied settings file](#the-copied-settings-file)
has no rule for its key, or the rule does not accept its value. Refused are
`preciousObjects = 1234567890`, `objectFormat = sha512`, `refStorage` with no value and
`partialClone = a/-b`. Not refused are `worktreeConfig = maybe`, `objectFormat = SHA256`, `refStorage = FILES`,
`preciousObjects = yes` and `preciousObjects = 1` (Linux, macOS and Windows VMs). An
extension in a config with no format version key is not refused. Under version `1`
the rules accept `objectFormat = sha256` and `refStorage = reftable`, so a sha256 or
a reftable repository is not refused. A value that git itself refuses but the copy
leaves out, such as `extensions.objectformat = md5` under version `0`, gets no
refusal of the daemon. The method answers as git does.

Four extension states are measured on `5fd08069` but not on a claustrum build with
the settings copy. They are `worktreeConfig = maybe` and `objectFormat = SHA256` under
version `1`, `refStorage = FILES`, and `objectFormat = md5` with no version key. For
`objectFormat = SHA256` the `git.status` of `5fd08069` answers `exit status 128`.

#### The count of settings

Each setting of a counted section costs the bytes of its
full name, the bytes of its value, and 96. The config has its own sum, and the
included files have one sum between them. The file in which a sum passes 8388608 is
refused. The counted sections are `core`, `remote`, `extensions`, `include`, `branch`,
`lfs`, `index`, `url`, `filter`, `feature`, `checkout` and `includeif`. The sections
`x` and `user` do not count. This arithmetic is fitted to the measured bounds on Linux,
macOS and Windows VMs. The exact pairs are measured in `core` and `remote` on a Linux
VM.

#### Class 2: files of the git directory

Class 2 is `a file in this repository's .git directory cannot be used: <detail>`.
The files are `shallow`, `info/attributes` and `info/exclude`, and the git directory
itself.

| Detail | When |
|---|---|
| `"<file>" is not a regular file` | `info/attributes` or `info/exclude` is a FIFO or a directory. A symbolic link there passes, also one to the null device. `shallow` is a FIFO, a directory or a symbolic link to the null device. The daemon does not wait on a FIFO |
| `"<file>": it is larger than 4 MiB` | `info/attributes` or `info/exclude` has more than 4194304 bytes |
| `"<file>" is larger than 4 MiB` | `shallow` has more than 4194304 bytes |
| `open <git dir>: not a directory` | Windows: a daemon `GIT_DIR=.git` names the `.git` file of a linked worktree, on the five methods |
| `open <file>: <reason>` | `shallow` has mode 000 (`permission denied`) or is a socket (`no such device or address`). The path is not in quotes. An `info/attributes` or `info/exclude` of mode 000 is not refused |
| `"<file>" holds something other than object ids` | A line of `shallow` is not 40 or 64 hex characters in lower case. That rule is fitted to the measured states. A blank line, an id with a space after it and a file of one line end are refused. A line end of CRLF, a last line with no newline and an empty file pass |

#### Which answer comes first

A refused config comes first, then a refused `config.worktree` file of a linked
worktree, then a refused file of the git directory (from the code for the middle step). In class 2, `shallow` comes before `info/attributes` (Linux, macOS and
Windows VMs), and `info/attributes` comes before `info/exclude`.

#### A folder that is not a repository

A folder with no repository keeps its "not a repository" answers.
That holds for a `HEAD` that is no ref and no object id, also with format version `2`.
It also holds for a repository of another owner with no `safe.directory` (Linux, macOS
and Windows VMs). The call about the repository in
[the settings copy](#the-settings-copy) decides that state, before the read. If git
answers it with exit 128 and a no-repository text, the method gives its "not a
repository" answer and reads no config. git's text for another owner is
`detected dubious ownership in repository`. Such an answer is not kept, so a new
`safe.directory` line counts at the next request (Linux and macOS VMs). After a change
of the owner inside the 5 seconds of a kept good answer, a request answers normally.
The first request after that time answers "not a repository".

#### claustrum's own choices

These rules are from the code and are not measured on `5fd08069`:

- `extensions.worktreeConfig` starts the `config.worktree` test with values that no
  run had, such as `oN` or `007`. The last such setting of the config counts.
- Under format version `1` the value rule refuses extension values that no run had,
  such as `preciousObjects = -1`.
- The cuts of the format version value and of the extension name count characters.
  The measured texts are ASCII.
- The `config.worktree` open error and size bound hold on `git.status`,
  `git.list_branches` and `git.worktree_remove` too, and every kind on Windows.
- The name bound and the count of settings are tested on a whole file before an
  include of that file is followed. So 200000 include lines of a missing file get the
  "more settings" text, as measured.
- The two include limits come before the open of an included file.
- An open error of the file of an `includeIf` section gives no refusal.
- An open error other than the measured ones gets the same `open <file>: <reason>`
  form.
- A `~/x` include is joined to the home folder and cleaned. With no home folder it
  gets the "expands neither" refusal. No daemon starts with no home folder.
- On Windows a `%(prefix)/…` value gets the "expands neither" text, with the cut at
  100 characters.
- A refusal of the trust check and of the daemon's own `GIT_CONFIG_COUNT` comes before
  a refusal of the read. On `git.worktree_remove` with `worktreeRoot` the read comes
  before the configuration listing.
- The listing that the read takes from git for one config file has a bound of 64 MiB.
  A long subsection name with many keys lists as far more bytes than the file has.
  The read tests the name bound and the count on the first 64 MiB of such a listing.
  If neither refuses, the read gives no verdict.
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
that copy, so git reads the copy and not the config of the repository. A rule here
that is not marked "claustrum's own" or "from the code" is measured against
`5fd08069` on a Linux VM. A rule that names macOS or Windows was measured on that
system.

#### The states and `settingsControl`

`git.info` reports the state in its last member, `settingsControl`. No other method
sends it, and an error reply has none (Linux, macOS and Windows).

| `settingsControl` | When | What it changes |
|---|---|---|
| no member | No start test of the daemon gave a state yet | Nothing. A folder with no repository, or a `HEAD` that is no ref, starts no test. The `.git` file below is the exception |
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
  repository" with no test. Its member follows the daemon's state (not measured).
- A `git.info` on a regular file gets the gate of the repository of its folder.
  Outside any repository it starts no test. As the first git request of a daemon its
  reply has no member on Linux and macOS. On Windows it is the hooks refusal, with
  `fork/exec <git.exe>: The directory name is invalid.`
- A request with the member gets `"off-switch"` also as the first request of a
  daemon, in a "no repository" reply and while a failed test is kept.
- A hold during a request moves to the git directory each later command that
  starts its own config listing (Linux, macOS and Windows). The config check and
  the call after it keep the copy, as does a command that already runs. On Linux,
  after the hold a later command runs the test and takes the copy. A create in a
  hold makes no copy.
- In that time `git.status`, `git.list_branches` and `git.worktree_remove` get no
  config refusal, as with the member. On a repository of format version 3 they
  answer `{"isRepo":false,"clean":false}`, `{"isRepo":false,"branches":[]}` and the
  lock-check text that ends `(its registrations could not be examined); retry`. The
  remove deletes nothing there because git itself fails. On a config that only the
  daemon refuses, the remove deletes. macOS and Windows are not measured.
- A held request keeps no hold for later requests. A `worktree add` that started in
  the hold is not stopped at 2 minutes after the holder answers. A held call that
  starts after that answer has the 2 minute limit (claustrum's own, not measured).
- Only the JSON value `true` counts for `shadowDirOff`. `"true"`, `1`, `null` and
  `false` are as no member (Linux and macOS).
- In the state `fallback-git` the test runs again on the first request more than 10
  minutes after the last test. In the state `shadow` no second test ran up to 720 s.
- The daemon logs one `[GitHandler]` line at the first decision of the state and one
  at a change.

#### The start test

The first request that reaches a repository, or the `.git` file above, without the
member runs the test. It is 10 git calls in a folder `.check-<n>` of the daemon, and
that folder is gone at the reply. Call 7 reads the config of the test repository. It
runs in `/`, and on Windows in the working folder of the daemon (from the code). If
the test fails, the request gets this text:

`Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.`

`git.info`, `git.status` and `git.list_branches` answer it as a `-32603` error.
`git.worktree_create` answers it in `error`, with the `errorCode`
`worktree_add_failed`. `git.worktree_remove` answers it after `failed to remove
worktree: could not check whether <worktreePath> is locked: `, with no `errorCode`.

A failed call 7 is followed by one `git version` call. If it fails too, the text is
`git cannot run on this host; git not run: <exec error>: <git text>` alone, in the
same frames. The exec error and the text are those of `git version`. A remove
deletes nothing there (Linux, macOS and Windows). On Windows that `git version` runs
in the working folder of the daemon.

| `<cause>` | When |
|---|---|
| `git <verb> failed under "<folder of the daemon>": <error lines of git, space-joined>` | A test call other than call 7 failed. Go's `%q` quotes the folder |
| `git <verb> failed under "<folder>": signal: killed` | The daemon ended a test call other than call 7 after 30 seconds (Linux and macOS). On Windows the end is `exit status 1` |
| the same with ` (git version <v>)` at its end | The failed or ended call was one of calls 1 to 5 (measured for `init` and `mktree`) |
| `this repository's git config cannot be used: <error lines of git>` | Call 7 failed, and the plain `git version` after it works |
| `this repository's git config cannot be used: git did not finish reading "<config of the test repository>" within 30 seconds; try again` | Call 7 ran, or held its stdout, past 30 seconds. The reply comes near 31 s. With stderr alone held, a call 7 that exits 1 gives the lines read before the cut (not measured) |
| `mkdir <path>: <reason>` or `open <path>: <reason>`, with no quotes | A folder or the lock file of the daemon cannot be made |

Some failed calls give `fallback-git` and not a failure. That is the case when git
refuses the copy and the same call works without it. A test call that works and
answers wrongly gives it too. At call 10 a `No space left on
device` error or an end by a signal fails the test. If two calls fail, the cause
names the first. A test call that takes 29 s passes, and one of 32 s gives the error
at about 31 s.

- The daemon keeps a failure for 10 seconds from the end of the failed test. A
  request in that time gets the kept text with no git call.
- The kept failure holds after its cause is gone. A request with
  `"shadowDirOff":true` works in that time.

#### The folders

The folder of one daemon is `<socket folder>/git-shadow/<host form>-<pid>-<16 hex>`.
`<host form>` is the host name with each character outside `A-Z`, `a-z`, `0-9` and `.`
written as `_`. A `-` becoming `_` and a kept `.` are measured (Linux and macOS). The
other characters are not measured. On Linux and macOS that folder and each folder of
a copy have mode `0700`, and the files of a copy have `0600`. The daemon makes the
folder at the first request that runs the test. It holds an exclusive lock on the
file `lock` in it for its whole life. On Windows the hold is an open handle with no
sharing.

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
  too (`.gitdir-<n>` on Linux and macOS, `.index-<n>` on Windows). For
  `.worktreeinclude-<n>` that is not measured. With
  `"shadowDirOff":true` the first three are in the temporary folder, as
  `claude-ssh-gitdir-<n>`, `claude-ssh-index-<n>` and
  `claude-ssh-worktreeinclude-<n>`.
- On Windows, if the folder of the daemon denies new folders, a create still works
  with the backup and restore privileges on. With both off it fails at `.s<n>`.

If the folder of a repository cannot be made, the request gets this text in the
three shapes of the start test:

`Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>`

The cause is the `mkdir` error of the folder. The same request again gets the same
line with no git call. On Windows a held copy gets 2 s of retries, then `rename
<temp file> <copy>: Access is denied.` with full paths.

The request that makes the folder of the daemon also sweeps `git-shadow`. An entry
goes if its lock file can be taken and its name has the host form of this host. An
entry with another name goes only when it is old: 8 days goes and 6 days stays. The
bound of 7 days is not measured. A held lock keeps the folder of a live daemon. The
sweep is measured on Linux and macOS, and in part on Windows.

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
folder with the oldest use that no git call holds. A held folder stays. With all 128
held, a request gets this text and nothing goes:

`Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again`

A folder goes between the git calls of a request, never during one. The next call
makes it again, with no new config read. A call right after its own config listing
does not (claustrum's own, not measured). A call whose remake fails gets the `cannot
write its copy` text above, or the 128 text when all 128 folders are held (from the
code). A git call that runs in a repository folder holds that folder until its
pipe wait ends (claustrum's own, not measured). So with all 128 held,
a new repository gets the 128 text then, and nothing goes. If its folder went and is
not made again, or its daemon shut down or replaced it, a call fails alone (from the code). It gets
this text, and a listing puts `listing the configuration in
force: ` before it (Linux and Windows):

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
macOS and Windows). So a pair that git reads to find the repository, such as
`safe.directory`, changes these answers (from the code, not measured). A good answer
is kept 5 seconds from the end of the call. A change of the root folder or of its
`.git` entry makes the daemon ask again.

| Answer of git | Reply |
|---|---|
| exit 128 with a text that starts with `fatal: not a git repository` in any letter case, or that holds `detected dubious ownership` or `cannot use bare repository` | The "no repository" answer of each method. No config is read, and no copy is made. git 2.56's text for a `.git` file to nowhere, `unsafe repository`, and the two phrases at other places of the text get the same answer (claustrum's own, not measured) |
| exit 1, exit 128 with no text, a signal (`signal: terminated`) or a git that cannot start (Windows) | `git could not check this repository on the remote machine: at "<root>": exit status <n>` |
| exit 1 or exit 2 with a text, or exit 128 with another text | The same text with git's text in place of `exit status 1`. Two lines stand there with one space between them. `warning: w` before `fatal: not a git repository` gets this reply. This row holds on all five methods. So does `fatal: error reading '<repo>/.git'` (Windows) |
| either of the two rows above | One `git version` call follows. If it fails, the reply is `git cannot run on this host; git not run: <exec error>: <git text>` in the frame of each method. A remove makes two such calls and deletes nothing. A git that cannot start is not measured here. After an exit 128 with no text or a signal, `git.info` and `git.worktree_create` are not measured. Neither is a remove with `worktreeRoot` |
| another path than the git directory, in two calls | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again`. `<path>` is the path as git printed it: with forward slashes on Windows |
| no answer in 30 seconds | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` (Linux, macOS and Windows) |

The three texts have the shapes of the start test. A bad answer is not kept, and a
remove asks twice. For a repository of another owner with no `safe.directory`,
`git.worktree_remove` answers the lock-check refusal that ends `(its registrations
could not be examined); retry`. With a child that holds the pipes, an exit 0 counts
by its output so far (measured on `git.status` only).

One `git version` call also follows a config read that failed with a syntax error.

#### The git calls

In the state `shadow` each hardened call carries `GIT_COMMON_DIR=<copy>` and
`GIT_OBJECT_DIRECTORY=<git directory>/objects`. A call with no `--git-dir=` in its
arguments also carries `GIT_DIR` and `GIT_WORK_TREE`. The four come right after the
profile values, in that order (Linux, macOS and Windows, the order from Linux and
macOS). [Hardened git calls](#hardened-git-calls) has the other points of a call.

#### `config.worktree`

The copy has no `extensions.worktreeConfig` setting, so git reads no
`config.worktree` file in the state `shadow`. The daemon does two things in its
place, in a repository with that extension and a `config.worktree` file. The
`worktreeConfig` values `2` and `10` turn that extension on.

- Each key of the copied settings file but the format version, `extensions.*` and
  `core.bare` goes before the option list of the git calls as one `-c` item, with the
  value of the copy, sorted by key. Six keys are measured. Other keys and a repeated
  key are not measured. A call on a linked worktree gets the items of its own file.
- A new worktree of `git.worktree_create` gets its own `config.worktree` file. See
  [git.worktree_create](protocol/git-worktree-create.md).

#### Where claustrum differs from `5fd08069`

In the differences that stay the reply lines are equal, except in two Windows rows of
a create whose `.git` becomes a file.

- After the start test on a `.git` file that names a missing path, `5fd08069` runs a
  `rev-parse` against `.probe`. claustrum runs a config listing there and makes no
  `.probe`, with equal frames (macOS and Windows).

### Hardened git calls

The git methods run most git steps as hardened calls. A hardened call is a git call
with a fixed list of `-c` options and a fixed set of environment variables. The lists
and the variables were measured against `5fd08069` with a logging git wrapper. The
`GIT_COMMON_DIR` pin and the hook pins were measured against `f6010b97`, and the
limits of the hook pins against `89cb6289`. A rule that names no system was measured
on Linux, macOS and Windows VMs.

A client sees this shape in at least four places:

- `git.status` reads the real `HEAD` commit, also where `refs/replace` names another
  (see [git.status](protocol/git-status.md#what-the-status-reads)).
- The failure texts of `git.worktree_create` hold no graft-file `hint:` lines.
- On Windows a create with a tree path of 270 characters succeeds.
- The records of a new worktree hold absolute paths, also where the user config sets
  `worktree.useRelativePaths` (macOS and Windows VMs).

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

`git config -z --list` ends each record with a NUL byte and puts a LF between key and
value. A key `hook.<name>.<var>` names a hook. The name is the text between the first
and the last dot. It can be empty or hold dots. `hook.<var>` names no hook, and a hook
key inside a value pins nothing.

Each hardened call gets two base pins, `hook.enabled=false` and an empty
`hook.event`. Each hook name adds `hook.<name>.enabled=false` and an empty
`hook.<name>.event`. A name counts once, in the order that the listing first shows
it. The pins go in through `GIT_CONFIG_COUNT`. The daemon's own `GIT_CONFIG_COUNT`
moves them (see [The daemon's own git environment](#the-daemons-own-git-environment)).

These points are from the code, not measured:

- Each hardened call takes the names of the listing right before it.
- A later listing that exceeds a limit does not refuse its call. The call then gets
  the two base pins only. A later listing that fails refuses in three places: the two
  later listings of `git.status` (see [git.status](protocol/git-status.md#errors)), the
  two listings before the checkout of a create in the state `shadow` (see
  [git.worktree_create → Error texts](protocol/git-worktree-create.md#error-texts)), and any listing that its time
  limit stopped.
- Two names that differ only in letter case get two pins. A name under two keys
  counts once toward the limits. The count is tested before the byte sum.

Only `command` and `event` were measured as `<var>`.

#### Profiles

A hardened call uses the heavy profile or the light profile:

| Profile | `-c` items | Variables, in this order | Calls |
|---|---|---|---|
| heavy | 28 | `GIT_NO_LAZY_FETCH=1`, `GIT_NO_REPLACE_OBJECTS=1`, `GIT_GRAFT_FILE=<null>`, `GIT_ALLOW_PROTOCOL=denied_by_claude_ssh`, an empty `GIT_ASKPASS`, `GIT_TERMINAL_PROMPT=0` | `git.info`, `git.list_branches` and `git.status`. Each `rev-parse --absolute-git-dir`, and the `--git-dir=<dir> --work-tree=<dir> rev-parse --show-toplevel` call. The hooks refusal check of `git.info`, `git.list_branches`, `git.status` and `git.worktree_remove` |
| light | 20 | `GIT_ALLOW_PROTOCOL=https:ssh`, `GIT_TERMINAL_PROMPT=0`, `GIT_NO_REPLACE_OBJECTS=1`, `GIT_GRAFT_FILE=<null>` | the other hardened calls, mostly those of `git.worktree_create`. The hooks refusal check of a create, and of a remove with `worktreeRoot` (the remove on a Linux VM) |

- The daemon's own `GIT_ALLOW_PROTOCOL` changes the first light value (see
  [The daemon's own git environment](#the-daemons-own-git-environment)).
- In the states `shadow` and `fallback-git` without `"shadowDirOff":true`, the light
  calls of a create and their listings carry the six heavy values. So do the three
  listings of the branch step of a remove. Linux and macOS VMs measured the state
  `shadow`, and a Windows VM the state `fallback-git`. The checkout
  and its listings keep the four light values. With `"shadowDirOff":true` the light
  calls carry the four light values.
- The calls of [the branch step](#the-branch-step) have their own variables.
- The `status`, `ls-files` and `diff-index` calls of `git.status` add
  `GIT_OPTIONAL_LOCKS=0`. The checkout adds `GIT_INDEX_FILE`.
- The order of the variables is measured on Linux and macOS VMs. The Windows logs of
  `5fd08069` are sorted by name and show the same values.

The heavy list, in order:

```text
core.fsmonitor=false
core.hooksPath=/dev/null
core.attributesFile=/dev/null
core.excludesFile=<file>
core.longpaths=true
protocol.allow=never
protocol.ext.allow=never
protocol.fd.allow=never
protocol.file.allow=never
protocol.git.allow=never
protocol.ssh.allow=never
protocol.http.allow=never
protocol.https.allow=never
credential.helper=
core.askPass=
fetch.bundleURI=
core.alternateRefsCommand=
alias.remote-https=
alias.remote-http=
alias.remote-ssh=
alias.remote-denied_by_claude_ssh.command=
help.autocorrect=0
submodule.recurse=false
fetch.recurseSubmodules=false
push.recurseSubmodules=false
gc.auto=0
maintenance.auto=false
core.commitGraph=false
```

The light list, in order:

```text
core.hooksPath=/dev/null
core.fsmonitor=false
branch.autoSetupMerge=false
fetch.bundleURI=
http.saveCookies=false
core.alternateRefsCommand=
alias.remote-https=
alias.remote-http=
alias.remote-ssh=
alias.remote-denied_by_claude_ssh.command=
help.autocorrect=0
core.excludesFile=<file>
submodule.recurse=false
fetch.recurseSubmodules=false
push.recurseSubmodules=false
gc.auto=0
maintenance.auto=false
advice.graftFileDeprecated=false
worktree.useRelativePaths=false
core.longpaths=true
```

The checkout adds `core.splitIndex=false` and `core.commitGraph=false` after the
light list. The `rev-list` call of the branch step adds `core.commitGraph=false`.

`<null>` is `/dev/null` on Linux and macOS, and `NUL` on Windows. When the user has
no global excludes file, `<file>` is `<null>`. Else it is that file. The
`core.hooksPath` and `core.attributesFile` values stay `/dev/null` on Windows. On
Windows the `status`, `ls-files` and `diff-index` calls of `git.status` pass
`core.excludesFile=/dev/null` too. That is claustrum's own, see
[D16](DIVERGENCES.md#d16).

With `GIT_GRAFT_FILE` set, git can print 8 `hint:` lines about `info/grafts` on
standard error. The light list turns them off with
`advice.graftFileDeprecated=false`, and the heavy list does not. The `status`,
`ls-files`, `rev-parse` and `diff-index` calls of `git.status` give only stdout to
the reply. A failure of one of them answers the exec error. So no `git.status`
reply holds a `hint:` line.

#### Environment and working directory

Each hardened call runs with the repository directory as its working directory, and
it passes no `-C`. The checkout of `git.worktree_create` runs in the new worktree,
and the `git status` call in the worktree. Both name their folders with `--git-dir`
and `--work-tree`.

The environment of a hardened call has four parts, in this order:

1. the daemon's own environment
2. the profile variables
3. the `GIT_COMMON_DIR` pin, if the [trust check](#git-directory-trust-check) sets one
4. the hook pins

In the state `shadow` the pin names the copy, and `GIT_OBJECT_DIRECTORY`, `GIT_DIR` and
`GIT_WORK_TREE` follow it (see [The settings copy](#the-settings-copy)). With
`"shadowDirOff":true` and in the state `fallback-git` the call carries
`GIT_COMMON_DIR=<git directory>` alone.

A `GIT_*` variable of the daemon keeps its place, before the added variables.
[The daemon's own git environment](#the-daemons-own-git-environment) gives the
exceptions. If the daemon adds a variable that its
environment already has, the added value and place win. Linux and macOS VMs. On
Windows claustrum gives a git child its environment in name order. That is
[D23](DIVERGENCES.md#d23).

The `git status` call of `git.status` starts with `--attr-source=<empty tree>`. The
heavy `-c` options, `--git-dir` and `--work-tree` follow it (Linux and macOS VMs).
It and its listing pin `GIT_COMMON_DIR` to the clean path of the common git
directory. On Windows that path has backslashes.

#### Listings and probes

Before each hardened call except the first (see [Refusal texts](#refusal-texts)),
the daemon runs `git config -z --list` in the same directory. The listing before the
checkout and the listing before the `git status` call start with `--git-dir=<dir>`. The listing carries the profile variables of the call after
it and the `GIT_COMMON_DIR` pin, and no hook pins. A listing before a call of the
branch step carries the light variables, and in the states `shadow` and
`fallback-git` the six heavy values. In the state `shadow` a second listing runs right before the checkout. It
carries the hook pins, `GIT_INDEX_FILE` and `LANGUAGE=C`, and it adds no `LC_ALL`.
Every other listing drops the daemon's own
`LC_ALL` and `LANGUAGE` and ends with `LC_ALL=C` and `LANGUAGE=C`. git then writes
its listing errors in English (Linux VM). The call after the listing keeps the
daemon's `LC_ALL`, `LANGUAGE` and `LANG` (Linux, macOS and Windows VMs). Other
`LC_*` variables stay too, but only those three were measured.

Before its first listing, a request with no cached value runs `git config --includes
--path core.excludesFile` in the temporary directory. It adds only `GIT_DIR=<null>`.
Two concurrent requests run it twice (Linux VM). The probe `git --attr-source=<empty tree>
version` adds no variable.

#### Temporary names

[The folders](#the-folders) of the settings copy names the short-lived folders of a
request. With `"shadowDirOff":true` they are temporary folders with these names:

- `claude-ssh-gitdir-<n>`, the git directory of the `git status` call
- `claude-ssh-worktreeinclude-<n>`, the folder of the include step of a create
- `claude-ssh-index-<n>`, the index folder of the checkout of a create

`<n>` is decimal digits. With `"shadowDirOff":true`, `5fd08069` uses the first two
names (Linux, macOS and Windows VMs), and `89cb6289` the index name (Linux and
macOS VMs). On Linux and macOS a client can see the index name
in the text `open <tmp>/claude-ssh-index-<n>/index: no such file or directory`. The
index name on Windows is claustrum's own (not measured).

#### Calls with `worktreeRoot`

A create with `worktreeRoot` runs a light `rev-parse --show-toplevel`, a heavy
`rev-parse --absolute-git-dir` and a light `worktree list --porcelain -z` in
`baseRepo`, each after its own listing. With the excludes read and the repo test,
that is the 9 git calls of the error catalogue (Linux and macOS VMs).

If the `rev-parse --show-toplevel` call of a `git.worktree_create` with
`worktreeRoot` fails, the create stops with `mkdir_failed` (Linux VM). Two failed `worktree
list` calls stop it the same way (Linux VM). If `baseRepo` and the root are both missing, a
`git.worktree_remove` with `worktreeRoot` makes no git call. It answers the `the
worktree location <root> is not reachable` refusal of
[git.worktree_remove → Error texts](protocol/git-worktree-remove.md#error-texts). Linux and macOS VMs.

#### Where the calls differ from `5fd08069`

With `"shadowDirOff":true` a call that
both builds make is equal in list, arguments, working directory and variables.
Without the member these are equal: the folder variables, the six values of the
light calls of a create, the `lfs.storage` item and `LANGUAGE=C` of the checkout,
its second listing and the names of the short-lived entries (state `shadow`, Linux
and macOS VMs). In the state `fallback-git` the variables and the names are equal
(Windows VM). The order of the
variables is equal on Linux and macOS VMs.

- With `worktreeRoot`, `5fd08069` runs `rev-parse --show-toplevel`, `rev-parse
  --absolute-git-dir` and `worktree list --porcelain -z` before the add, after the
  add and after the checkout. claustrum runs them before the add only (Linux and
  macOS VMs). A second remove of a gone worktree makes fewer calls too (Linux VM).
- With `existingBranch`, claustrum runs one config listing before the attach add,
  where `5fd08069` runs two. After a failed attach add, claustrum runs one listing
  before the `-b` add, and `5fd08069` none.
- With a git filter or git-lfs, the read-tree of claustrum can carry filter items
  before its list.

The reply lines are equal in each difference above.

### The daemon's own git environment

This section names the variables of the daemon's own environment that reach the
git calls of the git methods. It also gives the refusals of that environment. A
rule
here is measured against `f6010b97` on Linux, macOS and Windows VMs, unless it
names another build or fewer systems.

#### Variables that the git calls keep or drop

The repository calls are the configuration listings, the hardened calls,
`worktree add`, the checkout and `git status`. The excludes read and the
`--attr-source` probe get the daemon's environment as it is, plus their own
additions (`5fd08069`).

- The daemon's `PATH`, `GIT_EXEC_PATH`, `GIT_SSH` and loader variables are
  cleaned for the repository calls and the calls of the start test (`5fd08069`).
  The calls of the start test also get no `GIT_` variable of the daemon
  (`5fd08069`).
- `GIT_CONFIG` and `GIT_CONFIG_PARAMETERS` do not reach a repository call. The
  names match exactly, in upper case. So on Windows `git_config` and
  `Git_Config_Parameters` reach git.
- A listing gets the daemon's `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` as they are. It keeps a name with a leading-zero index,
  such as `GIT_CONFIG_KEY_01`, in its place (Linux and macOS VMs).
- A hardened call gets these names after the profile and the `GIT_COMMON_DIR`
  pin. First comes `GIT_CONFIG_COUNT`, with the number of all the pairs. Then
  come the daemon's pairs `0` to `count-1`, in index order. Then come the hook
  pins, from the pair `count` on. They are `hook.enabled=false`, an empty
  `hook.event`, and then the two pins of each hook name (`89cb6289`). The
  `GIT_INDEX_FILE` of the checkout and the `GIT_OPTIONAL_LOCKS` of the `status`,
  `ls-files` and `diff-index` calls follow them. The order is measured on Linux
  and macOS VMs.
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
the code). `5fd08069` does so for the branch of `git.info` and the repository
test of a create (Linux VM).

[Git-directory trust check](#git-directory-trust-check) gives the `GIT_DIR` of the
listing in a folder with no repository, and its frames.

#### The check of `GIT_CONFIG_COUNT`

The git methods check the daemon's `GIT_CONFIG_COUNT`. The check reads the count
and its pairs by their exact upper-case names. On Windows it does not see a
lower-case `git_config_count` or `git_config_key_0`.

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

`GIT_CONFIG_COUNT=0`, a pair with an empty value and `GIT_CONFIG_PARAMETERS='x`
change no frame in a plain folder.

#### Configuration listings that git cannot parse

With a key that git cannot read, git fails each configuration listing with exit
status 128. `<text>` is then `config-defined hooks could not be pinned off; git not
run: listing the configuration in force: exit status 128: <git text>`. A good pair
before the broken pair changes nothing. The git of the VMs gave these texts:

| Key | `<git text>` |
|---|---|
| empty | `error: empty config key fatal: unable to parse command-line config` |
| `nodot` | `error: key does not contain a section: nodot fatal: unable to parse command-line config` |
| `a.b c` | `error: invalid key: a.b c fatal: unable to parse command-line config` |

A `GIT_CONFIG_GLOBAL` that names a file git cannot parse fails each listing too.
With the content `[broken`, `<git text>` is `fatal: bad config line 1 in file
<file>` (Linux and macOS VMs). The `git version` call that follows a failed
listing gets no `GIT_CONFIG_GLOBAL` and passes (`89cb6289`). So the text stays the
hooks refusal. `89cb6289` answers so for `git.info` on a plain
folder and on a repository, and for `git.worktree_create` on a plain folder.

`89cb6289` and claustrum answer these failures in a folder that holds no
repository too, with these frames. `f6010b97` refuses these requests too. Nothing changes on disk.

| Request | Frame |
|---|---|
| `git.info` on a plain folder, or on a folder with an empty `.git` folder | `{"error":{"code":-32603,"message":<text>}}` |
| `git.list_branches` on a plain folder | the same |
| `git.status` with a plain-folder `baseRepo` | the same |
| `git.worktree_create` with a plain-folder `baseRepo`, without and with `worktreeRoot` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, and no folder is made. With nothing set, a create with `worktreeRoot` on Windows answers `not_a_repo` |
| `git.worktree_remove` without `worktreeRoot`, plain-folder `baseRepo`, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}` |
| `git.worktree_remove` with `worktreeRoot`, plain-folder `baseRepo` | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}`. Linux and macOS VMs. Windows is not measured. There claustrum refuses the `worktreeRoot` first (from the code) |

A path that does not exist answers as it does with nothing set (Linux and macOS
VMs, and `git.info` on a Windows VM). On Linux and macOS a plain folder of mode
0000, where git cannot start, answers so too.

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

#### Where each method answers the refusal

A refusal text starts with `config-defined hooks could not be pinned off; git not
run: `. Before the refusal, the daemon runs no git call except the excludes read.
A refusal of the git-directory trust check comes first, with the trust text in the
frame. That holds on Linux and macOS VMs in `git.info`, `git.list_branches`,
`git.status` and `git.worktree_create`. On the same VMs it holds in
`git.worktree_remove` for a gone worktree without `worktreeRoot`. With
`worktreeRoot` it holds there for a `baseRepo` that exists. A Windows VM measured
it in `git.info`, `git.status` and the remove of a gone worktree. The
table gives the frame of each method, and the refusals that come before the check.

| Method | Frame |
|---|---|
| `git.info` | `{"error":{"code":-32603,"message":<text>}}`, also for a plain dir, a missing dir and a file |
| `git.list_branches` | the same, also for a plain dir, a file, and an empty or absent `path`. For an empty or absent `path`, the daemon's working directory was a repository or a plain dir |
| `git.list_branches`, `path` not empty and does not resolve, or inside a managed worktrees directory | `{"isRepo":false,"branches":[]}`, before the check and with no git call |
| `git.status` | the same as `git.info`, also for a `path` that is a plain dir, the repository, missing or a file, and for a plain-dir `baseRepo` |
| `git.status`, `baseRepo` does not resolve | `{"isRepo":false,"clean":false}`, before the check and with no git call. See [git.status](protocol/git-status.md#baserepo). From the code: a `baseRepo` in a managed worktrees tree answers the same way. That case is not measured with a refused count |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, also for a plain-dir or missing `baseRepo`. The check comes before the refusals of a relative `worktreePath`, a `..` component, a path outside the repository and a path that exists. Three frames come first: the `-32602` frame of a missing `branchName`, the `nested_base_repo` frame of a `baseRepo` inside a managed worktrees directory, and the frame of claustrum's own trust-root test (Windows VM only). On Windows the check comes before the `worktreeRoot` refusal |
| `git.worktree_remove` | Some refusals come first, with their usual frames. These are a relative path, a path outside the repository, the repository itself and a `..` component. With `worktreeRoot` they are also a path outside the root, a `..` `baseRepo` and a root in `baseRepo` (Linux and macOS VMs). On Windows the `worktreeRoot` refusal comes first. A `baseRepo` that fails claustrum's own trust-root test gets the managed-worktrees refusal first (Windows VM only). Without `worktreeRoot`, `open <baseRepo>: not a directory` comes first for a regular-file `baseRepo`. So does `statat .claude: permission denied` for a `baseRepo` with mode 0600 and a path under `.claude` (Linux and macOS VMs) |
| `git.worktree_remove`, no `worktreeRoot`, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`, also for a plain-dir or missing `baseRepo` |
| `git.worktree_remove`, no `worktreeRoot`, worktree directory present | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`. On Linux and macOS VMs a `baseRepo` of `<T>/missing/..`, which does not resolve, gets it too. On Windows that `baseRepo` gets the managed-worktrees refusal first |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` a repository or a plain dir | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}`. A trust refusal puts its own text there instead. Linux and macOS VMs |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` missing, absolute and without a `..` component, worktree directory present | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (the repository at <baseRepo> could not be read); retry"}`. Linux and macOS VMs |
| `git.worktree_remove` with `worktreeRoot`, `baseRepo` missing, absolute and without a `..` component, worktree directory gone | `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`. If the root is missing too, the "not reachable" refusal comes first. Linux and macOS VMs |

### Time limits of git calls

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

### The branch step

The branch step deletes a branch only when another local branch or a
remote-tracking ref reaches its tip. Else it keeps the branch.
`git.worktree_remove` runs it after the worktree directory and its registration
go. The `git.worktree_create` rollbacks run it on the branch that the call made.
`<b>` is `branchName` as sent. `<tip>` is the object name in the listed record
of `refs/heads/<b>`. A rule that is not marked is measured against
`89cb6289` on a Linux VM. A rule that names macOS or Windows was measured there
too. A rule marked "claustrum's own" is not measured.

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

#### The git calls of the branch step

Each call runs in `baseRepo` after its own configuration listing (see
[Hardened git calls](#hardened-git-calls)). The calls are
`for-each-ref --count=10001 --format=%(objectname)%00%(refname)%00%(symref)
refs/heads/`, `rev-parse --verify --quiet refs/heads/<b>^{commit}`,
`rev-list -n 1 <tip> --not --remotes --not --stdin --` and
`update-ref --no-deref -d refs/heads/<b> <tip>`. Every call carries the light
`-c` list, and `rev-list` adds `-c core.commitGraph=false` after it. The daemon's
own environment comes first (see
[The daemon's own git environment](#the-daemons-own-git-environment)). Then the
calls set `GIT_TERMINAL_PROMPT=0`,
`GIT_NO_REPLACE_OBJECTS=1`, `GIT_GRAFT_FILE=<null device>` and the
`GIT_COMMON_DIR` pin. Then come `GIT_CONFIG_COUNT`, the inherited config pairs,
the hook pins,
`GIT_NO_LAZY_FETCH=1`,
`GIT_ALLOW_PROTOCOL=denied_by_claude_ssh` and an empty `GIT_ASKPASS` (Linux,
macOS and Windows VMs). Only `rev-list` gets a pipe on stdin: one
`^<refname>` line for each other local branch that is not a symbolic ref. The
other calls get the null device, where `89cb6289` gives a character device (macOS
VM). In
some states the reference runs more git calls than claustrum. Examples are no
repository at `baseRepo`, a gone worktree path, a second create at one path and
an attach add. The frames are equal.

#### Time limits of the branch step

These bounds are parity. They are always on, and they are not a divergence of
the catalog.

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
  common git dir, also in a rollback (claustrum's own). So two names of one git
  directory count as one repository here.
- The steps of different repositories run at the same time.
- claustrum runs two removes of one `baseRepo` one after the other. `89cb6289`
  deletes both worktree directories first.
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
`-probe-cli`.

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
[Daemon startup](DAEMON.md#daemon-startup-serve) has what the start does, the ready line,
and the exit codes and texts of the launcher. Unless a rule here says otherwise,
it is measured against `89cb6289` on Linux and macOS VMs. The flags under
[Flags of claustrum alone](#flags-of-claustrum-alone) have no reference run.

#### The token source

A token source is required. The detached daemon tests for it, not the launcher.
The daemon writes its reason to its own stderr, which is the
[daemon log](DAEMON.md#daemon-log-remote-serverlog), and exits. The launcher then gives up at
12 s, as under [The launcher wait](DAEMON.md#the-launcher-wait). This is deliberate parity.

- With neither flag the reason is
  `claustrum: daemonized child requires --token-file or --token-fd`. That case is
  not measured on `89cb6289`.
- A zero-byte `-token-file` behaves the same way, with the reason
  `claustrum: token is empty`. The daemon removes the file first.
- A `-token-file` that cannot be read gives `claustrum: read --token-file: <err>`
  (from the code).
- The daemon reads the token as a line. It strips every trailing `\r` and `\n`, and
  keeps other whitespace verbatim (from the code; the reference rows cover one
  `\n` or `\r\n`). It then removes the
  `-token-file`, as under [Authentication](#authentication).
- `-token-fd <n>` is claustrum-only. On Windows the start fails (from the code, not
  measured). On Linux and macOS the launcher reads the token from an
  already-open fd, where `0` is stdin, and forwards it to the daemon over an
  inherited pipe. So this handoff never touches disk. A failed read there gives
  `claustrum: read --token-fd: <err>` on the stderr of the launcher, with exit `1`
  (from the code). An empty token there is no read failure. The daemon then logs
  `claustrum: read --token-file: open : <err>`, and the launcher gives up at 12 s
  (from the code).

#### The login-shell PATH (Linux and macOS)

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
  can run the login shell a second time at the first spawn.

The shell of the PATH read gets seven entries. Five come from the daemon: `HOME`,
`PATH`, `SHELL`, `TERM` and `USER`. A name that the daemon does not have gets an
empty value. The other two are `DISABLE_AUTO_UPDATE=true` and
`ZSH_DISABLE_COMPFIX=true`. No other entry of the
daemon reaches it, such as `LANG` or `CLAUDE_SSH_DAEMON_CHILD`. The order of the
entries is not measured. A daemon that has a `TERM`, or that has no `HOME`, `PATH`
or `USER`, is not measured either. The shell of the SSH agent read gets another
environment.

#### The re-exec marker

The re-exec marker is `CLAUSTRUM_DAEMON_CHILD`, not the reference's
`CLAUDE_SSH_DAEMON_CHILD`. [ARCHITECTURE.md](ARCHITECTURE.md) has the reason.
claustrum unsets its own marker before it spawns. The daemon still sets
`CLAUDE_SSH_DAEMON_CHILD=1` in its environment, so `process.spawn` children
inherit it as on the reference.

#### Flags of claustrum alone

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
  after `server.shutdown`, the direct child too (Windows VM). The stdio of a survivor is not
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

The words, the signals and the removes are measured against `f6010b97` and
`90fca6e6` on Linux and macOS VMs. The steps after a successful connect are
measured against `5fd08069` on Linux, macOS and Windows VMs. A rule marked
claustrum's own is not measured.

`-stop` sends `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}` and a
newline. It sends no `auth` member, because that method is not authenticated
(see [Authentication](#authentication)). Then it reads once, with a 2 s deadline
(`5db5e4a`), and discards what it reads. It never relays a frame to stdout. A live daemon's
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
new holder gets no signal (`5fd08069`, Linux VM, after a successful connect.
After a failed connect it is claustrum's own). Windows has no run-dir lock, so a
failed connect there always prints `none`.

The macOS reference signals any live holder whose lock record says role `serve`
on this host's node. It does not check the holder's command line. claustrum
keeps its holder test on macOS, so it does not signal such a holder that is not
a serve process. It prints `survivor`. This is part of [D15](DIVERGENCES.md#d15).

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
two lines with `pid <N>` and no instance (`89cb6289`, Linux VM). claustrum
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
live foreign listener that
refuses the connect with `EACCES` loses its socket path. The listener stays
alive, but a client that dials by path cannot reach it after that. A variant
that removes only a socket is recorded under [Candidates considered but not
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

`-install` makes sure that one CLI version is in the cli-dir. On a cache miss it
downloads or reads the CLI, extracts and verifies it, puts it in place and runs
`<cli> --version` once. Then it prunes old versions and prints one
`__INSTALL_RESULT__<json>` facts line (schema in [ARCHITECTURE.md](ARCHITECTURE.md)).
A rule that is not marked claustrum's own is measured against `89cb6289`. The
prune, the sweeps, the blob, the rename text and the retry and the Linux cache-hit
file test are measured against `5fd08069`. The checksum order, the raw `-cli-dir`
text step and the `is a directory` text are measured on `5fd08069`, and `89cb6289`
is not measured there. A rule from another build names it. Both builds ran on Linux, macOS and Windows VMs, and a rule
that names one system was measured there only.

| Flag | Default | Meaning |
|---|---|---|
| `-cli-version <v>` | none | The version to ensure. It names the CLI file. Without it the `cliPath` is empty, no CLI runs and no folder is made |
| `-cli-dir <d>` | `<home>/.claude/remote/ccd-cli` | The folder of the version files. An empty value takes the default too |
| `-cli-url <u>` | none | Download the zstd archive of the CLI from this URL |
| `-cli-checksum <sha256>` | none | The SHA-256 of the archive |
| `-cli-zst <p>` | none | Read the zstd archive from this file, the blob |

`-cli-keep`, `-max-cli-bytes`, `-cli-download-timeout` and the deprecated
`-cli-probe-timeout` and `-libc-probe-timeout` are in
[Flags and config keys](#flags-and-config-keys). `-install` reaches the network only
with `-cli-url`.

`-install` exits `0` whenever it prints the facts line. It reports a failure inside
the facts as `cliError`, not through the exit code. Three cases print no facts
line. A flag that does not parse exits `2`. `-cli-version` with no `-cli-dir` and
no home folder exits `2`. Any other run with no home folder exits `1`. With no `HOME`
the reference prints `claude-ssh: cannot resolve home directory: $HOME is not defined`
on stderr (Linux). claustrum prints the same line with its own prefix,
`claustrum: `. On Windows its line names `%userprofile%`. The exit `1` cases, macOS and a missing `USERPROFILE` are not
measured.

The `cliError` catalogue follows:

| `cliError` | trigger |
|---|---|
| `installed cli at <path> is not runnable` | the `--version` run of a new CLI did not start or exited non-zero. `<path>` is the final path |
| `cli <v> missing and no --cli-url or --cli-zst provided` | cache miss with no source flag |
| `cli unresponsive: the installed Claude Code binary started but did not answer --version within 30s (120s for a first run) and was stopped; the host is not letting it run (endpoint security software or a stalled network home are the usual causes)` | a direct `--version` run stopped at 30 s (120 s after an install in the same run). Always-on, parity (see below) |
| `checksum mismatch: expected=<x>, actual=<y>` | `-cli-checksum` verify failed. It applies to every `-cli-url` run. With a supplied checksum it applies to `-cli-zst` too. The compare is case-sensitive |
| `opening input: <err>` | `-cli-zst` open error |
| `decompressing: <err>` | bad zstd blob or body, for example `invalid input: magic number mismatch`. A `-cli-zst` folder gives `read <p>: is a directory` (Linux) |
| `decompressing: decompressed CLI exceeds <n> bytes` | D10 cap, opt-in |
| `staging cli: <err>` | no staging file can be made in the cli-dir (claustrum's own, not measured) |
| `download failed with status <n>` | the server answered a status other than 200 (`5db5e4a`) |
| `download failed: response exceeds <n> bytes` | D10 cap on the download body, opt-in |
| `download failed: <transport err>` | a transport error before the body, for example a refused connection |
| `download failed: Get "<url>": net/http: timeout awaiting response headers` | no response headers for 60 s (always-on, parity, see below) |
| `download interrupted after <got>/<total> bytes: <transport err>` | a body read that ended with a transport error, for example `read tcp …: read: connection reset by peer` (parity, see below) |
| `download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)` | D12 download deadline, opt-in |
| `download stalled: no data for 60s after <got>/<total> bytes` | read-idle abort, meaning no bytes for 60 s on the `-cli-url` body (always-on, parity, `4534d86`) |
| `mkdir cli dir: <err>` | cli-dir uncreatable. `cliPath` is then empty. A file or a FIFO at the cli-dir path is replaced first (see Staging and cleanup) |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli path must not be or contain the home directory: "<path>"` | a folder at the final path that is the home folder or one of its parent folders (D2 hardening) |
| `clearing stale dir at <path>: <err>` | an occupied `cliPath` directory that claustrum cannot remove |
| `rename: <err>` | the rename of the new CLI failed, for example `rename: rename <tmp> <cli>: Access is denied.` on Windows. The prefix on Linux and macOS is claustrum's own |
| `rename: staging file vanished before install: <err>` | a concurrent sweep took the staging file |
| `cli unresponsive: the installed Claude Code binary was started through the host's managed launcher <argv0> and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish` | a managed launcher run stopped at 33 s (123 s after a fresh install), with `CLAUDE_SSH_MANAGED_LAUNCHER=1` (see below) |

The managed launcher (measured against `89cb6289` on a Linux VM unless a rule says otherwise):
- `-install` uses the [managed launcher](protocol/launcher-resolve.md) only when
  `CLAUDE_SSH_MANAGED_LAUNCHER` is exactly `1`. Every other value, and an unset
  variable, adds no field and runs no launcher. Values other than `true` and `1 `
  (a trailing space) are not measured.
- With the gate and a CLI path, `-install` resolves the launcher for that path, as
  [launcher.resolve](protocol/launcher-resolve.md) does. The facts line then gains
  launcher fields after `cliWasPresent`, `cliError` and `cliUnresponsive`, in this
  order: `launcherStatus`, `launcher`, `launcherSource`, `launcherPath`,
  `launcherReason`, `launcherStderr`. A field that does not apply is omitted. With
  `-cli-url`, `fetch` comes after `cliUnresponsive` and before `launcherStatus`
  (Linux).
- An empty `cliPath` gains no launcher field. That holds without `-cli-version`,
  where the line ends `"cliPath":"","cliWasPresent":false}`, and for a cli-dir that
  cannot be made (Linux).
- With `-cli-version` and no `-cli-dir`, the CLI path is in the default CLI folder,
  and the launcher gets that path (macOS).
- `none`: the CLI runs directly under the direct bounds below, with the gate in its
  env. The facts end `"cliWasPresent":true,"launcherStatus":"none"}`. A stopped
  direct run ends `"cliUnresponsive":true,"launcherStatus":"none"}`. On Windows the
  resolve always answers none, so `-install` appends `"launcherStatus":"none"` last
  and runs the CLI directly.
- `usable`: the CLI runs once, as `<launcher argv...> <cli> --version`. No direct
  run happens. The launcher's stdout is not reported, and its stderr does not reach
  `-install` stderr. The facts add `launcher` and `launcherSource`.
- `unusable` or `unreadable`: the CLI does not run at all. `cliWasPresent` stays
  true, and there is no `cliError`. The facts add `launcherSource` or
  `launcherPath`, and `launcherReason`.
- `probe_failed`: the launcher run exited non-zero or ended on a signal.
  `launcherReason` is `exit status <n>` or `terminated by SIGTERM`. The CLI file
  stays, `cliWasPresent` stays true, and there is no `cliError`. `launcherStderr`
  holds the launcher's stderr. If that is empty, the field is omitted.
- `launcherStderr` keeps 8192 bytes. More stderr adds `\n[… <n> more bytes]`, with
  the JSON escape `\n` on the line and `…` as raw `e2 80 a6`. `<n>` is the count of
  dropped bytes. claustrum's own (not measured): it keeps the first bytes, and it
  adds the `\n` also after a kept part that ends in a newline.
- On a cache hit a launcher run that has not ended at 33 s is stopped. The facts
  then read `"cliWasPresent":false`, the launcher `cli unresponsive: …` text above,
  `"cliUnresponsive":true`, `"launcherStatus":"unresponsive"` and
  `"launcherReason":"did not exit within 33s and was stopped"`. The CLI file stays,
  no install follows (with a source flag, claustrum's own), nothing is pruned and the sweep runs (Linux and macOS). With
  a usable launcher the direct bounds do not apply (Linux and macOS).
- The run right after a fresh install has a bound of 123 s (Linux). A stopped run
  gives the same facts with
  `"launcherReason":"did not exit within 123s and was stopped"`. The CLI stays
  installed, the blob is consumed and nothing is pruned.
- A fresh install puts the CLI at its final path and then runs it once through a
  usable launcher, which gets the final path. The facts read
  `"cliWasPresent":false` with the usable fields (Linux and macOS). A failed run
  and an unusable launcher still leave the CLI installed (Linux, `-cli-url`).
  claustrum does the same for an unreadable answer and for `-cli-zst` (claustrum's
  own).
- On a cache hit on Linux the CLI file must be a regular file with an exec bit
  that starts with `#!` or an ELF header of this machine. Another file does not
  run, and the install runs over it. `5fd08069` does that for a text file and
  for an ELF of another machine. The test beyond those two files is claustrum's
  own.
- The launcher run gets the daemon env without `CLAUDE_SSH_MANAGED_LAUNCHER`
  (Linux and macOS). claustrum also drops `CLAUDE_CODE_PROCESS_WRAPPER` and
  `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` there, as for a spawn with a launcher
  (claustrum's own).
- claustrum's own (not measured): the stop kills the run's whole process group and
  then waits up to 2 s for its output. The texts name the launcher's argv[0] only.
- `-install` still exits `0` with an empty stderr, and it writes no
  `[LauncherHandler]` line.

The direct `--version` run and its bounds are parity. A direct run is a run with
no usable launcher: the gate is unset, or the launcher status is `none`.
- On a cache hit a run that has not ended after 30 s is stopped. The facts then
  read `"cliWasPresent":false`, the direct `cli unresponsive: …` text and
  `"cliUnresponsive":true`. `-install` exits `0` with an empty stderr.
- The run of a CLI that this run installed is stopped at 120 s, with the same text.
  A new CLI that replaced a file at the final path that did not run gets the same
  bound (Linux).
- The download time is outside the bound (Linux and Windows). claustrum starts the
  clock right before the process start call.
- On Linux and macOS the CLI runs in a process group of its own, in the session of
  `-install`. A CLI that ignores SIGTERM stops at the same time. claustrum kills
  the group with SIGKILL (claustrum's own). A child in a new session survives.
- On Windows the stop ends the CLI process, and its children stay alive.
- A process that holds the output of the CLI after the CLI exited delays nothing.
  claustrum gives the CLI the null device as stdin, stdout and stderr (claustrum's
  own).
- After a stopped cache hit the CLI file stays, nothing is installed or pruned, a
  `-cli-zst` blob stays and the sweep runs. A `-cli-url` server gets no request
  (Linux and Windows).
- After a stopped first run the new CLI stays at its final path, the `-cli-zst`
  blob is consumed and nothing is pruned. With `-cli-url` the facts carry `fetch`
  after `cliUnresponsive`.

The default CLI folder:
- With `-cli-version` and no `-cli-dir`, or an empty `-cli-dir`, the CLI folder is
  `<home>/.claude/remote/ccd-cli`. On Windows `<home>` is `USERPROFILE`, and `HOME`
  is not read. The other steps work there as in an explicit folder.
- A cache miss with a version that D6 accepts makes the folder chain, with or without a source flag. Each new
  level gets mode `0700`, also with umask `000`, and a level that exists keeps its
  mode (Linux and macOS).

The CLI file name on Windows:
- The CLI file is `<cli-dir>\<version>.exe`. The `cliPath`, the file on disk, the
  path of the `--version` run and the `not runnable` text all carry that name. The
  `missing` text names the version with no suffix.
- The suffix is added always, so `-cli-version 9.9.9.exe` installs `9.9.9.exe.exe`.
  A version that ends in `.EXE` is not measured.
- Only `<version>.exe` counts as present. A file with the bare name counts for
  nothing and does not run. So a CLI that an older claustrum installed at the bare
  name answers `missing` until a source flag installs the `.exe` file beside it.
- A folder at the bare name stays. A folder at the `.exe` name is replaced by the
  CLI file.

Download progress and `fetch` stats, on the `-cli-url` path:
- While downloading, `-install` prints `__INSTALL_PROGRESS__<json>` lines to stdout:
  `{"phase":"download","bytes":<n>[,"total":<m>]}`. `total` carries the
  Content-Length. If the server sends none, `total` is dropped.
- A leading `bytes:0` line comes after the response headers and before any body
  byte. A server that sends no headers gets no line (Linux and Windows). A final
  `bytes:<n>` line comes only after the checksum passes. A failed check prints no
  final line (`f6010b97`, Linux).
- Between those two lines a tick comes each 1 s. If the byte count of a tick equals
  the count of the last printed line, the tick prints nothing. The counts jump irregularly, so a
  consumer treats them as progress, not as a byte-exact sequence. claustrum prints
  the final line also when a tick printed that count already, and it starts the
  tick clock at the first readable body (claustrum's own).
- The facts line gains a `fetch` object after `cliError`,
  `{"bytes":<n>,"ms":<n>,"longestPauseMs":<n>}`. Those are bytes read, download
  duration and the largest gap between reads. It is the last field, except with
  the managed launcher gate (see above). It appears whenever a download was
  attempted, even a 0-byte 404. A `-cli-zst` install and a cache hit have no
  `fetch`.
- claustrum does not close its idle connections after the download. Its transport
  is a copy of the Go default transport, whose idle limit is 90 s.
- The wait for the response headers has an always-on 60 s limit (Linux and
  Windows). A server that sends nothing fails the install with
  `download failed: Get "<url>": net/http: timeout awaiting response headers`. No
  progress line is printed, and `fetch` reads `"bytes":0` and
  `"longestPauseMs":0`.
- A body read that ends with a transport error fails the install with
  `download interrupted after <got>/<total> bytes: <transport err>` (Linux). The
  wait from the last byte to the error counts for `longestPauseMs`. One progress
  line is printed, and the cli-dir stays empty. claustrum words other read errors
  the same way, with a `<total>` of `0` for a body with no length (claustrum's
  own).
- The body has an always-on 60 s read-idle abort, reset on every byte. It fails
  the install with the `download stalled: …` text above. It is no total deadline,
  so a slow download that keeps sending completes. The total deadline is the
  opt-in [D12](DIVERGENCES.md#d12), off by default. When armed, it bounds the
  whole exchange, so an honest download that is too slow fails with
  `download failed: context deadline exceeded (…)`.

Checksum and verify ordering:
- claustrum verifies `-cli-checksum` on the `-cli-url` path unconditionally. An
  empty checksum still fails.
- The source is decompressed into the staging file first, and the checksum comes
  after it. A body that is no zstd archive answers
  `decompressing: invalid input: magic number mismatch` with a right and with a
  wrong checksum. `fetch` then counts the bytes that the decoder read, and only the
  leading progress line is printed (Linux). No chmod, clear or rename runs before
  the checksum passes (claustrum's own).
- If a `-cli-checksum` is supplied, claustrum also verifies the `-cli-zst` blob,
  as the reference does ([D1](DIVERGENCES.md#d1), retired). A mismatch
  keeps the blob. An absent or empty checksum verifies nothing.
- The compare is case-sensitive on both paths. The right digest in upper case
  answers `checksum mismatch: expected=<UPPER>, actual=<lower>` (`7d193f89` and
  `f6010b97`, Linux).
- Every cache miss with a CLI path and a version that D6 accepts makes the cli-dir
  first (mode `0700`, with its parents). That is before the source check, before
  the blob opens and before any network access (`5db5e4a` and later for
  `-cli-zst`, `f6010b97` for `-cli-url` and a missing source, Linux). So after that
  step a failed cache miss leaves the cli-dir. A new cli-dir stays empty.
- On Linux and macOS that folder step also runs on a cache hit. Windows makes the
  cli-dir on a cache miss only. On Linux and macOS the step uses the `-cli-dir` text
  as given, so `-cli-dir <dir>/sub/..` makes `<dir>/sub` (mode `0700`). On Windows
  `<dir>/sub` is not made. If the step fails, `cliPath` is empty and `cliError` is the mkdir error,
  such as `mkdir cli dir: mkdir <dir>/sub: not a directory`.

The other clocks: every `-install` clock above is always on and is a clock of the
reference, except the opt-in D12 deadline, the 2 s wait for the output of a
stopped launcher run and the 0.2 s bound of the rename retry (claustrum's own). The stdlib transport clocks
(`net.Dialer{Timeout:30s}`, `TLSHandshakeTimeout:10s`) apply on `-cli-url` only,
and they are not probed on the reference. `-cli-probe-timeout` and
`-libc-probe-timeout` are deprecated no-ops.

[D10](DIVERGENCES.md#d10) is the opt-in size cap. `-max-cli-bytes <n>`, or the
`max-cli-bytes` config key, governs both the decompressed CLI and the
download body. `0` is off, which is parity. claustrum streams the blob and never
buffers it, so "cap off" does not mean unbounded memory.

`-cli-version` hardening, [D6](DIVERGENCES.md#d6), is claustrum-only:
- The version must be one path component. Otherwise claustrum answers
  `cli version "…" must be a single path component`. It answers before the install
  writes or removes anything at a path built from the version. It writes nothing
  and keeps the blob. The reference has no such rule. With `../x` and a blob it
  installs to `<parent>/x`, outside the cli-dir. On `../victim` it destroys the
  target.
- It refuses `.`, `..`, `/` and `\` on every OS. A final component that is a
  symlink stays legal. `1.0.86`, `2.0.0-beta.1`, a commit sha, `latest` and
  `1.0.86+build.5` are accepted.
- A regular file at the joined path is first run with `--version`, as the cache-hit
  check. A run that passes answers `"cliWasPresent":true` with that path, as on the
  reference (Linux and macOS). A stopped run answers the unresponsive text. D6
  answers only when that run fails, or when no file is there.
- The two sweeps of the cli-dir name only the cli-dir, and they are not behind
  this rule.
- A version with a sweep name, such as `.fetch-1` or `1.0.0.zst`, installs. The
  same run keeps it, because the sweep removes only old entries. A later install
  removes it once it is more than 10 minutes old. On Windows the file is
  `<version>.exe`, and the sweep keeps it.

Staging and cleanup:
- claustrum stages the CLI at `<cli-dir>/.fetch-<random>` and renames it to the
  final path. That is one code path for `-cli-url` and `-cli-zst`. A download is
  decompressed into that file as it arrives, and no other file is made.
- An install puts the new CLI at its final path, removes the `-cli-zst` blob and
  runs the 10 minute sweep. Then `<final path> --version` runs. The prune follows a
  good run only. A direct run that does not start or exits non-zero removes the new CLI again with one plain
  remove. A stopped run keeps the new CLI. On Windows claustrum can leave that file
  when the exited CLI holds it (claustrum's own).
- What is at the final path goes before the new CLI runs. A folder there is removed
  as a tree, with its content. A file there that does not run is replaced by the
  new CLI (Linux). A new CLI that then exits non-zero leaves nothing at that path
  (claustrum's own).
- claustrum clears an occupied `cliPath` only when it is a directory, because
  `rename(2)` refuses to replace a directory. A regular file is replaced
  by the rename. A directory that claustrum cannot remove answers
  `clearing stale dir at <path>: <err>`. On Windows the cleared name is
  `<version>.exe`. The D6 rule runs first, so `cliPath` is a direct child of the
  cli-dir.
- claustrum does not remove a folder at the final path that is the home folder or
  contains it ([D2](DIVERGENCES.md#d2)). It answers
  `cli path must not be or contain the home directory: "<path>"`. The folder
  stays, the blob stays and no CLI runs. The reference removes that folder as a
  tree and puts the CLI file there. That is measured with `-cli-zst` on Linux,
  macOS and Windows, and `-cli-url` is not measured.
- That guard runs the lexical test of the RPC paths first. Then it compares the
  folder at the final path with the home folder and with each parent folder of it,
  by file identity, for the home path as given and for its resolved path.
- On macOS the volume ignores letter case, so a `-cli-version` in another letter
  case names a folder on disk. Both binaries replace such a folder that does not
  hold home, and the new file takes the name of the command. For a folder that
  holds home, claustrum refuses as above.
- The `-cli-zst` blob goes with one plain remove once the new CLI is in place.
  Every earlier failure keeps it: a blob that is no zstd archive, a folder that
  cannot be cleared and a denied rename. claustrum keeps it after a failed chmod
  too (claustrum's own). A CLI that then does not run still costs the blob.
- On Windows a rename that access denied stops is tried again for 0.2 s. If the
  target then holds the bytes of the staging file, the install counts as done and
  the staging file goes. The 0.2 s bound and the pauses are claustrum's own.
- A cli-dir path that names no folder is replaced. The trigger is a regular file, a
  symlink to a regular file or a FIFO at the cli-dir path. `-install` removes that
  entry, and for a symlink it removes the link and keeps the target. It also
  removes the regular file named exactly `ccd-cli-version` in the parent folder of
  the cli-dir. Then it makes the cli folder with mode `0700` and goes on. That holds
  for the default CLI folder too, and with no source flag.
- The name `ccd-cli-version` is literal. A file named `other-version`,
  `ccd-cli-version.bak` or `zzz-version` stays.
- A folder, a symlink to a folder and an absent cli-dir trigger nothing, and
  `ccd-cli-version` stays. A dangling symlink triggers nothing either, and the
  answer is `mkdir cli dir: mkdir <path>: file exists` with an empty `cliPath`.
  With a parent folder that is not writable nothing is removed, and the answer is
  `mkdir cli dir: mkdir <path>: not a directory`. Those `mkdir` texts are the Linux
  and macOS texts. Windows has no FIFO case and no case with a parent that is not
  writable.
- Both removes are plain removes of one path, never a tree. claustrum's own (not
  measured): it removes the version file first, and if that fails, the cli-dir
  entry stays. It leaves a socket, a device,
  a symlink to a FIFO and a `ccd-cli-version` that is a folder or a symlink alone.
- The `-cli-keep` prune:
    - Only a version file counts, and only a version file goes, with one plain
      remove, never as a tree. On Linux and macOS that is a regular file whose name
      is a version such as `1.0.0`, with an exec bit. On Windows it is a regular
      file `<version>.exe`. Every other entry stays and takes no keep place, for
      example a folder, a link, `README`, a `1.1.0` with mode `0644`, a bare
      `1.1.0` on Windows and `README.exe`.
    - claustrum counts a name of three runs of digits with dots between them. Other
      names are not measured.
    - The order is the mtime, newest first. Entries with one mtime stay in byte
      order of the name, so the later name goes (`89cb6289`). The new CLI takes its place in the
      order and always stays. `-cli-keep 0` and a negative value remove every
      version file but the new CLI, and the result line follows with exit `0` and an empty stderr.
    - A failed install, a stopped run and a cache hit do not prune.
- Two sweeps remove old regular files of the cli-dir with one plain remove each,
  never as a tree. A folder and a link stay (from the code for a link):
    - The 10 minute sweep takes `.fetch-<digits>` and `<version>.zst`. A file
      `.fetch-d`, a file `x.zst` and a folder `.fetch-9` stay.
    - The 7 day sweep takes `<version>.zst.part`. A file `p.zst.part` stays, also at 8 days old.
    - The 7 day sweep runs once before any CLI runs, also on a cache hit. On a good
      cache hit only that sweep runs. The 10 minute sweep runs before the run of a
      new CLI, after a failed attempt and after a D6 refusal. On a cache hit it runs
      only after a run that its bound stopped.
    - Neither sweep removes the `-cli-zst` blob, so a blob `1.0.0.zst` in the
      cli-dir stays.
    - The names match the letter case (claustrum's own). A future mtime is not old
      (`f6010b97`).
    - A concurrent install can take a staging file only once it is more than 10
      minutes old. claustrum then retries a `-cli-zst` staging step once.
- The sweeps and the prune skip an entry of the cli-dir that is the home folder or
  holds it ([D2](DIVERGENCES.md#d2), claustrum's own). They remove regular files
  only, so a home folder stays there on `5fd08069` too (derived from its
  cells, no home cell).

The libc probe fills the `libc` fact:
- claustrum runs `ldd` on every probe, and its output decides. A "musl" banner
  reports `musl`, and any other output reports `glibc`. The `/lib/ld-musl-*.so.*`
  marker is read only when `ldd` produced no output.
- The probe bounds ldd itself at 5 s, as the reference does since `19f30c46`
  (Linux). `ldd` runs in its own process group,
  and if it still runs at 5 s the whole group is killed. Output written before that
  kill is dropped, so the marker decides. No log line is written, and the facts
  line keeps its shape. The bound applies on a fresh install and on a cache hit.
- If `ldd` exits before 5 s while a child still holds its output pipe, the probe
  waits up to 2 s more. Then it uses the output it has, even past 5 s, and the
  child is not killed (`f6010b97`, Linux).

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
[managed launcher](protocol/launcher-resolve.md) (measured against `89cb6289` on a Linux VM unless a rule says otherwise). Every
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
