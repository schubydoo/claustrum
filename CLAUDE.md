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

- Go 1.25+. Dependencies: `github.com/klauspost/compress` (zstd) cross-platform,
  plus two Windows-only (compiled into Windows builds only): `golang.org/x/sys`
  (Job Object teardown) and `github.com/Microsoft/go-winio` (the opt-in
  `-listen-pipe` named-pipe transport); `CGO_ENABLED=0`.
- In-repo tests (`*_test.go`) cover the wire surface two ways: fast unit tests
  (frame encoding, dispatch / auth / error routing, replay buffer, env merging,
  the `-install` pipeline) **and** a socket-integration suite (`harness_test.go`
  + `integration*_test.go`) that boots the daemon on a temp `AF_UNIX` socket and
  asserts every method's frames against committed golden fixtures
  (`testdata/socket_*.golden.json`) — so CI gates compatibility without the
  reference binary. The suite runs on linux, macOS, **and Windows** in CI:
  process fixtures come from the test binary itself (`helperproc_test.go`,
  `CLAUSTRUM_TEST_HELPER`), never `/bin/*`, so stream bytes match the goldens
  on every OS. ~98% statement coverage. The cross-binary battery that diffs
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
  `methods_process.go`** — the 19 methods across `server.*` / `files.*` /
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
  extraction, the `-keep-children` POSIX-only policy via `honorKeepChildren`, the
  Windows-only `-listen-pipe` named-pipe transport via `startPipeTransport` /
  `honorListenPipe` in `pipetransport_windows.go`). The JSON-RPC surface is
  identical everywhere.
- **`pipetransport.go`** — the opt-in, default-off, Windows-only `-listen-pipe`
  transport (CT-5): when set, `-serve` *additionally* serves the identical
  JSON-RPC dispatch over a Windows named pipe (a second `acceptLoop` over the same
  `serveConn` — no wire change), publishing the chosen pipe name to `rpc.pipe`
  beside the socket. Owner-only DACL, same `daemon.token` auth. Off ⇒ byte-for-byte
  identical to the reference. Platform-neutral helpers here; go-winio wiring in
  `pipetransport_windows.go`, the non-Windows no-op stub in `pipetransport_other.go`.

## Conventions

- **Byte-identical wire frames are the contract.** Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the validation battery green. An *intentional*
  divergence must be documented in [`docs/PROTOCOL.md`](docs/PROTOCOL.md) and the PR.
- **No new dependencies** without discussion — stdlib + zstd (`klauspost/compress`)
  + `golang.org/x/sys` and `github.com/Microsoft/go-winio` (both Windows-only),
  `CGO_ENABLED=0`.
- **No telemetry, ever.**
- **Cross-platform parity** — keep OS specifics in `*_unix.go` / `*_windows.go`;
  `make all` must cross-compile cleanly for all six targets.
- Keep host-specific / reverse-engineering working notes out of the repo —
  `scratch/` is gitignored on purpose.

## Always do

- **Every change to `main` goes through a branch + PR — never commit or push to
  `main` directly.**
- **Conventional Commits** for PR titles (`feat:` / `fix:` / `docs:` / `chore:`
  / `ci:` …); PRs squash-merge, so the title becomes the commit subject. Titles
  are hygiene only — they do **not** drive releases.
- **Releases are changesets-only** (knope, not release-please). A user-facing
  change adds a `.changeset/*.md` fragment (`knope document-change`); the fragment
  drives the version bump + changelog. No fragment ⇒ no release. Internal PRs
  (CI/tooling/refactor/tests) need none — apply the `no-changelog` label. See
  [`.changeset/`](.changeset/) and CONTRIBUTING.md → Changesets.
- **A changeset body is ONE line.** knope renders any multi-line body as a `####`
  heading block instead of a bullet, which breaks the changelog (it already did,
  in 1.7.2 and 1.7.3). Fold every detail into the single sentence — never a second
  line, never a second paragraph. `scripts/lint_changesets.py` gates this in CI
  and pre-commit.
- Before a PR: `gofmt -l .` empty, `go vet ./...` clean, `golangci-lint run`
  clean, `go test -race ./...` green. For a **wire-surface** change, also re-run
  the `scratch/` validation battery to confirm frames stay byte-identical.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas

- **`process.spawn` runs arbitrary commands as the daemon's user — by design.**
  Treat socket + token as equivalent to shell access (threat model in
  [`SECURITY.md`](SECURITY.md)).
