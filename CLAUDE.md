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
| Lint | `golangci-lint run ./...` (config in `.golangci.yml`; v2) |
| Tests | `go test -race ./...` — unit + socket-integration suites |
| Validation battery | `scratch/probe/validate.sh` — diffs frames vs the reference (gitignored) |

- Go 1.25+. Dependencies: `github.com/klauspost/compress` (zstd) and
  `golang.org/x/sys` (Windows Job Object teardown — only compiled into Windows
  builds); `CGO_ENABLED=0`.
- In-repo tests (`*_test.go`) cover the wire surface two ways: fast unit tests
  (frame encoding, dispatch / auth / error routing, replay buffer, env merging,
  the `-install` pipeline) **and** a socket-integration suite (`harness_test.go`
  + `integration*_test.go`) that boots the daemon on a temp `AF_UNIX` socket and
  asserts every method's frames against committed golden fixtures
  (`testdata/socket_*.golden.json`) — so CI gates compatibility without the
  reference binary. ~83% statement coverage. The cross-binary battery that diffs
  against the reference daemon lives in `scratch/`.

## Architecture

One binary, mode-switched by flag (`main.go`): `-serve`, `-bridge`, `-stop`,
`-version`, `-install`.

- **`rpc.go`** — request/response types, error codes, `dispatch` (parse →
  auth → version → route by namespace; auth is checked *before* the jsonrpc
  version — probe-verified). Mistyped params → `-32602 Invalid params`.
- **`server.go`** — the `-serve` daemon: AF_UNIX listener (mode `0600`),
  per-conn read loop, **concurrent** dispatch, self-daemonize, graceful shutdown.
- **`methods_server.go` · `methods_files.go` · `methods_git.go` ·
  `methods_process.go`** — the 18 methods across `server.*` / `files.*` /
  `git.*` / `process.*`.
- **`results.go`** — result structs whose fields are declared in the exact order
  the reference emits. **Never a map** (maps sort keys and diverge from the wire
  contract).
- **`process.go`** — `procManager` / `managedProc`: spawn in own process group,
  stream base64 stdout/stderr frames, async stdin writer (bounded queue +
  backpressure), per-process replay buffer, `reattach`.
- **`bridge.go`** — `-bridge`: a dumb stdio↔socket relay (what SSH attaches to).
- **`logging.go`** — tiny leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`, defaults
  to emit-everything). The level tag goes *before* the `[Component]` prefixes so
  existing greps keep matching.
- **`metrics.go`** — opt-in Prometheus counters at `/metrics`, served by a stdlib
  `net/http` listener **only** when `-metrics-addr` is set (off by default; not
  part of the JSON-RPC wire). Counting is always-on atomics; the endpoint is the
  opt-in part.
- **`install.go`** — `-install`: CLI download / verify (SHA-256 — **`-cli-url`
  downloads unconditionally; the local `-cli-zst` SFTP blob is verified only when a
  `-cli-checksum` is supplied**, an opt-in divergence from the reference, see D1) /
  extract (zstd) / prune.
- OS-specific behavior is isolated in `*_unix.go` / `*_windows.go` (daemonize,
  process groups / Windows Job Objects for whole-tree kill, login-shell PATH
  extraction). The JSON-RPC surface is identical everywhere.

## Conventions

- **Byte-identical wire frames are the contract.** Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the validation battery green. An *intentional*
  divergence must be documented in [`docs/PROTOCOL.md`](docs/PROTOCOL.md) and the PR.
- **No new dependencies** without discussion — stdlib + zstd (`klauspost/compress`)
  + `golang.org/x/sys` (Windows-only), `CGO_ENABLED=0`.
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
- Before a PR: `gofmt -l .` empty, `go vet ./...` clean, `golangci-lint run`
  clean, `go test -race ./...` green. For a **wire-surface** change, also re-run
  the `scratch/` validation battery to confirm frames stay byte-identical.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas

- **`process.spawn` runs arbitrary commands as the daemon's user — by design.**
  Treat socket + token as equivalent to shell access (threat model in
  [`SECURITY.md`](SECURITY.md)).
- **Auth is in-band per request** (`"auth":"<token>"`); the daemon's token comes
  from `-token-file` (read once, then unlinked so it never lands in
  `/proc/<pid>/environ`) or `-token-fd` (read from an open descriptor, forwarded
  to the daemonized child over a pipe — never touches disk); `CLAUDE_RPC_TOKEN`
  is only for the `-bridge`/`-stop` clients.
- **A connection's requests dispatch concurrently** — replies can return out of
  order, matching the reference. Don't serialize them.
- **`-install` reaches the network only with `-cli-url`** and verifies the
  SHA-256 before extracting on that download path unconditionally. The local
  `-cli-zst` (SFTP) blob is checksum-verified **only when a `-cli-checksum` is
  supplied** — an *intentional* opt-in divergence from the reference (which never
  verifies it), documented in [`docs/PROTOCOL.md`](docs/PROTOCOL.md) +
  [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md) D1; an absent checksum stays
  trusting, so honest callers are byte-identical. `-serve` makes no outbound connections.

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- Internals → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- Ideas / deferred → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local agent guardrails + context-mode routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks); cut a release by pushing
  a `v*` tag — signed + SBOM'd + SLSA provenance via [`.goreleaser.yaml`](.goreleaser.yaml)

## Documentation references

For Claude Code or Anthropic API specifics, prefer current docs (context7 /
find-docs) over memory — <https://docs.anthropic.com/en/docs/claude-code/overview>.
