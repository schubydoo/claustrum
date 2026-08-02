# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities **privately** through GitHub's
[private vulnerability reporting](https://github.com/schubydoo/claustrum/security/advisories/new)
(the "Report a vulnerability" button on the repository's **Security** tab). Do
**not** open a public issue for security reports.

You can expect an initial response within a few days. Once a fix is ready we'll
coordinate disclosure and credit you, if you'd like.

## Supported versions

Only the latest release on `main` receives security fixes.

## Scope & threat model

claustrum is **trusted, host-local infrastructure**, not a multi-tenant service.
It listens on an `AF_UNIX` socket (mode `0600`, owner-only) and supervises child
processes on the host it runs on. Key considerations:

- **Auth** is an in-band per-request token. The `-serve` daemon takes it from
  `-token-file` (unlinked immediately after reading) or `-token-fd` (read from an
  open descriptor and forwarded to the detached daemon over a pipe — never on
  disk, in argv, or in the environment); the `-bridge` client reads
  `CLAUDE_RPC_TOKEN`. Anyone who can read the token *and* reach the socket can
  drive the daemon. Protect both: the socket is owner-only by design; keep the
  token file (or fd source) owner-readable and short-lived.
- **`server.shutdown` is the one method the token does not gate.** It is not
  authenticated — behavioral parity with the reference, which the Desktop client
  depends on: it tears the daemon down by invoking `server --stop --socket
  <sock>` with no token in its environment. So for that one method, reaching the
  socket is by itself sufficient to stop the daemon and drop every session, and
  `-stop` sends no token at all. The socket's owner-only mode is what confines
  this; an actor who already shares the uid can do strictly more via
  `process.spawn` below. The same exception applies to the optional Windows
  named-pipe transport, which shares this dispatch.
- **The running daemon persists its token to `daemon.token` (mode `0600`) in the
  socket's directory** so a client can reconnect and re-authenticate after the
  `-token-file`/`-token-fd` source is gone (behavioral parity with the reference
  daemon — added upstream `5db5e4a`; see [`docs/PROTOCOL.md`](docs/PROTOCOL.md) →
  Token persistence). It is written atomically and **unlinked on graceful
  shutdown**, but is **left behind on an unclean kill (`SIGKILL`) or crash**,
  since cleanup runs only on the graceful path. This widens the on-disk token
  window versus the immediate-unlink of the source, so treat the socket directory
  as owner-only: it is where the token now lives for the daemon's lifetime. On
  POSIX the file is `0600`; on Windows those bits are **not** an owner-only DACL
  (a Go `os.CreateTemp` limitation the reference shares), so confinement there
  comes from the session directory's ACL, not the file mode.
- **`process.spawn` runs arbitrary commands** as the daemon's user, by design —
  that is the daemon's job (it hosts the agent and MCP servers). Treat access to
  the socket + token as equivalent to shell access for that user.
- **`-install` reaches the network** (HTTPS) only when given a `-cli-url`, and it
  verifies the downloaded blob against the supplied `-cli-checksum` (SHA-256)
  before extracting and before marking the CLI runnable. In `-serve` mode the
  daemon makes **no** outbound connections.
- **`-metrics-addr` is off by default.** No inbound network listener exists
  unless the operator opts in with `-metrics-addr`. When enabled it serves
  Prometheus *counters only* (connection / spawn / exit / reattach / byte tallies
  — no command output, arguments, or tokens) and has **no authentication**, so
  bind it to a trusted interface (loopback). Exposing it on a reachable interface
  discloses coarse operational counts to anyone who can connect.
- **`wantPid` (CT-1) discloses no new secret.** When an already-authenticated
  caller opts in, the result carries the child's OS `pid` plus an opaque daemon
  `startTime` token — used for PID-reuse / orphan detection. A socket + token
  holder can already spawn and observe processes, so this is no new exposure, and
  `startTime` is a daemon-internal token, not a credential.
- **`-listen-pipe` is off by default** and Windows-only. When set, the daemon
  opens an *additional* Windows named pipe serving the same JSON-RPC (same in-band
  token auth) so a client that cannot consume the `AF_UNIX` socket can still
  connect. The pipe is created with an **owner-only DACL** (SDDL
  `D:P(A;;GA;;;<current-user-SID>)` — GENERIC_ALL to the daemon user's SID and to
  no Everyone / Authenticated-Users / anonymous principal), the named-pipe
  analogue of the socket's `0600` mode. It is **local-only** by two independent
  mechanisms: that owner-only DACL, **and** remote-client rejection at pipe
  creation — go-winio's `ListenPipe` creates the server pipe with
  `FILE_PIPE_REJECT_REMOTE_CLIENTS`, so a client reaching it over SMB
  (`\\host\pipe\…`) is refused regardless of the DACL. It therefore grants no
  access the socket + token
  didn't already grant — it is the same authenticated surface reached over a
  different local transport. When off, no pipe exists and behavior is byte-for-byte
  identical to the reference. The chosen pipe name is published to `rpc.pipe` in
  the socket directory (the name is not a secret — the DACL is the access control)
  and removed on graceful shutdown.
- **`-keep-children` is off by default** and adds no attack surface — no listener,
  no new auth path, and the children still run as the daemon's user (unchanged
  from `process.spawn` above). The one operational consequence: when set, a
  graceful shutdown leaves those processes **running and orphaned** (reparented to
  init), so they outlive the daemon and are no longer reachable via its
  `process.kill`/socket. That is the intended behavior (surviving a daemon
  restart), but the operator owns their eventual cleanup. POSIX-only.
- **`files.*` / `git.*`** read and act on paths the caller supplies; they are as
  privileged as the daemon's user.

Reports that require already having the socket + token (or host shell access), or
that amount to "the operator can run commands on their own host," are generally
out of scope.