- **Two methods `os.RemoveAll` a caller-supplied path, and both are `~`-expanded
  first**: `files.extract_tar` wipes `destDir`, `git.worktree_remove` deletes
  `worktreePath` when git fails. `"~"` therefore meant `os.RemoveAll($HOME)` —
  which destroyed the maintainer's home directory on 2026-08-02. `wipesHomeDir`
  (`homeguard.go`) refuses a target that **is or contains** home; paths **under**
  home stay allowed, because `~/.claude/…` is the daemon's own install path.
  Always-on, not opt-in — see [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md) D2.
  **Any RPC path param that reaches a recursive delete owes this guard**, and
  `IsAbs && !isFilesystemRoot` is not it — a home directory passes both, and
  neither one resolves a relative path (`worktreePath:".."` from a daemon sitting
  in home deletes it; measured). The install path has a **third** `os.RemoveAll`
  on operator-supplied input (`filepath.Join(cliDir, cliVersion)`); it is guarded
  by D6's single-path-component rule instead, not by `wipesHomeDir`.
- **Auth is in-band per request** (`"auth":"<token>"`); the daemon's token comes
  from `-token-file` (read once, then unlinked so it never lands in
  `/proc/<pid>/environ`) or `-token-fd` (read from an open descriptor, forwarded
  to the daemonized child over a pipe — never touches disk). **No mode reads
  `CLAUDE_RPC_TOKEN`** — `-bridge` relays a client that supplies its own `auth`,
  and the daemon strips the variable from spawned children. **`server.shutdown`
  is the one method that is not authenticated** (parity with the reference —
  Desktop stops the daemon with no token in its environment), so `-stop` sends no
  `auth` member; every other method still rejects an unauthenticated request
  `-32001`.
- **The daemon persists its token to `daemon.token` (mode `0600`) beside the
  socket** — written atomically at startup, unlinked on graceful shutdown — so a
  client can reconnect to a running daemon after the `-token-file` was unlinked /
  the `-token-fd` pipe closed (`tokenpersist.go`). This is **behavioral parity
  with the reference** (added upstream `5db5e4a`); the fixed name + socket-dir
  location *are* the reconnect contract, so they're deliberately not
  configurable. Known parity caveats (same as the reference, **do not "fix"**
  without making them an opt-in divergence): two daemons sharing one directory
  collide on the file, and on Windows `0600` is not an owner-only DACL (a Go
  `os.CreateTemp` limitation — the per-user session dir is the confinement). See
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token persistence.
- **A connection's requests dispatch concurrently** — replies can return out of
  order, matching the reference. Don't serialize them. The per-request goroutine
  **recovers from panics**, replying `-32603 "recovered panic: <v>"`. That frame
  is **claustrum's own and is NOT a parity claim** — the path is unreachable, so
  no client can observe it and it cannot diverge from anything. Don't add a golden
  for it (the battery never exercises it) and don't treat it as a wire contract.
  It is provoked in tests through the `dispatchRequest` seam.
- **The `files.extract_tar` size cap is OFF by default, and that is the parity
  position.** The reference applies no cap at any size the probe could reach
  (measured: a 629 MB payload extracts fully and answers
  `{"success":true,"fileCount":1}`), so a non-zero default fails
  an extraction the reference completes — with no way through, since Claude
  Desktop owns the argv. `maxExtractBytes` therefore defaults to `0` = unlimited,
  and the cap is opt-in via `-max-extract-bytes` **or** the `max-extract-bytes`
  key in `claustrum.conf` (the config key is the reachable one). Disabled bypasses
  `io.LimitReader` entirely — do not "simplify" it into a huge limit, because the
  `max-total+1` arithmetic is what defines the boundary. Divergence D3; see
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md) + [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md).
- **The `-install` CLI size cap is OFF by default (D10).** `maxCLIBytes` governs
  both the decompressed CLI and the download body; measured on both paths, the
  reference took a 600 MiB payload all the way to the runnability check, so a
  non-zero default fails an install the reference completes — and Claude Desktop
  owns the argv on `-install`, so there is no way through. Opt in with
  `-max-cli-bytes` or the `max-cli-bytes` key in `claustrum.conf`. Disabled
  bypasses **both** `io.LimitReader`s entirely — do not "simplify" either into a
  huge limit, because the `cap+1` arithmetic is what defines the boundary. The
  blob is **streamed, never buffered** (a path, not a `[]byte`, so the staging
  retry can re-read it); that is what keeps "cap off" from meaning "unbounded
  memory".
