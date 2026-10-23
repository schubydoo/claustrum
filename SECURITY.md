# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/schubydoo/claustrum/security/advisories/new)
(the "Report a vulnerability" button on the repository's Security tab). Do not
open a public issue for security reports.

You can expect an initial response within a few days. When a fix is ready, we
will coordinate disclosure. If you want credit, we will credit you.

## Supported versions

Only the latest release on `main` receives security fixes.

## Scope & threat model

claustrum is trusted, host-local infrastructure, not a multi-tenant service. It
listens on an `AF_UNIX` socket (mode `0600`, owner-only) and supervises child
processes on the host it runs on.

### Trust boundary

`process.spawn` runs arbitrary commands as the daemon's user, by design. That is
the daemon's job, because it hosts the agent and the MCP servers. Access to the
socket plus a valid token is therefore equivalent to shell access for that user.
Everything below assumes an actor who does not already hold both. An actor who
holds both can do whatever the daemon's user can.

A caller passes a managed launcher to `process.spawn` in its `launcher` param.
The child then runs through that program, as on the reference. The daemon
refuses a launcher that fails its checks, and it refuses every launcher on
Windows. The daemon does not enforce the launcher. A spawn without the param
runs the command directly. The param gives a caller no power that the socket and
token did not already give. See
[process.spawn](docs/protocol/process-spawn.md#the-managed-launcher) and
[launcher.resolve](docs/protocol/launcher-resolve.md).

### Auth & tokens

- Auth is an in-band per-request token. The `-serve` daemon takes it from
  `-token-file`, which it unlinks right after the read, or from `-token-fd`. A
  `-token-fd` token goes to the detached daemon over a pipe, never on disk, in
  argv or in the environment.
- No mode reads `CLAUDE_RPC_TOKEN`. `-bridge` is a relay, and its client carries
  its own `auth`. The daemon unsets the variable before it daemonizes, and it
  strips the variable from every spawned child.
- Protect the token and the socket together. Whoever can read the token *and*
  reach the socket can drive the daemon. The socket is owner-only by design. Keep
  the token source owner-readable and short-lived.
- The running daemon persists its token to `daemon.token` (mode `0600`) in the
  socket's directory, so a client can reconnect after the token source is gone.
  This is parity with the reference daemon. See
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token persistence. At a graceful
  shutdown, a daemon whose own file is still in place unlinks it. A `SIGKILL` or a crash leaves the file behind.
  The token therefore stays on disk longer than its source, so keep the socket
  directory owner-only. On Windows the `0600` bits are not an owner-only DACL, so
  the ACL of the session directory confines the file.
- `server.shutdown` is the one method the token does not gate. It is not
  authenticated. That is behavioral parity with the reference. Desktop stops the
  daemon with no token in its environment. Reaching the socket stops the
  daemon and drops every session, unless the Linux peer check refuses the
  caller. `-stop` sends no token. The socket's owner-only mode is what confines this. An
  actor who already shares the uid can do strictly more through `process.spawn`.
  The optional Windows named-pipe transport shares this dispatch, so the same
  exception applies. The claim that the Desktop client relies on this teardown
  path is a driver claim. See
  [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#driver-claims-and-their-provenance).

### Network

- `-install` reaches the network (HTTPS) only with a `-cli-url`. That
  download is verified against its SHA-256 unconditionally, before the CLI is
  marked runnable. An empty `-cli-checksum` still fails.
- The local `-cli-zst` (SFTP) blob is checksum-verified only with a supplied
  `-cli-checksum`. Without one, the blob is trusted. The reference does the same.
- In `-serve` mode the daemon makes no outbound network connections. Every dial
  it makes is to a local `AF_UNIX` socket.

### Caller-supplied paths

`files.*` and `git.*` read and act on paths the caller supplies. They are as
privileged as the daemon's user. Four of those paths reach a recursive delete
(`os.RemoveAll` or `os.Root.RemoveAll`):

- `files.extract_tar` wipes its destination before unpacking. The wipe goes
  through a handle of the parent folder, which follows no link and no junction.
- `git.worktree_remove` deletes the worktree path, and then its entry under the
  git directory. Each delete goes through an `os.Root`, so no delete follows a
  symlink out of the worktree. A worktree locked in the `.git` folder of `baseRepo`
  is refused, not deleted.
- When `git.worktree_create` rolls back a worktree, it deletes the worktree path
  and its registration. A rollback follows a failure after a successful
  `git worktree add`, for example an expired `timeoutMs` or a failed checkout.
  Before the add, the daemon also removes a stale registration that names the
  new worktree. If two entries name it, or the entry holds a `locked` file,
  nothing goes. On Windows an entry whose record names a path that exists
  stays too (D27). Each rollback delete after a good add goes through an
  `os.Root`, after the home guard (D24 adds an identity test). The one exception
  is the registration delete of a create that holds no git directory and
  accepted no registration. That delete
  runs no home guard, and on Windows it uses no `os.Root`. It tests the
  `gitdir` record and the registrations directory. After a failed add, the daemon
  removes the worktree path only as an empty directory. See
  [git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure).
- On Windows, in the state `shadow` and after a good `git worktree add`,
  `git.worktree_create` removes old staging entries from the git directory of
  `baseRepo`, through its held handle of that folder. Their names start with `.claude-new-worktree-`, and
  their own write time is 1 hour old or more. Only a real folder goes as a tree.
  A file, a link or a junction goes with a plain remove, so the target of a
  junction stays.

`wipesHomeDir` (`homeguard.go`) refuses any target that is or contains the home
directory. It is an always-on guard (D2). See
[`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Paths
under home stay allowed, because the daemon's own install path lives there.

### Optional surfaces

Every surface below is off by default and none is reachable unless the operator
enables it.

| Surface | Default | Platform | Security consequence |
|---|---|---|---|
| `-metrics-addr` | off | all | Adds an inbound HTTP listener. That listener serves Prometheus counters only, with no authentication. Bind it to loopback. |
| `-listen-pipe` | off | Windows | Serves the same JSON-RPC over a Windows named pipe, with the same in-band token auth. It is for clients that cannot use the `AF_UNIX` socket. It is owner-only and local-only. See below. |
| `-keep-children` | off | all | Adds no listener and no auth path. Children run as the daemon's user. On graceful shutdown they are left running and orphaned. On Linux and macOS they are reparented to init. The operator therefore owns their eventual cleanup. |
| `-wire-log` | off | all | Appends every JSON-RPC frame to a file for diagnostics. It is the inverse of `-metrics-addr`, because it captures frame payloads: the `content` of a `files.read` reply, `process.stdin`, and the spawn env. Those payloads are truncated at 512 bytes, unless you set `-wire-log-max-string=0`. Credentials are redacted by key only, so a secret inside a payload string is not caught. The file is forced to `0600` on every open, append included. Treat it as sensitive. |
| `wantPid` (CT-1) | off | all | When an already-authenticated caller opts in, the result carries the child `pid` and an opaque daemon `startTime`. They serve the detection of PID reuse and orphans, and they are not a credential. There is no new secret. |

Details for the three surfaces that need more than a row:

- `-metrics-addr`: the counters are tallies of connections, spawns, exits,
  reattaches, and bytes only. They hold no command output, no arguments, and no
  tokens. Exposing the endpoint on a reachable interface discloses coarse
  operational counts to anyone who can connect.
- `-wire-log`: redaction is by key. It covers the `auth` member and any key
  whose name holds token, secret, password, passwd, credential or api_key, at
  any depth. With `-wire-log-max-string=0`, only a string value is redacted, so
  an object or a number under such a key stays in the file. A secret inside a
  payload string is not caught. A reader of the file sees what the client sent and what the daemon sent back,
  such as file contents and child output. Keep the flag off unless you need it,
  and store a capture with the same care as the socket token.
- `-listen-pipe`: the pipe carries an owner-only DACL, in SDDL
  `D:P(A;;GA;;;<current-user-SID>)`. It grants GENERIC_ALL to the SID of the
  daemon user only, as the `0600` mode does for the socket. go-winio's
  `ListenPipe` also creates the pipe with `FILE_PIPE_REJECT_REMOTE_CLIENTS`, so
  a client over SMB (`\\host\pipe\…`) is refused. The pipe grants no access that
  the socket plus the token did not already grant. With the flag off, no pipe
  exists. The pipe name is published to `rpc.pipe` in the socket directory and
  removed at a graceful shutdown. The name is not a secret, because the DACL is
  the access control.

Some reports require the socket plus the token already, or host shell access.
Other reports amount to "the operator can run commands on their own host." Both
kinds are generally out of scope.
