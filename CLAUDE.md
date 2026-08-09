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
  `-cli-checksum` is supplied**, a conditional divergence from the reference —
  activated by the caller, not by an operator; see D1) /
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
  divergence must be catalogued in [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)
  (entry + decision rules), carry its wire frames in
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md), and be called out in the PR.
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

## Gotchas — Part A: always-on safety (hold these before touching code)

- **`process.spawn` runs arbitrary commands as the daemon's user — by design.**
  Treat socket + token as equivalent to shell access (threat model in
  [`SECURITY.md`](SECURITY.md)).
- **Three code paths `os.RemoveAll` a caller- or operator-supplied path.** Two are
  RPC paths, both `~`-expanded first — so `"~"` once meant `os.RemoveAll($HOME)`,
  which destroyed the maintainer's home directory on 2026-08-02:
    - `files.extract_tar` wipes `destDir` — guarded by `wipesHomeDir` (`homeguard.go`).
    - `git.worktree_remove` deletes `worktreePath` when git fails — guarded by `wipesHomeDir`.
    - `-install` deletes `filepath.Join(cliDir, cliVersion)` (operator input) —
      guarded by **D6's single-path-component rule instead**, not `wipesHomeDir`.

  `wipesHomeDir` refuses a target that **is or contains** home (resolving relative
  paths with `filepath.Abs` first); paths **under** home stay allowed, because
  `~/.claude/…` is the daemon's own install path. Always-on, not opt-in.
  **Any RPC path param that reaches a recursive delete owes this guard** —
  `IsAbs && !isFilesystemRoot` is *not* a substitute (home passes both, and it
  resolves no relative path, so `worktreePath:".."` from a daemon in home still
  deletes it; measured). See [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md) D2.
- **Auth is in-band per request** (`"auth":"<token>"`). The daemon's token comes
  from `-token-file` (read once, then unlinked so it never lands in
  `/proc/<pid>/environ`) or `-token-fd` (read from an open descriptor, forwarded
  to the daemonized child over a pipe — never touches disk). **No mode reads
  `CLAUDE_RPC_TOKEN`** — `-bridge` relays a client that supplies its own `auth`,
  and the daemon strips the variable from spawned children. **`server.shutdown`
  is the one method that is not authenticated** (parity with the reference —
  Desktop stops the daemon with no token in its environment), so `-stop` sends no
  `auth` member; every other method rejects an unauthenticated request `-32001`.
- **The daemon persists its token to `daemon.token` (mode `0600`) beside the
  socket** — written atomically at startup, unlinked on graceful shutdown — so a
  client can reconnect after the `-token-file` was unlinked / the `-token-fd` pipe
  closed (`tokenpersist.go`). The fixed name + socket-dir location *are* the
  reconnect contract, so they are deliberately not configurable. **Do not "fix"**
  the known parity caveats (two daemons in one dir collide on the file; on Windows
  `0600` is not an owner-only DACL) without making the change an opt-in divergence.
  See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token persistence.
- **A connection's requests dispatch concurrently** — replies can return out of
  order, matching the reference. Don't serialize them. The per-request goroutine
  **recovers from panics**, replying `-32603 "recovered panic: <v>"`. That frame
  is **claustrum's own and is NOT a parity claim** — the path is unreachable, so no
  client can observe it and it cannot diverge from anything. Don't add a golden for
  it (the battery never exercises it), don't treat it as a wire contract. It is
  provoked in tests through the `dispatchRequest` seam.
- **`-serve` makes no outbound connections; `-install` reaches the network only
  with `-cli-url`.** That download path verifies its SHA-256 before extracting,
  unconditionally.
- **`-cli-probe-timeout` and `-libc-probe-timeout` are a swap footgun** — one
  letter apart, the same type, resolved two lines apart in main's `-install` arm; a
  swap compiles and passes every isolated test. `TestInstallArmWiresEachFlagToItsOwnGlobal`
  is the guard against it.
