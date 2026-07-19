<h1 align="center">claustrum</h1>

<p align="center">
  <em>A tiny, dependency-light Go daemon that hosts a remote Claude Code session over SSH —<br>
  a local CLI-version manager + process supervisor + JSON-RPC multiplexer (with a replay<br>
  buffer) over a Unix socket. An independent, clean-room implementation you can run yourself.</em>
</p>

<p align="center">
  <a href="https://github.com/schubydoo/claustrum/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/schubydoo/claustrum/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://codecov.io/gh/schubydoo/claustrum"><img alt="codecov" src="https://codecov.io/gh/schubydoo/claustrum/graph/badge.svg"></a>
  <a href="https://greptile.com"><img alt="Reviewed by Greptile" src="https://img.shields.io/badge/Greptile-reviewed-7C3AED"></a>
  <a href="https://schubydoo.github.io/claustrum/"><img alt="Docs" src="https://img.shields.io/badge/docs-mkdocs--material-526CFE?logo=materialformkdocs&logoColor=white"></a>
  <a href="https://github.com/schubydoo/claustrum/blob/main/LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue.svg"></a>
  <a href="https://go.dev/"><img alt="Go" src="https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white"></a>
  <img alt="platforms" src="https://img.shields.io/badge/platforms-linux%20%C2%B7%20macOS%20%C2%B7%20windows-555">
</p>

> **Independent & unaffiliated.** claustrum is a clean-room implementation. It is **not**
> affiliated with, authorized by, or endorsed by Anthropic. "Claude", "Claude Code", and
> "Claude Desktop" are trademarks of Anthropic, PBC, used here only to describe
> interoperability. See [NOTICE](NOTICE).

---

## What it is

When you drive a remote Claude Code session over SSH, a small Go daemon runs on the remote
host. It isn't a network relay — it's **local plumbing**:

- **CLI-version manager** — downloads/verifies/extracts the pinned `claude` CLI, prunes old
  versions.
- **Process supervisor** — spawns and manages the agent (and any MCP-server) child processes,
  owning their stdio.
- **JSON-RPC multiplexer** — speaks newline-delimited JSON-RPC 2.0 over an `AF_UNIX` socket,
  fanning many clients/streams over one connection, with a **replay buffer** so a late or
  reconnecting client can catch up.

