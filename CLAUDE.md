# Claustrum

A tiny, dependency-light **Go** daemon — a clean-room reimplementation of the
small daemon that hosts a remote Claude Code session over SSH: a local
CLI-version manager + process supervisor + JSON-RPC multiplexer (with a replay
buffer) over an `AF_UNIX` socket. Built to a behavioral contract captured by
black-box probing the reference binary; no code was copied or decompiled (see
[`NOTICE`](NOTICE)).

**The one hard rule: stay byte-identical to the reference daemon's JSON-RPC
frames.** The wire surface *is* the product.

## Build · run · test

| Task | Command |
|------|---------|
| Build the binary | `make build` — CGO off, `-trimpath`, stripped → `./claustrum` |
| Cross-build all 6 targets | `make all` — linux/darwin/windows × amd64/arm64 → `dist/` |
| Format | `make fmt` (`gofmt -w`); `gofmt -l .` must be empty |
| Vet | `make vet` (`go vet ./...`) |
| Unit tests | `go test -race ./...` |
| Validation battery | `scratch/probe/validate.sh` — diffs frames vs the reference (gitignored) |

- Go 1.23+. Only dependency is `github.com/klauspost/compress` (zstd); `CGO_ENABLED=0`.
- Unit tests in `*_test.go` cover the wire surface (frame encoding, dispatch /
  auth / error routing, the replay buffer, env merging). The full cross-binary
  battery that diffs against the reference daemon lives in `scratch/`.

## Architecture

One binary, mode-switched by flag (`main.go`): `-serve`, `-bridge`, `-stop`,
`-version`, `-install`.

- **`rpc.go`** — request/response types, error codes, `dispatch` (parse →
  version → auth → route by namespace).
- **`server.go`** — the `-serve` daemon: AF_UNIX listener (mode `0600`),
  per-conn read loop, **concurrent** dispatch, self-daemonize, graceful shutdown.
- **`methods_server.go` · `methods_files.go` · `methods_git.go` ·
  `methods_process.go`** — the 18 methods across `server.*` / `files.*` /
  `git.*` / `process.*`.
- **`results.go`** — result structs whose fields are declared in the exact order
  the reference emits. **Never a map** (maps sort keys and diverge from the wire
  contract).
- **`process.go`** — `procManager` / `managedProc`: spawn in own process group,
  stream base64 stdout/stderr frames, per-process replay buffer, `reattach`.
- **`bridge.go`** — `-bridge`: a dumb stdio↔socket relay (what SSH attaches to).
- **`install.go`** — `-install`: CLI download / verify (SHA-256) / extract
  (zstd) / prune.
- OS-specific behavior is isolated in `*_unix.go` / `*_windows.go` (daemonize,
  process groups, login-shell PATH extraction). The JSON-RPC surface is
  identical everywhere.

## Conventions

- **Byte-identical wire frames are the contract.** Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the validation battery green. An *intentional*
  divergence must be documented in [`docs/PROTOCOL.md`](docs/PROTOCOL.md) and the PR.
- **No new dependencies** without discussion — stdlib + one zstd lib, `CGO_ENABLED=0`.
- **No telemetry, ever.**
- **Cross-platform parity** — keep OS specifics in `*_unix.go` / `*_windows.go`;
  `make all` must cross-compile cleanly for all six targets.
- Keep host-specific / reverse-engineering working notes out of the repo —
  `scratch/` is gitignored on purpose.

## Always do

- **Every change to `main` goes through a branch + PR — never commit or push to
  `main` directly.**
- **Conventional Commits** for PR titles (`feat:` / `fix:` / `docs:` / `chore:`
  / `ci:` …); PRs squash-merge, so the title becomes the commit subject.
- Before a wire-surface PR: `gofmt -l .` empty, `go vet ./...` clean,
  `go test -race ./...` green, **and** re-run the `scratch/` validation battery
  to confirm frames stay byte-identical.

## Gotchas

- **`process.spawn` runs arbitrary commands as the daemon's user — by design.**
  Treat socket + token as equivalent to shell access (threat model in
  [`SECURITY.md`](SECURITY.md)).
- **Auth is in-band per request** (`"auth":"<token>"`); the token comes from
  `-token-file` (read once, then unlinked so it never lands in
  `/proc/<pid>/environ`) or `CLAUDE_RPC_TOKEN`.
- **A connection's requests dispatch concurrently** — replies can return out of
  order, matching the reference. Don't serialize them.
- **`-install` reaches the network only with `-cli-url`** and verifies the
  SHA-256 before extracting; `-serve` makes no outbound connections.

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- Internals → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- Ideas / deferred → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local agent guardrails + context-mode routing → `CLAUDE.local.md` (gitignored)

## Documentation references

For Claude Code or Anthropic API specifics, prefer current docs (context7 /
find-docs) over memory — <https://docs.anthropic.com/en/docs/claude-code/overview>.
