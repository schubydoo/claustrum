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

### Auth & tokens

- Auth is an in-band per-request token. The `-serve` daemon takes it from
  `-token-file` or from `-token-fd`. With `-token-file`, the file is unlinked
  immediately after reading. With `-token-fd`, the token is read from an open
  descriptor and forwarded to the detached daemon over a pipe. That handoff is
  never on disk, never in argv, and never in the environment.
- claustrum never reads a token from the environment. No mode reads
  `CLAUDE_RPC_TOKEN`. `-bridge` is a dumb relay, and its client carries its own
  `auth`. The daemon unsets the variable before it daemonizes, and it strips the
  variable from every spawned child.
- Protect the token and the socket together. Whoever can read the token *and*
  reach the socket can drive the daemon. The socket is owner-only by design. Keep
  the token source owner-readable and short-lived.
- The running daemon persists its token to `daemon.token` (mode `0600`) in the
  socket's directory. A client can therefore reconnect after the `-token-file`
  source or the `-token-fd` source is gone. This is parity with the reference
  daemon, upstream `5db5e4a`. See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token
  persistence. The file is written atomically and unlinked on graceful shutdown.
  An unclean kill (`SIGKILL`) or a crash leaves the file behind, because cleanup
  runs only on the graceful path. This widens the on-disk token window, compared
  with the immediate unlink of the source. Treat the socket directory as owner-only,
  because it is where the token lives for the daemon's lifetime. On POSIX the file
  is `0600`. On Windows those bits are not an owner-only DACL, which is a Go
  `os.CreateTemp` limitation the reference shares. Confinement on Windows
  therefore comes from the session directory's ACL.
- `server.shutdown` is the one method the token does not gate. It is not
  authenticated. That is behavioral parity with the reference, because Desktop
  stops the daemon with no token in its environment. Reaching the socket is
  therefore enough on its own to stop the daemon and drop every session, and
  `-stop` sends no token. The socket's owner-only mode is what confines this. An
  actor who already shares the uid can do strictly more through `process.spawn`.
  The optional Windows named-pipe transport shares this dispatch, so the same
  exception applies. The claim that the Desktop client relies on this teardown
  path is a driver claim. See
  [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#driver-claims-and-their-provenance).

### Network

- `-install` reaches the network (HTTPS) only with a `-cli-url`. That
  download is verified against its SHA-256 unconditionally, before extraction and
  before the CLI is marked runnable. An empty `-cli-checksum` still fails.
- The local `-cli-zst` (SFTP) blob is checksum-verified only with a supplied
  `-cli-checksum`. Without one, the blob is trusted. This is an intentional
  conditional divergence, D1. See [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md).
- In `-serve` mode the daemon makes no outbound network connections. Its only dial
  is the orphan-exit loopback self-probe. That probe is a connection to the
  daemon's own `AF_UNIX` socket, and it makes sure that a successor took the path
  over (`orphanexit.go`). It is never network egress.

### Caller-supplied paths

`files.*` and `git.*` read and act on paths the caller supplies. They are as
privileged as the daemon's user. Three of those paths reach a recursive delete
(`os.RemoveAll`):

- `files.extract_tar` wipes its destination before unpacking.
- When git fails for a non-locked reason, `git.worktree_remove` deletes the
  worktree path. A locked worktree is refused, not deleted.
- When `git.worktree_create` rolls back a worktree, it deletes the worktree path.
  That rollback happens after a failed `git worktree add`. It also happens after
  the post-checkout drain exceeds the caller `timeoutMs`. An add that the caller's
  `timeoutMs` cut short is the exception. Such an add answers `timeout` and leaves
  the leaf.

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
| `-keep-children` | off | POSIX | Adds no listener and no auth path. Children run as the daemon's user. On graceful shutdown they are left running and orphaned, reparented to init. The operator therefore owns their eventual cleanup. |
| `-wire-log` | off | all | Appends every JSON-RPC frame to a file for diagnostics. It is the inverse of `-metrics-addr`, because it captures frame payloads: `files.write`, `process.stdin`, and the spawn env. Those payloads are truncated at 512 bytes, unless you set `-wire-log-max-string=0`. Credentials are redacted by key only, so a secret inside a payload string is not caught. The file is forced to `0600` on every open, append included. Treat it as sensitive. |
| `wantPid` (CT-1) | off | all | When an already-authenticated caller opts in, the result carries the child `pid` and an opaque daemon `startTime`. They serve the detection of PID reuse and orphans, and they are not a credential. There is no new secret. |

Details for the three surfaces that need more than a row:

- `-metrics-addr`: the counters are tallies of connections, spawns, exits,
  reattaches, and bytes only. They hold no command output, no arguments, and no
  tokens. Exposing the endpoint on a reachable interface discloses coarse
  operational counts to anyone who can connect.
- `-wire-log`: unlike the counters, a capture contains the frame payloads. Those
  payloads are arguments, file contents, and the `process.spawn` environment.
  Redaction is by key, on the `auth` member and on token-like env keys. It
  therefore withholds those keys, but it cannot find a credential embedded in a
  free-form payload string. Anyone who reads the file gains what the client sent.
  Keep the flag off unless you need it. Store a capture with the same care as the
  socket token.
- `-listen-pipe`: the pipe carries an owner-only DACL, in SDDL
  `D:P(A;;GA;;;<current-user-SID>)`. That DACL grants GENERIC_ALL to the daemon
  user's SID, and to no Everyone principal, no Authenticated-Users principal, and
  no anonymous principal. That DACL is the named-pipe analogue of the socket's
  `0600` mode. The pipe is local-only by two independent mechanisms. The first mechanism
  is that DACL. The second is go-winio's `ListenPipe`, which creates the pipe with
  `FILE_PIPE_REJECT_REMOTE_CLIENTS`. A client that reaches the pipe over SMB
  (`\\host\pipe\…`) is therefore refused, regardless of the DACL. The pipe grants
  no access that the socket plus the token did not already grant. With the flag
  off, no pipe exists, and behavior is byte-for-byte identical to the reference.
  The chosen pipe name is published to `rpc.pipe` in the socket directory, and it
  is removed on graceful shutdown. The name is not a secret, because the DACL is
  the access control.

Some reports require the socket plus the token already, or host shell access.
Other reports amount to "the operator can run commands on their own host." Both
kinds are generally out of scope.
