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
  `golang.org/x/sys` and `github.com/Microsoft/go-winio`.
- In-repo tests cover the wire surface two ways: fast unit tests, and a
  socket-integration suite (`harness_test.go` + `integration*_test.go`) that
  boots the daemon on a temp `AF_UNIX` socket and asserts every method's frames
  against golden fixtures (`testdata/socket_*.golden.json`) — so CI gates
  compatibility without the reference binary, on linux, macOS **and** Windows.
  Process fixtures come from the test binary itself (`helperproc_test.go`),
  never from `/bin/*`, so stream bytes match the goldens on every OS. Statement
  coverage is approximately 98%. The cross-binary battery that diffs against
  the reference daemon lives in `scratch/`.

## Architecture

One binary; a flag selects the mode (`main.go`): `-serve`, `-bridge`, `-stop`,
`-version`, `-install`.

| File | Role |
|------|------|
| `rpc.go` | request/response types, error codes, `dispatch` (parse → auth → version → route; auth is checked *before* the jsonrpc version — probe-verified) |
| `server.go` | the `-serve` daemon: `AF_UNIX` listener (mode `0600`), per-conn read loop, **concurrent** dispatch, self-daemonize, graceful shutdown |
| `methods_*.go` | the 19 methods across `server.*` / `files.*` / `git.*` / `process.*` |
| `results.go` | result structs, fields declared in the exact order the reference emits — **never a map** (a map sorts its keys and diverges from the wire contract) |
| `process.go` | `procManager` / `managedProc`: spawn in own process group, base64 stream frames, async stdin writer (bounded queue + backpressure), per-process replay buffer, `reattach` |
| `bridge.go` | `-bridge`: a stdio↔socket relay — what SSH attaches to; it injects no auth |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`, default emit-everything); the level tag goes *before* the `[Component]` prefixes, so existing greps keep matching |
| `metrics.go` | opt-in Prometheus counters at `/metrics` — a listener exists **only** when `-metrics-addr` is set; counting is always-on atomics |
| `install.go` | `-install`: CLI download / verify (SHA-256) / extract (zstd) / prune — a `-cli-url` download is verified unconditionally; the local `-cli-zst` blob only when a `-cli-checksum` is supplied (D1) |
| `*_unix.go` / `*_windows.go` · `pipetransport*.go` | OS specifics (daemonize, process groups / Windows Job Objects, login-shell PATH, the POSIX-only `-keep-children`) and the opt-in, default-off, Windows-only `-listen-pipe` named-pipe transport (CT-5) |

The JSON-RPC surface is identical on every OS. Full internals →
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

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
  `knope document-change`; the fragment drives the version bump and the
  changelog. No fragment ⇒ no release. An internal PR needs no fragment —
  apply the `no-changelog` label instead. See [`.changeset/`](.changeset/) and
  CONTRIBUTING.md → Changesets.
- **A changeset body is ONE line.** knope renders any multi-line body as a
  `####` heading block instead of a bullet. That breaks the changelog, and it
  already broke it in 1.7.2 and 1.7.3. Fold every detail into the single
  sentence. `scripts/lint_changesets.py` gates this in CI and pre-commit.
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

Seven divergences are opt-in flags — D3 (`max-extract-bytes`), D4
(`files-read-regular-only`), D5 (`git-timeout`), D10 (`max-cli-bytes`), D11
(`cli-probe-timeout`), D12 (`cli-download-timeout`), D14 (`libc-probe-timeout`)
— and **all seven default OFF: that is the parity position.** The reference
applies no such cap, deadline, or refusal at any input the probe could reach,
so any non-off default would fail an operation the reference completes. Claude Desktop owns the `-serve` / `-install` argv, so the
**`claustrum.conf` key is the reachable knob**, not the flag. Each disabled
state bypasses its limiter entirely (Part A's "never simplify" rule).

**D5's deadline gates a destructive path.** `git.worktree_remove` treats a
failed git as permission to delete `worktreePath`. Therefore never read a fired
`git-timeout` as "git refused". Opting D5 in is wire-visible.

The two non-flag divergences: **D1** — claustrum verifies the `-cli-zst` SFTP
blob **only when a `-cli-checksum` is supplied** (conditional and
caller-activated; an absent checksum stays trusting, so honest callers get
byte-identical behavior). **D13** — verify-before-decompress *ordering*;
always-on but **unresolved**, not justified.

The flag/key table, the governing rules (rule 1–4 + clauses (a)/(b)/(c)), each
divergence's default / activation / cost / reopen trigger →
[`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Per-method wire frames →
[`docs/PROTOCOL.md`](docs/PROTOCOL.md). Driver-claim provenance ("Desktop owns
the argv", `cliError` classification, libc selection) →
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- Divergence catalog + rules → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)
- Internals + driver-claim provenance → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- Shipped ledger (completed work) → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local agent guardrails + agent-tool routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks). Releases: pending
  `.changeset/` fragments → **knope** opens a version PR (`knope-prepare.yml`);
  merging it tags `v*` (`knope-release.yml`), and the tag fires `release.yml` —
  signed + SBOM + SLSA via [`.goreleaser.yaml`](.goreleaser.yaml). Gated on the
  `KNOPE_ENABLED` repo var.
- Claude Code / Anthropic API specifics → prefer current docs (context7 /
  find-docs) over memory — <https://docs.anthropic.com/en/docs/claude-code/overview>.
