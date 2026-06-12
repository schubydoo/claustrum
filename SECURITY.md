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
  disk, in argv, or in the environment); the `-bridge`/`-stop` clients read
  `CLAUDE_RPC_TOKEN`. Anyone who can read the token *and* reach the socket can
  drive the daemon. Protect both: the socket is owner-only by design; keep the
  token file (or fd source) owner-readable and short-lived.
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