- **`-install` applies wall-clock bounds the reference does not appear to, plus one
  ordering difference.** Three exist — the `<cli> --version` runnability probe
  (D11), the download (D12), and, on linux only, the `ldd --version` libc probe at
  5 s (tier item 5; `libc_other.go` skips the probe entirely off linux). **D11 and
  D12 are BOTH off by default now**, opt-in via `-cli-probe-timeout` /
  `-cli-download-timeout` or the matching `claustrum.conf` keys — the config keys
  are the reachable ones, since Desktop owns the argv. Each disables differently
  and neither may be "simplified" into a huge duration: D11 bypasses
  `context.WithTimeout` entirely, and D12 relies on `http.Client{Timeout: 0}` being
  the stdlib's own "no timeout". ⚠️ D12's zero frees the **body read only** —
  `http.DefaultTransport` still applies `net.Dialer{Timeout: 30s}` and
  `TLSHandshakeTimeout: 10s`, both always-on, unnumbered and unprobed on the
  reference. So at the shipped defaults the only `-install` wall-clock bound
  claustrum applies is the linux `ldd` one, and even that cannot fire **where the
  musl loader glob matches**, because `detectLibcWith` returns before spawning
  `ldd` — the predicate is the glob, not the host, so a musl box whose loader the
  glob misses still reaches it. Everything below about D11 and D12 therefore
  describes what an operator opts INTO, measured against the old hardcoded 15 s and
  5 minutes, except where a sentence says otherwise.
  D13 is **not** a bound — it is verify-before-decompress
  ordering, which no slow input trips. **No bound here is a hang detector**: each is
  a threshold, so an honest-but-slow input trips it too. For D11 and D12 the
  reference showed no deadline at or below 45 s (400 s for D12) on an input that
  never answers, and **(D11 only)** it INSTALLED a 90 s CLI, waiting
  91 s — which bounds D11's above 90 s, not at absent; the ldd bound has never been probed
  on either binary.
  **Measured (D11, claustrum side):** a CLI answering honestly in 20 s diverges,
  and *how* depends on the shape — `installed cli at <path> is not runnable` with
  the staged binary deleted; or `cli <v> missing and no --cli-url or --cli-zst
  provided`; or **no error at all**, silently reinstalled. On `-cli-zst` the blob is
  consumed whenever decompression succeeded, which includes the silent shape. The
  cached binary survives **every** failure before the rename — both probes timing
  out is one of them, and so are a 404, a checksum mismatch, a `mkdir cli dir:`
  error and a bad-zstd blob, none of which reach the second probe at all. Only a
  replacement that gets as far as the rename replaces it. With no source flag
  `ensureCLI` still runs but returns before any install step (the orphan sweep
  does still run).
  **Measured (reference):** it installs the 20 s CLI outright on the no-cache
  shape. **Derived:** on the cached shapes it should cache-hit and report
  `cliWasPresent:true` having installed nothing, since its guard has no cut-off at
  or below 90 s — not separately measured, and what it does above 90 s is
  unmeasured on both binaries. **Measured on the D11 flip branch:** with the
  deadline off, claustrum is likewise still running when cut at 45 s against a
  planted `sleep 120` CLI, with an opted-in 15 s run and an instant CLI as the two
  controls. **D12's honest-but-slow half is measured too**: a valid zstd blob
  dribbled over ~14 s installs on the reference and on claustrum at the new default
  alike, while a `-cli-download-timeout 2s` control fails at 2 s. ⚠️ That fixture
  sits far under the retracted 5-minute bound, so it measures the shape rather than
  the shipped value; the fixture must also carry a VALID zstd body, because with an
  invalid one the reference answers `decompressing: invalid input: magic number
  mismatch` at 0 s, which is D13, not a download bound.
  The ldd bound's *value* delta is narrow —
  fallback and true value coincide except where `ldd` reports musl and the loader
  glob misses. But a **stalled** `ldd` makes it total on **any host without a musl
  loader** — i.e. every ordinary glibc host, since that is exactly when `ldd` is
  spawned at all: claustrum falls back at 5 s and emits a full
  `__INSTALL_RESULT__` where the reference is assumed to block (not probed). D13 differs on the one combined-failure input of the
  three measured — corrupt *and* wrong-checksummed — not on every possible input.
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
- Host-local agent guardrails + agent-tool routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks). Releases are automated by
  **knope** (`knope.toml`): pending `.changeset/` fragments → `knope-prepare.yml`
  opens a `chore: prepare release X.Y.Z` PR (bumps `VERSION` + `CHANGELOG.md`);
  merging it → `knope-release.yml` tags `v*` + creates the GitHub Release, and that
  tag fires `release.yml` — signed + SBOM'd + SLSA provenance via
  [`.goreleaser.yaml`](.goreleaser.yaml). Gated on the `KNOPE_ENABLED` repo var.

## Documentation references

For Claude Code or Anthropic API specifics, prefer current docs (context7 /
find-docs) over memory — <https://docs.anthropic.com/en/docs/claude-code/overview>.
