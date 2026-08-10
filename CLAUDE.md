# Claustrum

Claustrum is a tiny, dependency-light **Go** daemon. It is a clean-room
reimplementation of the small daemon that hosts a remote Claude Code session
over SSH. It combines three parts: a local CLI-version manager, a process
supervisor, and a JSON-RPC multiplexer (with a replay buffer) over an
`AF_UNIX` socket. Black-box probes of the reference binary captured a
behavioral contract, and claustrum is built to that contract. No code was
copied and no code was decompiled (see [`NOTICE`](NOTICE)).

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

- Use Go 1.25+ and `CGO_ENABLED=0`. One dependency is cross-platform:
  `github.com/klauspost/compress` (zstd). Two more go into Windows builds only:
  `golang.org/x/sys` (Job Object teardown) and `github.com/Microsoft/go-winio`
  (the opt-in `-listen-pipe` named-pipe transport).
- In-repo tests (`*_test.go`) cover the wire surface two ways. The first way is
  fast unit tests: frame encoding, dispatch / auth / error routing, the replay
  buffer, env merging, and the `-install` pipeline. The second way is a
  socket-integration suite (`harness_test.go` + `integration*_test.go`). That
  suite boots the daemon on a temp `AF_UNIX` socket. It then asserts every
  method's frames against committed golden fixtures
  (`testdata/socket_*.golden.json`). Thus CI gates compatibility without the
  reference binary. The suite runs on linux, macOS, **and Windows** in CI.
  Process fixtures come from the test binary itself (`helperproc_test.go`,
  `CLAUSTRUM_TEST_HELPER`), never from `/bin/*`, so the stream bytes match the
  goldens on every OS. Statement coverage is approximately 98%. The
  cross-binary battery that diffs against the reference daemon lives in
  `scratch/`.

## Architecture

There is one binary. A flag (`main.go`) selects the mode: `-serve`, `-bridge`,
`-stop`, `-version`, `-install`.

- **`rpc.go`** — request/response types, error codes, `dispatch` (parse →
  auth → version → route by namespace; `dispatch` checks auth *before* the
  jsonrpc version — probe-verified). Mistyped params → `-32602 Invalid params`.
- **`server.go`** — the `-serve` daemon: AF_UNIX listener (mode `0600`),
  per-conn read loop, **concurrent** dispatch, self-daemonize, graceful shutdown.
- **`methods_server.go` · `methods_files.go` · `methods_git.go` ·
  `methods_process.go`** — the 19 methods across `server.*` / `files.*` /
  `git.*` / `process.*`.
- **`results.go`** — result structs. Declare their fields in the exact order
  the reference emits. **Never use a map** (a map sorts its keys and diverges
  from the wire contract).
- **`process.go`** — `procManager` / `managedProc`: spawn in own process group,
  stream base64 stdout/stderr frames, async stdin writer (bounded queue +
  backpressure), per-process replay buffer, `reattach`.