- **A disabled limiter bypasses its `io.LimitReader` / `context.WithTimeout`
  entirely — never "simplify" it into a huge value.** For the caps, the `cap+1`
  (or `max-total+1`) arithmetic is what defines the boundary; for the deadlines, an
  unarmed cancel path is what makes the timeout `false` by construction. A huge
  constant is a different, observable behavior. This applies to every flag in
  Part B.

## Gotchas — Part B: the opt-in wire divergences

Seven divergences are opt-in flags, and **all seven default OFF — that is the
parity position.** The reference applies no such cap or deadline at any size or
duration the probe could reach, so a non-zero default would fail an operation the
reference completes. Claude Desktop owns the `-serve` / `-install` argv, so the
**`claustrum.conf` key is the reachable knob**, not the flag. Each disabled state
bypasses its limiter entirely (see Part A's "never simplify" rule).

**D5's deadline gates a destructive path:** `git.worktree_remove` treats a failed
git as permission to delete `worktreePath`, so a `git-timeout` firing must never be
read as "git refused". Opting it in is wire-visible.

| ID | Flag | `claustrum.conf` key | Scope | Default | What it bounds / caps |
|----|------|----------------------|-------|---------|-----------------------|
| D3 | `-max-extract-bytes` | `max-extract-bytes` | `-serve` | off (0) | total uncompressed bytes `files.extract_tar` writes |
| D4 | `-files-read-regular-only` | `files-read-regular-only` | `-serve` | off (false) | makes `files.read` refuse non-regular paths (FIFO / socket / char / block device) with `-32602 files.read: not a regular file` |
| D5 | `-git-timeout` | `git-timeout` | `-serve` | off (0) | wall-clock deadline on every git invocation (gates a destructive fallback — see above) |
| D10 | `-max-cli-bytes` | `max-cli-bytes` | `-install` | off (0) | the decompressed CLI **and** the download response body |
| D11 | `-cli-probe-timeout` | `cli-probe-timeout` | `-install` | off (0) | wall-clock bound on the `<cli> --version` runnability probe |
| D12 | `-cli-download-timeout` | `cli-download-timeout` | `-install` | off (0) | wall-clock bound on the whole `-cli-url` download (body read only — stdlib dial 30 s / TLS 10 s still always apply) |
| D14 | `-libc-probe-timeout` | `libc-probe-timeout` | `-install` (linux only) | off (0) | wall-clock bound on the `ldd --version` libc probe |

**The two non-flag divergences:**

- **D1** — the `-cli-zst` SFTP blob is checksum-verified **only when a
  `-cli-checksum` is supplied** (a *conditional*, caller-activated divergence, not
  an off-by-default flag). An absent checksum stays trusting, so honest callers are
  byte-identical.
- **D13** — verify-before-decompress *ordering* (no wall-clock threshold); its
  trigger is reachable, and it stays **always-on but unresolved**, not justified.

For the full catalog and the governing rules (rule 1–4 + clauses (a)/(b)/(c)),
each divergence's default / activation / cost / reopen trigger, and the measured
forensics behind them → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Per-method
wire facts (params, result field order, error strings, the D5 `signal: killed`
frames) → [`docs/PROTOCOL.md`](docs/PROTOCOL.md). The driver-claim provenance that
some of these rest on — "Desktop owns the argv", `cliError` classification, libc
selection → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) → Driver claims and
their provenance.

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- Divergence catalog + rules → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)
- Internals + driver-claim provenance → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- Shipped ledger (completed work) → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local agent guardrails + agent-tool routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks). Releases are automated by
  **knope** (`knope.toml`): pending `.changeset/` fragments → `knope-prepare.yml`
  opens a `chore: prepare release X.Y.Z` PR (bumps `VERSION` + `CHANGELOG.md`);
  merging it → `knope-release.yml` tags `v*` + creates the GitHub Release, and that
  tag fires `release.yml` — signed + SBOM'd + SLSA provenance via
  [`.goreleaser.yaml`](.goreleaser.yaml). Gated on the `KNOPE_ENABLED` repo var.
- Claude Code / Anthropic API specifics → prefer current docs (context7 /
  find-docs) over memory — <https://docs.anthropic.com/en/docs/claude-code/overview>.
