<h1 align="center">claustrum</h1>

<p align="center">
  <em>A tiny, dependency-light Go daemon that hosts a remote Claude Code session over SSH.<br>
  It is a local CLI-version manager, a process supervisor, and a JSON-RPC multiplexer<br>
  with a replay buffer, over a Unix socket.<br>
  An independent, clean-room implementation you can run yourself.</em>
</p>

<p align="center">
  <a href="https://github.com/schubydoo/claustrum/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/schubydoo/claustrum/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://codecov.io/gh/schubydoo/claustrum"><img alt="codecov" src="https://codecov.io/gh/schubydoo/claustrum/graph/badge.svg"></a>
  <a href="https://www.greptile.com/?utm_source=oss_badge&utm_medium=readme&utm_campaign=greptile_for_open_source"><img alt="Greptile: The War on Bugs" src="https://www.greptile.com/badge.svg"></a>
  <a href="https://www.bestpractices.dev/projects/14131"><img alt="OpenSSF Best Practices" src="https://www.bestpractices.dev/projects/14131/badge"></a>
  <a href="https://schubydoo.github.io/claustrum/"><img alt="Docs" src="https://img.shields.io/badge/docs-mkdocs--material-526CFE?logo=materialformkdocs&logoColor=white"></a>
  <a href="https://github.com/schubydoo/claustrum/blob/main/LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue.svg"></a>
  <a href="https://go.dev/"><img alt="Go" src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white"></a>
  <img alt="platforms" src="https://img.shields.io/badge/platforms-linux%20%C2%B7%20macOS%20%C2%B7%20windows-555">
</p>

> Independent and unaffiliated. claustrum is a clean-room implementation. It is not
> affiliated with, authorized by, or endorsed by Anthropic. "Claude", "Claude Code", and
> "Claude Desktop" are trademarks of Anthropic, PBC. This document uses them only to
> describe interoperability. See [NOTICE](NOTICE).

---

## What it is

When you drive a remote Claude Code session over SSH, a small Go daemon runs on the remote
host. It is not a network relay. It is local plumbing:

- CLI-version manager. It downloads, verifies, and extracts the pinned `claude` CLI, and it
  prunes old versions.
- Process supervisor. It spawns and manages the agent child process and any MCP-server child
  processes, and it owns their stdio.
- JSON-RPC multiplexer. It speaks newline-delimited JSON-RPC 2.0 over an `AF_UNIX` socket, and
  it fans many clients and streams over one connection. A replay buffer lets a late client or
  a reconnecting client catch up.

