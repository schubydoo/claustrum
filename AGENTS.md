# Claustrum

Claustrum is a tiny Go daemon with few dependencies. It is a clean-room
reimplementation of the small daemon that hosts a remote Claude Code session
over SSH. It combines three parts: a local CLI-version manager, a process
supervisor, and a JSON-RPC multiplexer over an `AF_UNIX` socket. The
multiplexer holds a replay buffer. Black-box probes of the reference binary
captured a behavioral contract, and claustrum is built to that contract. No
code was copied and no code was decompiled (see [`NOTICE`](NOTICE)).

The one hard rule: stay byte-identical to the JSON-RPC frames of the reference
daemon. The wire surface is the product.

## Build · run · test

| Task | Command |
|------|---------|
| Build the binary | `make build`. CGO is off, the build uses `-trimpath`, and the binary is stripped. Output: `./claustrum` |
| Cross-build all 6 targets | `make all`. Targets: linux/darwin/windows × amd64/arm64 → `dist/` |
| Format | `make fmt` runs `gofmt -w`. `gofmt -l .` must be empty |
| Vet | `make vet` (`go vet ./...`) |
| Lint | `golangci-lint run ./...`. The configuration is `.golangci.yml` (v2) |
| Tests | `go test -race ./...`. This runs the unit suites and the socket-integration suites |
| Validation battery | `scratch/probe/validate.sh`. It diffs frames against the reference. The script is gitignored |

- Use Go 1.25+ and `CGO_ENABLED=0`. The build toolchain is deliberately held
  below 1.27. The default `jsonv2` in 1.27 moves inherited wire bytes, so build
  and CI resolve Go from `go.mod`, never from `go-version: stable`. See
  [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md) → toolchain-induced
  drift. One dependency is cross-platform:
  `github.com/klauspost/compress` (zstd). Two more go into Windows builds only:
  `golang.org/x/sys` and `github.com/Microsoft/go-winio`.
- In-repo tests cover the wire surface two ways: fast unit tests, and a
  socket-integration suite (`harness_test.go` + `integration*_test.go`). The
  suite boots the daemon on a temp `AF_UNIX` socket. It asserts the frames of
  every method against golden fixtures (`testdata/socket_*.golden.json`). CI
  therefore gates compatibility without the reference binary, on linux, macOS
  and Windows. Process fixtures come from the test binary itself
  (`helperproc_test.go`), never from `/bin/*`, so stream bytes match the
  goldens on every OS. Statement coverage is approximately 99%. The
  cross-binary battery that diffs against the reference daemon lives in
  `scratch/`.

## Architecture

There is one binary. A flag selects the mode (`main.go`): `-serve`, `-bridge`,
`-stop`, `-version`, `-install`, `-probe-cli`. `19f30c46` added the one-shot
`-probe-cli` CLI-runnability probe.