- **`bridge.go`** — `-bridge`: a dumb stdio↔socket relay (what SSH attaches to).
- **`logging.go`** — tiny leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`, defaults
  to emit-everything). The logger puts the level tag *before* the `[Component]`
  prefixes, so existing greps continue to match.
- **`metrics.go`** — opt-in Prometheus counters at `/metrics`. A stdlib
  `net/http` listener serves them **only** when `-metrics-addr` is set (off by
  default; not part of the JSON-RPC wire). The daemon always does the counting,
  with atomics. Only the endpoint is opt-in.
- **`install.go`** — `-install`: CLI download / verify (SHA-256) / extract
  (zstd) / prune. **claustrum verifies a `-cli-url` download unconditionally.
  It verifies the local `-cli-zst` SFTP blob only when a `-cli-checksum` is
  supplied** — a conditional divergence from the reference, which the caller
  activates and an operator does not; see D1.
- `*_unix.go` / `*_windows.go` hold the OS-specific behavior: daemonize,
  process groups / Windows Job Objects for whole-tree kill, login-shell PATH
  extraction, the `-keep-children` POSIX-only policy via `honorKeepChildren`,
  and the Windows-only `-listen-pipe` named-pipe transport via
  `startPipeTransport` / `honorListenPipe` in `pipetransport_windows.go`. The
  JSON-RPC surface is identical everywhere.
- **`pipetransport.go`** — the opt-in, default-off, Windows-only `-listen-pipe`
  transport (CT-5). When the flag is set, `-serve` *additionally* serves the
  identical JSON-RPC dispatch over a Windows named pipe. That pipe is a second
  `acceptLoop` over the same `serveConn`, so there is no wire change. The
  daemon publishes the chosen pipe name to `rpc.pipe` beside the socket. The
  pipe has an owner-only DACL and the same `daemon.token` auth. Off ⇒
  byte-for-byte identical to the reference. The platform-neutral helpers are
  here. The go-winio wiring is in `pipetransport_windows.go`, and the
  non-Windows no-op stub is in `pipetransport_other.go`.

## Conventions

- **Byte-identical wire frames are the contract.** Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the validation battery green. An *intentional*
  divergence owes three things. Give it an entry with its decision rules in
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Give it its wire frames in
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md). Call it out in the PR.
- **Do not add a new dependency** without discussion. The permitted set is
  stdlib + zstd (`klauspost/compress`) + `golang.org/x/sys` and
  `github.com/Microsoft/go-winio` (both Windows-only), with `CGO_ENABLED=0`.
- **No telemetry, ever.**
- **Cross-platform parity** — keep OS specifics in `*_unix.go` / `*_windows.go`.
  `make all` must cross-compile cleanly for all six targets.
- Keep host-specific / reverse-engineering working notes out of the repo.
  `scratch/` is gitignored on purpose.

## Always do

- **Every change to `main` goes through a branch + PR — never commit or push to
  `main` directly.**
- Use **Conventional Commits** for PR titles (`feat:` / `fix:` / `docs:` /
  `chore:` / `ci:` …). PRs squash-merge, so the title becomes the commit
  subject. Titles are hygiene only — they do **not** drive releases.
- **Releases are changesets-only** (knope, not release-please). For a
  user-facing change, add a `.changeset/*.md` fragment with
  `knope document-change`. The fragment drives the version bump and the
  changelog. No fragment ⇒ no release. An internal PR (CI / tooling / refactor
  / tests) needs no fragment — apply the `no-changelog` label instead. See
  [`.changeset/`](.changeset/) and CONTRIBUTING.md → Changesets.
- **A changeset body is ONE line.** knope renders any multi-line body as a
  `####` heading block instead of a bullet. That breaks the changelog, and it
  already broke it in 1.7.2 and 1.7.3. Fold every detail into the single
  sentence — never a second line, never a second paragraph.
  `scripts/lint_changesets.py` gates this in CI and pre-commit.
- Before a PR, make all four of these true: `gofmt -l .` prints nothing,
  `go vet ./...` is clean, `golangci-lint run` is clean, and
  `go test -race ./...` is green. For a **wire-surface** change, also re-run the
  `scratch/` validation battery to confirm that the frames stay byte-identical.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas — Part A: always-on safety (hold these before touching code)

- **`process.spawn` runs arbitrary commands as the daemon's user — by design.**
  Treat socket + token as equivalent to shell access (threat model in
  [`SECURITY.md`](SECURITY.md)).
- **Three code paths give a caller- or operator-supplied path to
  `os.RemoveAll`.** Two of them are RPC paths. The daemon `~`-expands both of
  those paths first, so `"~"` once meant `os.RemoveAll($HOME)`. That destroyed
  the maintainer's home directory on 2026-08-02:
    - `files.extract_tar` wipes `destDir` — guarded by `wipesHomeDir` (`homeguard.go`).
    - `git.worktree_remove` deletes `worktreePath` when git fails — guarded by `wipesHomeDir`.
    - `-install` deletes `filepath.Join(cliDir, cliVersion)` (operator input) —
      guarded by **D6's single-path-component rule instead**, not `wipesHomeDir`.

  `wipesHomeDir` refuses a target that **is or contains** home. It resolves a
  relative path with `filepath.Abs` first. It still permits a path **under**
  home, because `~/.claude/…` is the daemon's own install path. The guard is
  always-on, not opt-in. **Any RPC path param that reaches a recursive delete
  owes this guard.** `IsAbs && !isFilesystemRoot` is *not* a substitute: home
  passes both tests, and that check resolves no relative path, so
  `worktreePath:".."` from a daemon in home still destroys home through its
  parent (measured). See
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md) D2.
- **Auth is in-band per request** (`"auth":"<token>"`). The daemon's token comes
  from `-token-file` or from `-token-fd`. With `-token-file` the daemon reads the
  file once and then unlinks it, so the token never lands in
  `/proc/<pid>/environ`. With `-token-fd` the daemon reads the token from an
  open descriptor and forwards it to the daemonized child over a pipe, so this
  handoff never touches disk. **No mode reads `CLAUDE_RPC_TOKEN`**: `-bridge`
  relays a client that supplies its own `auth`, and the daemon strips the
  variable from spawned children. **`server.shutdown` is the one method that is
  not authenticated** (parity with the reference — Desktop stops the daemon with
  no token in its environment), so `-stop` sends no `auth` member. Every other
  method rejects an unauthenticated request `-32001`.
- **The daemon persists its token to `daemon.token` (mode `0600`) beside the
  socket.** It writes the file atomically at startup and unlinks it on graceful
  shutdown (`tokenpersist.go`). A client can thus reconnect after the
  `-token-file` was unlinked, or after the `-token-fd` pipe closed. The fixed
  name and the socket-dir location *are* the reconnect contract, so they are
  deliberately not configurable. **Do not "fix"** the known parity caveats
  without making the change an opt-in divergence. There are two such caveats:
  two daemons in one dir collide on the file, and on Windows `0600` is not an
  owner-only DACL. See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token
  persistence.
- **A connection's requests dispatch concurrently.** Replies can return out of
  order, which matches the reference. Do not serialize them. The per-request
  goroutine **recovers from panics**. It replies with
  `-32603 "recovered panic: <v>"`. That frame is **claustrum's own and is NOT a
  parity claim** — the path is unreachable, so no client can observe it and it
  cannot diverge from anything. Do not add a golden for that frame (the battery
  never exercises it). Do not treat it as a wire contract. The tests provoke it
  through the `dispatchRequest` seam.
- **`-serve` makes no outbound connections. `-install` reaches the network only
  with `-cli-url`.** That download path verifies its SHA-256 before it extracts,
  unconditionally.
- **`-cli-probe-timeout` and `-libc-probe-timeout` are a swap footgun.** The two
  names differ only in their `cli`/`libc` prefix, they have the same type, and
  main's `-install` arm resolves them in consecutive statements. A swap compiles
  and passes every isolated test.
  `TestInstallArmWiresEachFlagToItsOwnGlobal` is the guard against it.
- **A disabled limiter bypasses its `io.LimitReader` / `context.WithTimeout`
  entirely. Never "simplify" it into a huge value.** For the caps, the `cap+1`
  (or `max-total+1`) arithmetic defines the boundary. For the deadlines, an
  unarmed cancel path makes the timeout `false` by construction. A huge constant
  is a different, observable behavior. This rule applies to every flag in
  Part B.

## Gotchas — Part B: the opt-in wire divergences

Seven divergences are opt-in flags. **All seven default OFF — that is the
parity position.** The reference applies no such cap and no such deadline at any
size or duration the probe could reach. A non-zero default would thus fail an
operation that the reference completes. (D4 is the non-threshold one. It is not
a cap and not a deadline but a `Mode().IsRegular()` check — the reference reads
`/dev/null` and blocks on a writerless FIFO instead of refusing either one.)
Claude Desktop owns the `-serve` / `-install` argv, so the **`claustrum.conf`
key is the reachable knob**, not the flag. Each disabled state bypasses its
limiter entirely (see Part A's "never simplify" rule).

**D5's deadline gates a destructive path.** `git.worktree_remove` treats a
failed git as permission to delete `worktreePath`. Therefore never read a fired
`git-timeout` as "git refused". Opting D5 in is wire-visible.

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

- **D1** — claustrum verifies the `-cli-zst` SFTP blob against a checksum **only
  when a `-cli-checksum` is supplied**. This is a *conditional*,
  caller-activated divergence, not an off-by-default flag. With no checksum
  claustrum stays trusting, so honest callers get byte-identical behavior.
- **D13** — verify-before-decompress *ordering*, with no wall-clock threshold.
  Its trigger is reachable. It stays **always-on but unresolved**, not
  justified.

Three documents hold the rest:

- The full catalog, the governing rules (rule 1–4 + clauses (a)/(b)/(c)), each
  divergence's default / activation / cost / reopen trigger, and the measured
  forensics behind them → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md).
- The per-method wire facts (params, result field order, error strings, the D5
  `signal: killed` frames) → [`docs/PROTOCOL.md`](docs/PROTOCOL.md).
- The driver-claim provenance that some of these rest on — "Desktop owns the
  argv", `cliError` classification, libc selection →
  [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) → Driver claims and their
  provenance.

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- Divergence catalog + rules → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)
- Internals + driver-claim provenance → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- Shipped ledger (completed work) → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local agent guardrails + agent-tool routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks). **knope**
  (`knope.toml`) automates the releases in three steps. First, pending
  `.changeset/` fragments make `knope-prepare.yml` open a
  `chore: prepare release X.Y.Z` PR, which bumps `VERSION` + `CHANGELOG.md`.
  Second, a merge of that PR makes `knope-release.yml` tag `v*` and create the
  GitHub Release. Third, that tag fires `release.yml`, which signs the artifacts
  and adds the SBOM and the SLSA provenance via
  [`.goreleaser.yaml`](.goreleaser.yaml). The `KNOPE_ENABLED` repo var gates
  this release automation.
- Claude Code / Anthropic API specifics → prefer current docs (context7 /
  find-docs) over memory — <https://docs.anthropic.com/en/docs/claude-code/overview>.