`claustrum` is a from-scratch, behaviorally-compatible implementation of that daemon. It
produces byte-identical JSON-RPC frames for every method, apart from a small set of
deliberate divergences in [docs/DIVERGENCES.md](docs/DIVERGENCES.md). You can also use it on
its own, for example as a building block for self-hosted tooling such as
[clauster](https://github.com/schubydoo/clauster).

> Status: stable (v1.0+). The JSON-RPC, process, file and git surface and the CLI-version
> installer are complete. No telemetry, ever.

## Install / build

Claustrum requires Go 1.26. The toolchain is held below 1.27, because the default
`jsonv2` of Go 1.27 moves inherited wire bytes. See
[docs/UPSTREAM-TRACKING.md](docs/UPSTREAM-TRACKING.md). Build with the `go.mod` toolchain.

Two dependencies are cross-platform: `github.com/klauspost/compress` for zstd, and
`golang.org/x/sys` for system calls. Windows builds also compile in
`github.com/Microsoft/go-winio`, for the opt-in `-listen-pipe` named-pipe transport (CT-5).

```sh
# build the native binary
make build          # -> ./claustrum   (CGO off, -trimpath, stripped)

# or cross-build all six targets into ./dist/
make all            # linux/darwin/windows × amd64/arm64

# or straight go
go build -o claustrum .
go install github.com/schubydoo/claustrum@latest
```

`claustrum -version` prints `claustrum <version> (built <iso8601>)`. A local `go build` stamps
the VCS SHA and time, and a release carries its tag. `go install …@vX.Y.Z` reports the module
version and the tag time. A pseudo-version such as `@main` prints `built unknown`.

A plain `go install` binary takes the host cgo defaults, has no `-trimpath`, and is not
stripped. Pass the release flags for a build equal to a release:

```sh
CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" github.com/schubydoo/claustrum@latest
```

## Usage

There is one binary. A flag selects the mode:

```text
claustrum -serve   -socket <path> -token-file <path>   # self-daemonize, run the RPC server
claustrum -bridge  -socket <path>                       # dumb stdio<->socket relay (what SSH attaches)
claustrum -stop    -socket <path>                       # ask a running daemon to shut down
claustrum -install -cli-version <v> [-cli-dir <dir>] [-cli-url <url> -cli-checksum <sha256>] [-cli-zst <file>] [-cli-keep <n>]
claustrum -version
claustrum -probe-cli <cli>                              # probe <cli> --version (30s direct, 33s via a managed launcher): empty=runs / __CLI_HUNG__ / __CLI_BAD__ / __CLI_LAUNCHER__; exit 0
```

### Start a daemon and talk to it

```sh
# 1. a private socket + auth token
D=$(mktemp -d); TOK=$(uuidgen); printf '%s' "$TOK" > "$D/token"

# 2. start the daemon (self-daemonizes; reads + unlinks the token file)
claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token" &

# 3. speak JSON-RPC over the socket (auth is in-band, per request)
# reuse the token generated in step 1 (the daemon unlinked the file when it read it)
printf '{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"%s"}\n' "$TOK" \
  | socat - UNIX-CONNECT:"$D/rpc.sock"
# -> {"jsonrpc":"2.0","id":1,"result":{"pong":true}}

# 4. enumerate everything the daemon implements
printf '{"jsonrpc":"2.0","id":2,"method":"server.capabilities","auth":"%s"}\n' "$TOK" \
  | socat - UNIX-CONNECT:"$D/rpc.sock"

# 5. shut it down
claustrum -stop -socket "$D/rpc.sock"   # no token needed: shutdown is unauthenticated
```

[docs/EXAMPLES.md](docs/EXAMPLES.md) has more sessions: spawn a process and read its stream,
reattach through the replay buffer, and extract a plugin tarball.

## How it works

- Transport: NDJSON over `AF_UNIX` `SOCK_STREAM`, at mode `0600`. There is one persistent
  connection. Requests dispatch concurrently.
- Auth: every request carries an in-band `"auth":"<token>"`. The token comes from
  `-token-file` or `-token-fd`, and [SECURITY.md](SECURITY.md) describes the handoff.
  `server.shutdown` is the one method without auth, as on the reference, so `-stop` sends
  no token.
- The daemon has 20 methods, across `server.*`, `files.*`, `git.*`, `launcher.*`,
  `process.*`, and `plugins.*`. `server.capabilities` self-describes them. Each method has
  a page under [docs/protocol/](docs/protocol/).
- `process.*` is the core. A client supplies its own `id` on `spawn`. The daemon streams id-less
  `{"type":"stream",…}` notifications, which carry base64 stdout and stderr and an `exit`. The
  daemon buffers those notifications and replays them on `reattach{fromSeq}`. This is how the
  daemon hosts both the agent and the MCP servers.

### Operational knobs

All of these are off by default. Each one is claustrum's own and leaves the frames of a
client unchanged:

- `CLAUSTRUM_LOG_LEVEL` raises the threshold of the leveled stderr log. Logging is always on.
- `-metrics-addr` opts into a local Prometheus `/metrics` endpoint. Without the flag, no
  listener exists.
- `-keep-children` (CT-2) leaves spawned children running across a graceful shutdown.
- `-listen-pipe` (CT-5, Windows only) also serves the same JSON-RPC over a named pipe.
- `-wire-log` appends every JSON-RPC frame to a file for diagnostics. It redacts
  credentials by key only.

One opt-in member of claustrum's own changes a frame: `"wantPid":true` adds `pid` and
`startTime` to the result of `process.spawn`, and of `process.reattach` for a found process (CT-1). Three more flags opt into a deliberate
divergence: `-max-extract-bytes` (D3), `-max-cli-bytes` (D10) and `-cli-download-timeout`
(D12). [docs/DIVERGENCES.md](docs/DIVERGENCES.md) has each entry and its limits. The
[flags table](docs/PROTOCOL.md#flags-and-config-keys) lists the token, knob, divergence and
deprecated flags with their `claustrum.conf` keys. [-install](docs/PROTOCOL.md#-install-ensure-the-agent-cli)
has the `-cli-*` flags.

## Platforms

Claustrum cross-compiles to linux, macOS (darwin), and windows, on amd64 and arm64. It is a
static `CGO_ENABLED=0` Go binary, and every OS serves the same JSON-RPC methods.

## Tests

CI runs the in-repo tests on every PR, on linux, macOS, and Windows. A socket-integration suite
boots the daemon and asserts the frames of every method against committed golden fixtures.
[docs/UPSTREAM-TRACKING.md](docs/UPSTREAM-TRACKING.md) describes how compatibility with the
reference stays in sync.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Issues and PRs welcome.

## Security

See [SECURITY.md](SECURITY.md) for the threat model and how to report a vulnerability privately.

## License

[Apache License 2.0](LICENSE) · © 2026 Schuby. See [NOTICE](NOTICE) for the independence and
trademark statement.