| File | Role |
|------|------|
| `rpc.go` | request/response types, error codes, and `dispatch`. The dispatch order is parse → auth → version → route. Probes showed that dispatch tests auth before the jsonrpc version, except on the unauthenticated `server.shutdown` |
| `server.go` | the `-serve` daemon: `AF_UNIX` listener (mode `0600`), per-conn read loop, concurrent dispatch, self-daemonize, graceful shutdown |
| `methods_*.go` | the 19 methods across `server.*` / `files.*` / `git.*` / `process.*` / `plugins.*`. `7d193f89` removed `server.version`. `19f30c46` added `plugins.prune` |
| `results.go` | result structs, with fields declared in the exact order the reference emits. Never use a map. A map sorts its keys and diverges from the wire contract |
| `process.go` | `procManager` / `managedProc`: spawn in own process group, base64 stream frames, async stdin writer (bounded queue + backpressure), per-process replay buffer, `reattach` |
| `bridge.go` | `-bridge`: a stdio↔socket relay. SSH attaches to it. It injects no auth |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`, default emit-everything). The level tag goes before the `[Component]` prefixes, so existing greps keep matching |
| `metrics.go` | opt-in Prometheus counters at `/metrics`. The `-metrics-addr` flag creates the listener. Without that flag there is no listener. Counting is always-on atomics |
| `wirelog.go` | opt-in `-wire-log` JSON-RPC frame capture (CT-3). It is a pure side channel over already-marshaled bytes, and it is off by default. It redacts credentials by key only, not by payload contents. It forces `0600` on every open |
| `install.go` | `-install`: CLI download / verify (SHA-256) / extract (zstd) / prune. A `-cli-url` download is verified unconditionally. If a `-cli-checksum` is supplied, claustrum also verifies the local `-cli-zst` blob. Without `-cli-checksum` it does not verify that blob (D1) |
| `*_unix.go` / `*_windows.go` · `pipetransport*.go` | OS specifics (daemonize, process groups / Windows Job Objects, login-shell PATH, the POSIX-only `-keep-children`) and the opt-in, default-off, Windows-only `-listen-pipe` named-pipe transport (CT-5) |

The JSON-RPC surface is identical on every OS. Full internals →
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Conventions

- Byte-identical wire frames are the contract. Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the validation battery green. An intentional
  divergence owes three things. Give it an entry with its decision rules in
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Give it its wire frames in
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md). Call it out in the PR.
- Do not add a new dependency without discussion. The permitted set is
  stdlib + zstd (`klauspost/compress`) + `golang.org/x/sys` and
  `github.com/Microsoft/go-winio` (both Windows-only), with `CGO_ENABLED=0`.
- Never add telemetry.
- Cross-platform parity is a rule. Keep OS specifics in `*_unix.go` /
  `*_windows.go`. `make all` must cross-compile cleanly for all six targets.
- Keep host-specific / reverse-engineering working notes out of the repo.
  `scratch/` is gitignored on purpose.

## Always do

- Every change to `main` goes through a branch and a PR. Never commit or push
  to `main` directly.
- Use Conventional Commits for PR titles (`feat:` / `fix:` / `docs:` /
  `chore:` / `ci:` …). PRs squash-merge, so the title becomes the commit
  subject. Titles are hygiene only. They do not drive releases.
- Releases are changesets-only (knope, not release-please). For a
  user-facing change, add a `.changeset/*.md` fragment with
  `knope document-change`. The fragment drives the version bump and the
  changelog. No fragment ⇒ no release. An internal PR needs no fragment.
  Apply the `no-changelog` label instead. See [`.changeset/`](.changeset/) and
  CONTRIBUTING.md → Changesets.
- A changeset body is ONE line. knope renders any multi-line body as a
  `####` heading block instead of a bullet. That breaks the changelog, and it
  already did so in 1.7.2 and 1.7.3. Fold every detail into that one
  line. Several sentences on it are fine. `scripts/lint_changesets.py` gates this in CI and pre-commit.
- Before a PR, make all four of these true: `gofmt -l .` prints nothing,
  `go vet ./...` is clean, `golangci-lint run` is clean, and
  `go test -race ./...` is green. For a wire-surface change, also re-run the
  `scratch/` validation battery to make sure that the frames stay
  byte-identical.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas — Part A: always-on safety (hold these before touching code)

- `process.spawn` runs arbitrary commands as the user of the daemon. This is by
  design. Treat socket + token as equivalent to shell access. The threat model
  is in [`SECURITY.md`](SECURITY.md).
- Four code paths give a caller-supplied or operator-supplied path to
  `os.RemoveAll`. Three of them are RPC paths. The daemon `~`-expands those RPC
  paths first, so `"~"` once meant `os.RemoveAll($HOME)`. That destroyed
  the maintainer's home directory on 2026-08-02:
    - `files.extract_tar` wipes `destDir`. `wipesHomeDir` (`homeguard.go`) guards it.
    - If git fails for a non-locked reason, `git.worktree_remove` deletes
      `worktreePath`. Since `7d193f89`, a LOCKED worktree is refused, not
      deleted. Also since `7d193f89`, the containment of the reference refuses a
      home path first: `worktreePath` must be strictly inside `baseRepo`. On the
      default branch `wipesHomeDir` is therefore defense-in-depth. It fires in
      one case only: a repo is an ancestor of home. On the `worktreeRoot` /
      `external_root` branch that in-repo containment does not apply, so there
      `wipesHomeDir` is the active home guard. Both branches run it before the
      delete.
    - When `git.worktree_create` rolls back a worktree, it deletes
      `worktreePath`. The rollback runs in two cases. The first case is a failed
      `git worktree add`: the leaf it made is removed, so a retry at the same
      path with a fresh branch succeeds. An add cut short by the `timeoutMs` of
      the caller answers `timeout` and leaves the leaf. The second case is a
      post-checkout drain that exceeded the `timeoutMs` of the caller.
      `wipesHomeDir` guards both deletes as defense-in-depth behind the
      containment that create applies itself. Create also tests the checkpoint
      identity of the leaf again, so a swap during the add or the drain cannot
      redirect the delete.
    - `-install` deletes `filepath.Join(cliDir, cliVersion)`, which is operator
      input. The single-path-component rule of D6 guards it instead, not
      `wipesHomeDir`.

  `wipesHomeDir` refuses a target that is home or contains home. It resolves a
  relative path with `filepath.Abs` first. It still permits a path under home,
  because `~/.claude/…` is the install path of the daemon itself. The guard is
  always-on, not opt-in. Any RPC path param that reaches a recursive delete
  owes this guard. `IsAbs && !isFilesystemRoot` is not a substitute: home
  passes both tests, and neither test resolves a relative path. A daemon in home
  that receives `worktreePath:".."` therefore still destroys home through the
  parent directory (measured). See
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md) D2.
- `plugins.prune` is a further `os.RemoveAll` site, and it is safe by
  construction. It deletes `<socket-derived-root>/<hash>`. The root comes from
  the socket layout of the daemon itself, not from an RPC or operator path. The
  leaf must match `^[0-9a-f]{16}$` (`pluginHashRE`). The path is never
  caller-supplied and never `~`-expanded, so it needs no `wipesHomeDir` guard.
  The four paths above remain the only caller-supplied or operator-supplied
  deletes.
- Auth is in-band per request (`"auth":"<token>"`). The token of the daemon
  comes from `-token-file` or from `-token-fd`. With `-token-file` the daemon
  reads the file once and then unlinks it, so the token never lands in
  `/proc/<pid>/environ`. With `-token-fd` the daemon reads the token from an
  open descriptor. It forwards the token to the daemonized child over a pipe,
  so this handoff never touches disk. No mode reads `CLAUDE_RPC_TOKEN`:
  `-bridge` relays a client that supplies its own `auth`, and the daemon strips
  the variable from spawned children. `server.shutdown` is the one method that
  is not authenticated. That is parity with the reference. Desktop stops the
  daemon with no token in its environment, so `-stop` sends no
  `auth` member. Every other method rejects an unauthenticated request `-32001`.
- The daemon persists its token to `daemon.token` (mode `0600`) beside the
  socket. It writes the file atomically at startup and unlinks it on graceful
  shutdown (`tokenpersist.go`). A client can thus reconnect after the daemon
  unlinked the `-token-file`, or after the `-token-fd` pipe closed. The fixed
  name and the socket-dir location are the reconnect contract, so they are
  deliberately not configurable. One parity caveat remains: on Windows `0600`
  is not an owner-only DACL. Do not "fix" that caveat without making the change
  an opt-in divergence. The old caveat was that two daemons in one dir collide
  on the file. The run-dir lock now bounds it (`daemon.lock`, parity with
  `4534d86`). On Linux and macOS a new daemon evicts a prior live same-socket
  daemon before it binds. The two daemons therefore no longer coexist to
  collide. Two cases remain: eviction is refused, or the host is Windows, which
  ships no run-dir lock. That lock is parity, not a claustrum "fix". The macOS
  holder-verification that claustrum applies to the lock is a hardening
  divergence (D15). See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token
  persistence / Run-dir lock.
- The requests of one connection dispatch concurrently. Parse and auth errors
  are the exception: the read loop writes them
  ([`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Transport). Replies can return out
  of order, which matches the reference. Do not serialize them. The per-request
  goroutine recovers from panics. It replies with
  `-32603 "recovered panic: <v>"`. For `server.shutdown` it writes no frame at
  all. That frame is claustrum's own and is NOT a parity claim. The path
  is unreachable, so no client can observe it and it cannot diverge from
  anything. Do not add a golden for that frame. The battery never exercises it.
  Do not treat it as a wire contract. The tests provoke it through the
  `dispatchRequest` seam.
- `-serve` makes no outbound network connections. `-install` reaches the network
  only with `-cli-url`. That download path verifies its SHA-256 before it
  extracts, unconditionally. Every dial `-serve` makes is to a local `AF_UNIX`
  socket, never network egress.
- `-cli-probe-timeout` and `-libc-probe-timeout` are a swap footgun. The two
  names differ only in their `cli`/`libc` prefix, they have the same type, and
  the `-install` arm of main resolves them in consecutive statements. A swap
  compiles and passes every isolated test.
  `TestInstallArmWiresEachFlagToItsOwnGlobal` is the guard against it.
- A disabled limiter bypasses its `io.LimitReader` / `context.WithTimeout`
  entirely. Never "simplify" it into a huge value. For the caps, the `cap+1`
  (or `max-total+1`) arithmetic defines the boundary. For the deadlines, an
  unarmed cancel path makes the timeout `false` by construction. A huge constant
  is a different, observable behavior. This rule applies to every flag in
  Part B.

## Gotchas — Part B: the opt-in wire divergences

Seven divergences are opt-in flags:

- D3 (`max-extract-bytes`)
- D4 (`files-read-regular-only`)
- D5 (`git-timeout`)
- D10 (`max-cli-bytes`)
- D11 (`cli-probe-timeout`)
- D12 (`cli-download-timeout`)
- D14 (`libc-probe-timeout`)

All seven default OFF. That is the parity position.
The reference applies no such cap, deadline, or refusal at any input that the
probe can reach. A non-off default therefore fails an operation that the
reference completes. Claude Desktop owns the `-serve` / `-install` argv, so the
`claustrum.conf` key is the reachable knob, not the flag. Each disabled state
bypasses its limiter entirely. That is the "never simplify" rule of Part A.

The deadline of D5 gates a destructive path. `git.worktree_remove` treats a
non-locked git failure as permission to delete `worktreePath`. Since
`7d193f89`, a LOCKED worktree is refused before the delete. Therefore never
read a fired `git-timeout` as "git refused". Opting D5 in is wire-visible.

This section holds two non-flag divergences. D1: if a `-cli-checksum` is
supplied, claustrum verifies the `-cli-zst` SFTP blob. Without `-cli-checksum`
it does not verify that blob. D1 is therefore conditional and caller-activated.
Without that flag the path stays trusting, so honest callers get byte-identical
behavior. D13: verify-before-decompress ordering. D13 is always-on, but it is
unresolved, not justified.

D17 is off-wire and macOS-only. The host cleaner reads an `lsof` run it gave
up on as busy. A completed run that found nothing reads as not busy. The
reference side is not probe-measured. The
harm it refuses is the cleaner SIGTERMing a daemon that is serving a client on a
host where `lsof` cannot answer. The sibling lock read is deliberately NOT
covered. See the entry.

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
  `.changeset/` fragments → knope opens a version PR (`knope-prepare.yml`).
  Merging it tags `v*` (`knope-release.yml`), and the tag fires `release.yml`.
  That release is signed and carries SBOM and SLSA, via
  [`.goreleaser.yaml`](.goreleaser.yaml). Gated on the `KNOPE_ENABLED` repo var.
- Claude Code / Anthropic API specifics → prefer current docs (context7 /
  find-docs) over memory:
  <https://docs.anthropic.com/en/docs/claude-code/overview>.