`claustrum` is a from-scratch, behaviorally-compatible implementation of that daemon, so it can
be used **independently** — e.g. as a building block for self-hosted tooling like
[clauster](https://github.com/schubydoo/clauster). It is validated to produce
**byte-identical** JSON-RPC frames for every method (see [Validation](#validation)).

> **Status: stable (v1.0+).** The JSON-RPC/process/file/git surface is complete and validated; the
> CLI-version installer is implemented and behavior-checked. No telemetry, ever.

## Install / build

Requires Go 1.25+. Dependencies: `github.com/klauspost/compress` (zstd) and
`golang.org/x/sys` (Windows Job Object teardown — only compiled into Windows builds).

```sh
# build the native binary
make build          # -> ./claustrum   (CGO off, -trimpath, stripped)

# or cross-build all six targets into ./dist/
make all            # linux/darwin/windows × amd64/arm64

# or straight go
go build -o claustrum .
go install github.com/schubydoo/claustrum@latest
```

`claustrum -version` prints `claustrum <git-sha> (built <iso8601>)` (the SHA/time come from
the embedded VCS build info).

## Usage

One binary, mode-switched by flag:

```text
claustrum -serve   -socket <path> -token-file <path>   # self-daemonize, run the RPC server
claustrum -bridge  -socket <path>                       # dumb stdio<->socket relay (what SSH attaches)
claustrum -stop    -socket <path>                       # ask a running daemon to shut down
claustrum -install -cli-dir <dir> -cli-version <v> [-cli-url <url> -cli-checksum <sha256>] [-cli-zst <file>] [-cli-keep <n>]
claustrum -version
```

### Start a daemon and talk to it

```sh
# 1. a private socket + auth token
D=$(mktemp -d); printf '%s' "$(uuidgen)" > "$D/token"

# 2. start the daemon (self-daemonizes; reads + unlinks the token file)
claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token" &

# 3. speak JSON-RPC over the socket (auth is in-band, per request)
TOK=$(cat "$D/token" 2>/dev/null || true)   # already unlinked; use the value you generated
printf '{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"%s"}\n' "$TOK" \
  | socat - UNIX-CONNECT:"$D/rpc.sock"
# -> {"jsonrpc":"2.0","id":1,"result":{"pong":true}}

# 4. enumerate everything the daemon implements
printf '{"jsonrpc":"2.0","id":2,"method":"server.capabilities","auth":"%s"}\n' "$TOK" \
  | socat - UNIX-CONNECT:"$D/rpc.sock"

# 5. shut it down
claustrum -stop -socket "$D/rpc.sock"   # reads CLAUDE_RPC_TOKEN for auth
```

More worked examples — spawning a process and reading its base64 output stream, reattaching to
catch up via the replay buffer, extracting a plugin tarball — are in
**[docs/PROTOCOL.md](docs/PROTOCOL.md)** and **[docs/EXAMPLES.md](docs/EXAMPLES.md)**.

## How it works

- **Transport:** NDJSON over `AF_UNIX` `SOCK_STREAM` (mode `0600`); one persistent connection;
  requests dispatched concurrently.
- **Auth:** every request carries an in-band `"auth":"<token>"`; the daemon's token comes from
  `-token-file` (read once, then unlinked) or `-token-fd` (read from an open descriptor —
  never touches disk); the `-bridge`/`-stop` clients use `CLAUDE_RPC_TOKEN`.
- **19 methods** across `server.*`, `files.*`, `git.*`, `process.*` (`server.capabilities`
  self-describes them).
- **process.\*** is the core: a client supplies its own `id` on `spawn`; the daemon streams
  id-less `{"type":"stream",…}` notifications (base64 stdout/stderr + an `exit`), buffers them,
  and replays on `reattach{fromSeq}`. This is how both the agent and MCP servers are hosted.
- **Operational knobs** (claustrum-only, all off the wire): `CLAUSTRUM_LOG_LEVEL` quiets the
  leveled stderr diagnostics, `-metrics-addr` opts into a local Prometheus `/metrics`
  endpoint (no listener exists without it), and `-keep-children` (CT-2, POSIX-only) makes a
  graceful shutdown leave spawned children running so they survive a daemon restart (off by
  default — shutdown kills them, unchanged; ignored with a warning on Windows).
- **Protocol extensions** (claustrum-only, opt-in — **additions**, *not* part of the reference
  contract it mimics): `process.spawn` / `process.reattach` accept `"wantPid":true`, which adds
  `pid` + `startTime` to the result for PID-reuse / orphan detection (CT-1). These are pure
  additions — a client that doesn't opt in sees **byte-identical** frames, so the guarantee below
  is unaffected. See [docs/PROTOCOL.md](docs/PROTOCOL.md) and the
  [divergence catalog](docs/IMPROVEMENTS.md#deliberate-divergences-post-parity-opt-in).

Full details: **[docs/PROTOCOL.md](docs/PROTOCOL.md)** and **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**.

## Platforms

Cross-compiles to **linux**, **macOS (darwin)**, and **windows** on **amd64** and **arm64** (6
targets). It's a static `CGO_ENABLED=0` Go binary. OS-specific behavior (daemonize, process
groups on Unix / Job Objects on Windows for whole-tree kill, login-shell PATH extraction) is
isolated in `*_unix.go` / `*_windows.go` files; the JSON-RPC surface is identical everywhere.

## Validation

`claustrum` is checked against a reference daemon with a request **battery** that exercises every
method, error path, and the full process lifecycle, then diffs normalized frames. Current status:
**byte-identical across all methods**, plus a behavior-identical `-install` path. The harness
lives in `scratch/` (local, not published).

An **in-repo test suite** (run in CI on every PR, on linux, macOS, and Windows) locks the same
contract without the reference binary: a socket-integration battery boots the daemon and asserts
every method's frames against committed golden fixtures, alongside unit tests for the install
pipeline and the bridge/stop clients (~80% statement coverage). See
[docs/UPSTREAM-TRACKING.md](docs/UPSTREAM-TRACKING.md) for how compatibility is kept in sync over
time.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Issues and PRs welcome.

## Security

See [SECURITY.md](SECURITY.md) for the threat model and how to report a vulnerability privately.

## License

[Apache License 2.0](LICENSE) · © 2026 Schuby. See [NOTICE](NOTICE) for the independence &
trademark statement.
