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
  <a href="https://go.dev/"><img alt="Go" src="https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white"></a>
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
documented, deliberate divergences. Those divergences are listed in
[docs/DIVERGENCES.md](docs/DIVERGENCES.md). You can also use claustrum on its own. For
example, it can be a building block for self-hosted tooling such as
[clauster](https://github.com/schubydoo/clauster).

> Status: stable (v1.0+). The JSON-RPC, process, file, and git surface is complete and
> checked. The CLI-version installer is implemented, and its behavior is checked. No
> telemetry, ever.

## Install / build

Claustrum requires Go 1.25 or later. The toolchain is held below 1.27, because the default
`jsonv2` of Go 1.27 moves inherited wire bytes. See
[docs/UPSTREAM-TRACKING.md](docs/UPSTREAM-TRACKING.md). Build with the `go.mod` toolchain.

One dependency is cross-platform: `github.com/klauspost/compress`, for zstd. Two more modules
are compiled into Windows builds only. `golang.org/x/sys` does the Job Object teardown.
`github.com/Microsoft/go-winio` provides the opt-in `-listen-pipe` named-pipe transport
(CT-5).

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
the SHA and the time from the embedded VCS build info. A released binary carries its tag.
`go install …@vX.Y.Z` reports the resolved module version and the tagged release timestamp.
The code that stamps the version lives in `buildstamp.go`. A pseudo-version such as `@main` prints
`built unknown`.

A `go install` binary is not flag-for-flag identical to a release artifact. It takes the host
cgo defaults, it has no `-trimpath`, and it is not stripped. Pass the release flags for an
equivalent build:

```sh
CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" github.com/schubydoo/claustrum@latest
```

## Usage

There is one binary. A flag selects the mode:

```text
claustrum -serve   -socket <path> -token-file <path>   # self-daemonize, run the RPC server
claustrum -bridge  -socket <path>                       # dumb stdio<->socket relay (what SSH attaches)
claustrum -stop    -socket <path>                       # ask a running daemon to shut down
claustrum -install -cli-dir <dir> -cli-version <v> [-cli-url <url> -cli-checksum <sha256>] [-cli-zst <file>] [-cli-keep <n>]
claustrum -version
claustrum -probe-cli <cli>                              # probe <cli> --version (30s): empty=runs / __CLI_HUNG__ / __CLI_BAD__; exit 0
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

[docs/PROTOCOL.md](docs/PROTOCOL.md) and [docs/EXAMPLES.md](docs/EXAMPLES.md) hold more worked
examples. They spawn a process and read its base64 output stream. They reattach and catch up
through the replay buffer. They extract a plugin tarball.

## How it works

- Transport: NDJSON over `AF_UNIX` `SOCK_STREAM`, at mode `0600`. There is one persistent
  connection. Requests dispatch concurrently.
- Auth: every request carries an in-band `"auth":"<token>"`. The daemon's token comes from
  `-token-file` or from `-token-fd`. With `-token-file` the daemon reads the file once and then
  unlinks it. With `-token-fd` the daemon reads the token from an open descriptor, so the
  handoff never touches disk. Claustrum reads `CLAUDE_RPC_TOKEN` nowhere, and strips it from
  spawned children. One method is the exception to auth itself: `server.shutdown` is not
  authenticated, which matches the reference. Therefore `-stop` sends no token at all.
- The daemon has 19 methods, across `server.*`, `files.*`, `git.*`, `process.*`, and
  `plugins.*`. `server.capabilities` self-describes them.
- `process.*` is the core. A client supplies its own `id` on `spawn`. The daemon streams id-less
  `{"type":"stream",…}` notifications, which carry base64 stdout and stderr and an `exit`. The
  daemon buffers those notifications and replays them on `reattach{fromSeq}`. This is how the
  daemon hosts both the agent and the MCP servers. `process.spawn` and `process.reattach` also
  accept `"wantPid":true` (CT-1). That member adds `pid` and `startTime` to the result, for the
  detection of PID reuse and orphans. A client that does not opt in sees byte-identical frames.

### Operational knobs

These knobs belong to claustrum only, and they stay off the wire:

- `CLAUSTRUM_LOG_LEVEL` raises the threshold of the leveled stderr log. Logging is always on.
- `-metrics-addr` opts into a local Prometheus `/metrics` endpoint. Without the flag, no
  listener exists.
- `-keep-children` (CT-2, POSIX only) leaves spawned children running across a graceful
  shutdown.
- `-listen-pipe` (CT-5, Windows only) also serves the same JSON-RPC over a named pipe.
- `-wire-log` (CT-3) appends every JSON-RPC frame to a file for diagnostics. It redacts
  credentials by key only.

All of them are off by default.

Seven flags opt into a deliberate divergence from the reference. Each flag is off by default,
and each flag has a matching `claustrum.conf` key. Claude Desktop owns the argv, so that key is
the reachable knob. That is a driver claim. See
[docs/ARCHITECTURE.md → Driver claims and their provenance](docs/ARCHITECTURE.md#driver-claims-and-their-provenance).
See [docs/DIVERGENCES.md](docs/DIVERGENCES.md) for the catalog, the rules, and the
measurements.

| Flag | Default | Opts into | Scope |
|------|---------|-----------|-------|
| `-max-extract-bytes` (D3) | off (0) | a size cap for `files.extract_tar`, with an error frame over the cap | `-serve` |
| `-files-read-regular-only` (D4) | off | refusing a non-regular `files.read` (`-32602`) | `-serve` |
| `-git-timeout` (D5) | off (0) | a deadline on every git call (`-32603` `signal: killed`) | `-serve` |
| `-max-cli-bytes` (D10) | off (0) | a size cap on the decompressed CLI + download body | `-install` |
| `-cli-probe-timeout` (D11) | off (0) | a deadline on the `<cli> --version` runnability probe | `-install` |
| `-cli-download-timeout` (D12) | off (0) | a deadline on the CLI download | `-install` |
| `-libc-probe-timeout` (D14) | off (0) | a deadline on the `ldd --version` libc probe | `-install`, linux only |

For the full details, see [docs/PROTOCOL.md](docs/PROTOCOL.md) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Platforms

Claustrum cross-compiles to linux, macOS (darwin), and windows, on amd64 and arm64. That is
six targets. It is a static `CGO_ENABLED=0` Go binary. OS-specific behavior is isolated in the
`*_unix.go` and `*_windows.go` files. That behavior covers four areas:

- The daemonize step.
- Process groups on Unix, and Job Objects on Windows, for a whole-tree kill.
- Login-shell PATH extraction.
- The Windows-only `-listen-pipe` transport.

The JSON-RPC surface is identical everywhere.

## Validation

`claustrum` is checked against a reference daemon with a request battery. The battery
exercises every method, every error path, and the full process lifecycle. It then diffs
normalized frames. The current status is byte-identical on every method the battery exercises,
apart from the documented, deliberate divergences. Those divergences are catalogued in
[docs/DIVERGENCES.md](docs/DIVERGENCES.md). Most of them are opt-in and off by default. A few
are always-on or conditional. The code for the battery lives in `scratch/`, which is local and
not published.

An in-repo test suite locks the same contract without the reference binary. CI runs that suite
on every PR, on linux, macOS, and Windows. A socket-integration battery boots the daemon and
asserts every method's frames against committed golden fixtures. Unit tests cover the install
pipeline and the bridge and stop clients. Statement coverage is about 99%. See
[docs/UPSTREAM-TRACKING.md](docs/UPSTREAM-TRACKING.md) for how compatibility is kept in sync over
time.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Issues and PRs welcome.

## Security

See [SECURITY.md](SECURITY.md) for the threat model and how to report a vulnerability privately.

## License

[Apache License 2.0](LICENSE) · © 2026 Schuby. See [NOTICE](NOTICE) for the independence and
trademark statement.
