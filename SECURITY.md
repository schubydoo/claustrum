# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/schubydoo/claustrum/security/advisories/new)
(the "Report a vulnerability" button on the repository's Security tab). Do not
open a public issue for security reports.

You can expect an initial response within a few days. Once a fix is ready we'll
coordinate disclosure and credit you, if you'd like.

## Supported versions

Only the latest release on `main` receives security fixes.

## Scope & threat model

claustrum is trusted, host-local infrastructure, not a multi-tenant service. It
listens on an `AF_UNIX` socket (mode `0600`, owner-only) and supervises child
processes on the host it runs on.

### Trust boundary

`process.spawn` runs arbitrary commands as the daemon's user, by design — that is
the daemon's job (it hosts the agent and MCP servers). **Access to the socket plus
a valid token is therefore equivalent to shell access for that user.** Everything
below assumes an actor who does not already hold both; anyone who does can do
whatever the daemon's user can.

### Auth & tokens

- Auth is an in-band per-request token. The `-serve` daemon takes it from
  `-token-file` (unlinked immediately after reading) or `-token-fd` (read from an
  open descriptor and forwarded to the detached daemon over a pipe — this
  handoff never on disk, in argv, or in the environment).
- claustrum never reads a token from the environment. `CLAUDE_RPC_TOKEN` is read
  by no mode; `-bridge` is a dumb relay whose client carries its own `auth`; and
  the daemon unsets the variable before daemonizing and strips it from every
  spawned child.
- Protect the token and the socket together: whoever can read the token *and*
  reach the socket can drive the daemon. The socket is owner-only by design; keep
  the token source owner-readable and short-lived.
- The running daemon persists its token to `daemon.token` (mode `0600`) in the
  socket's directory, so a client can reconnect after the `-token-file` /
  `-token-fd` source is gone (parity with the reference daemon, upstream
  `5db5e4a`; see [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token persistence). It
  is written atomically and unlinked on graceful shutdown, but is **left behind on
  an unclean kill (`SIGKILL`) or crash** — cleanup runs only on the graceful path.
  This widens the on-disk token window versus the immediate-unlink of the source,
  so treat the socket directory as owner-only: it is where the token lives for the
  daemon's lifetime. On POSIX the file is `0600`; on Windows those bits are not an
  owner-only DACL (a Go `os.CreateTemp` limitation the reference shares), so
  confinement there comes from the session directory's ACL.
- `server.shutdown` is the one method the token does not gate: it is **not
  authenticated**, behavioral parity with the reference (Desktop stops the daemon
  with no token in its environment). So reaching the socket is by itself enough to
  stop the daemon and drop every session, and `-stop` sends no token. The socket's
  owner-only mode is what confines this; an actor who already shares the uid can
  do strictly more via `process.spawn`. The optional Windows named-pipe transport
  shares this dispatch, so the same exception applies. That the Desktop client
  relies on this teardown path is a driver claim (see
  [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#driver-claims-and-their-provenance)).

### Network

- `-install` reaches the network (HTTPS) only when given a `-cli-url`. That
  download is verified against its SHA-256 unconditionally, before extracting and
  before marking the CLI runnable — an empty `-cli-checksum` still fails.
- The local `-cli-zst` (SFTP) blob is checksum-verified only when a
  `-cli-checksum` is supplied; absent one it is trusted. This is an intentional
  conditional divergence (D1; see [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)).
- In `-serve` mode the daemon makes **no** outbound connections.

### Caller-supplied paths

`files.*` and `git.*` read and act on paths the caller supplies; they are as
privileged as the daemon's user. Two of those paths reach a recursive delete
(`os.RemoveAll`): `files.extract_tar` wipes its destination before unpacking, and
`git.worktree_remove` deletes the worktree path when git fails. `wipesHomeDir`
(`homeguard.go`) refuses any target that is or contains the home directory — an
always-on guard (D2; see [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)). Paths
under home stay allowed, because the daemon's own install path lives there.

### Optional surfaces

Every surface below is off by default and none is reachable unless the operator
enables it.

| Surface | Default | Platform | Security consequence |
|---|---|---|---|
| `-metrics-addr` | off | all | Adds an inbound HTTP listener serving Prometheus counters only, with **no authentication** — bind it to loopback. |
| `-listen-pipe` | off | Windows | Serves the same JSON-RPC over a Windows named pipe (same in-band token auth) for clients that cannot use the `AF_UNIX` socket; owner-only and local-only (see below). |
| `-keep-children` | off | POSIX | Adds no listener or auth path; children run as the daemon's user. On graceful shutdown they are left running and orphaned (reparented to init), so the operator owns their eventual cleanup. |
| `wantPid` (CT-1) | off | all | When an already-authenticated caller opts in, the result carries the child `pid` plus an opaque daemon `startTime` (for PID-reuse / orphan detection, not a credential) — no new secret. |

Details for the two surfaces that need more than a row:

- `-metrics-addr`: the counters are connection / spawn / exit / reattach / byte
  tallies only — no command output, arguments, or tokens. Exposing the endpoint on
  a reachable interface discloses coarse operational counts to anyone who can
  connect.
- `-listen-pipe`: the pipe carries an owner-only DACL (SDDL
  `D:P(A;;GA;;;<current-user-SID>)` — GENERIC_ALL to the daemon user's SID and to
  no Everyone / Authenticated-Users / anonymous principal), the named-pipe
  analogue of the socket's `0600` mode. It is local-only by two independent
  mechanisms: that DACL, and go-winio's `ListenPipe` creating the pipe with
  `FILE_PIPE_REJECT_REMOTE_CLIENTS`, so a client reaching it over SMB
  (`\\host\pipe\…`) is refused regardless of the DACL. It grants no access the
  socket + token did not already grant. When off, no pipe exists and behavior is
  byte-for-byte identical to the reference. The chosen pipe name is published to
  `rpc.pipe` in the socket directory (the name is not a secret — the DACL is the
  access control) and removed on graceful shutdown.

Reports that require already having the socket + token (or host shell access), or
that amount to "the operator can run commands on their own host," are generally
out of scope.
