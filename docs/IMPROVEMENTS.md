# claustrum — improvement backlog (stack-ranked)

Everything here **retains wire compatibility** (no change to method shapes, error
codes, or frame formats unless explicitly noted as protocol-safe). Ranked by
**lowest cost / highest impact** first within each tier. Each item carries its
status (✅ done · ⬜ open) and an **Impact / Cost** rating (H/M/L).

> Compatibility rule of thumb: anything touching `rpc.go`, `methods_*.go`,
> `process.go`, or `results.go` must keep the validation battery byte-identical.

## Tier 1 — quick wins (do first)

### 1 · CI workflow ✅ — impact H / cost L

`go vet`, `gofmt -l`, `make all`, `go test` gate every PR; catches cross-compile
and format regressions. Pure additive. Shipped as `.github/workflows/ci.yml`.

### 2 · In-repo Go test suite ✅ — impact H / cost M

Shipped: `harness_test.go` + `integration_test.go` + `integration_fs_git_test.go`
boot the daemon on a temp socket and assert each method's frames over the real
wire path — CI now gates compatibility without the reference binary.

### 3 · Golden-frame fixtures ✅ — impact H / cost M

Shipped: `testdata/socket_*.golden.json` (responses/errors, `files.*`, `git.*`),
asserted byte-equal; regenerate with `go test -run Socket -update`. Locks the
contract so refactors can't drift silently.

### 4 · Atomic `-install` extract ✅ — impact M / cost L

`ensureCLI` now decompresses + chmods + verifies at `cliPath.tmp`, then
`os.Rename`s into place, so an interrupted install never leaves a half-written
or non-runnable cliPath. Behavior-compatible — the end state and
`__INSTALL_RESULT__` facts are identical to the reference's in-place extract
(cliPath appears only as a complete 0755 verified binary; same "not runnable"
error).

### 5 · Timeouts on `git`/`exec` calls ✅ — impact M / cost L

`git.*` and the `-install` libc probe shelled out with no deadline; a wedged
git/ldd hung a request goroutine forever. Both are now wrapped in
`exec.CommandContext`: the `ldd --version` probe (`lddProbeTimeout`, security
fix S4 / HackerOne [#3793023](https://hackerone.com/reports/3793023)) and every
`git` invocation (`gitTimeout` 60s — a timed-out git reports `ok=false`, the
same as any other failure). Happy-path results/frames unchanged; an
attack/pathological-path-only divergence from the reference (which has no
deadline).

### 6 · pre-commit + `gofmt`/`vet` hooks ✅ — impact M / cost L

Shipped a zero-dependency `pre-commit` hook tracked in `.githooks/`, installed
via `make hooks` (sets `core.hooksPath`). Mirrors CI's lint job in the same
order — `gofmt -l`, `go vet ./...`, a `go mod tidy` cleanliness check (run
against a backup so it never dirties go.mod/go.sum), and `golangci-lint` when on
PATH. Early-exits for non-Go commits; bypass with `--no-verify`. No Python
`pre-commit` framework (keeps the "no new dependencies" rule); also added
`make lint`/`make test` and documented `make hooks` in CONTRIBUTING.

### 7 · `go vet`-clean + `staticcheck` in CI ✅ — impact M / cost L

Shipped via `golangci-lint` (`.golangci.yml`, standard set incl. staticcheck +
govet + errcheck + ineffassign + unused, plus misspell/unconvert), wired into
the CI `lint` job.

## Tier 2 — medium

### 8 · Bounded replay buffer (ring) ✅ — impact M-H / cost M

Shipped in #58: each per-process buffer is capped at 50 MiB of base64 data (was
unbounded — a noisy long-lived process grew memory without bound); the oldest
frames drop and `firstSeq` advances past the cap. **Protocol-safe** —
`reattach` returns `firstSeq`, so clients handle the moved floor.

### 9 · stdin backpressure ✅ — impact M / cost M

- `process.stdin` used to write synchronously, so a slow/non-reading child
  blocked the dispatch goroutine once the 64 KB pipe filled.
- **Parity gap** — a probe showed the reference returns `{success:true}`
  immediately (async/queued) where claustrum blocked.
- Now each proc has a `stdinWriter` goroutine draining a bounded
  (`stdinQueueCap`, 8 MiB) FIFO queue; `process.stdin` enqueues and returns
  immediately. A full queue applies backpressure and logs the reference's
  `stdin backpressure: queue full` guard.
- Re-probe: claustrum now matches the reference (success in ~350 ms vs
  previously blocked); `-serve` battery byte-identical. The exact queue
  threshold is a stderr-log edge, not a wire frame.

### 10 · Fuzz the JSON-RPC parser ✅ — impact M / cost L-M

Shipped `fuzz_test.go`: `FuzzDispatch` (parse→auth→version→route→param-presence,
side-effectful methods skipped so a coverage-guided fuzzer can't drive
spawn/extract_tar/read) + `FuzzBindParams` (param-type binding, pure). Seeds run
in CI; ~1.5M execs clean under active `-fuzz`. _Optional follow-up:_ a short
`-fuzztime` CI job for ongoing fuzzing.

### 11 · Release automation ✅ — impact H / cost M

Shipped `.goreleaser.yaml` + `release.yml`: 6-target builds, checksums, syft
CycloneDX SBOM, cosign signing, and SLSA `*.intoto.jsonl` provenance — satisfies
Scorecard SBOM + Signed-Releases (10/10). Also shipped `release-please.yml` +
`pr-auto-update.yml` for automated version PRs (claustrum-ci[bot]).

### 12 · Pin the Go toolchain ✅ — impact M / cost L

go.mod carries an explicit `toolchain` directive alongside the `go` directive
(currently `toolchain go1.26.4` / `go 1.25.0`); with CI/release on
`go-version-file: go.mod`, setup-go provisions that exact toolchain, so release
builds are reproducible against a known patch. Renovate bumps the patch over
time. (The pin first moved 1.24.4 → 1.25.11 when x/sys was bumped for
GO-2026-5024 — see #14 — and Renovate has since advanced it to go1.26.4.)

### 13 · Structured/leveled logging ✅ — impact M / cost L-M

Shipped a tiny leveled logger (`logging.go`):

- The daemon's diagnostic `log.Printf("[Component] …")` calls now go through
  `logDebugf`/`logInfof`/`logWarnf`/`logErrorf`.
- The level tag is prepended *before* the existing
  `[Server]`/`[process.Manager]`/`[frameSink]`/`[shellenv]` prefixes — left
  **byte-intact** so greps keep working.
- Threshold from `CLAUSTRUM_LOG_LEVEL` (`debug`|`info`|`warn`|`error`),
  **defaulting to `debug`** so output is unchanged unless an operator raises it.
- Still routes through the stdlib default logger (timestamps + `log.SetOutput`
  test capture intact).
- The CLI's fatal `claustrum: …` startup errors are left as-is — user-facing
  exit messages, not diagnostic logs.
- Stderr-only; the wire surface is untouched (goldens unchanged).

## Tier 3 — larger / lower-priority

### 14 · Windows process-tree kill via Job Objects ✅ — impact M / cost M-H

- Spawned children are now confined to a Windows Job Object (`confineProcess`
  in `sysproc_windows.go`); `process.kill`/`killAll` call `TerminateJobObject`,
  tearing down the whole tree instead of just the parent (the old best-effort
  `TerminateProcess` leaked grandchildren).
- The job carries `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, so the handle is closed
  on child exit (reaping stragglers) and the tree also dies if the daemon
  itself exits.
- Unix is unchanged (process-group kill); a new cross-platform `procGroup`
  abstraction unifies both.
- Job Object failure falls back to the old parent-only kill — spawns never
  fail because of confinement.
- Added dependency `golang.org/x/sys` (**Windows-only** — not compiled into
  other targets; discussed/approved). Initially pinned v0.33.0; bumped to
  **v0.44.0** to clear **GO-2026-5024** (`NewNTUnicodeString` overflow —
  unreachable in claustrum, which never calls it, but version-flagged by
  Scorecard), which in turn required the Go bump to **1.25** (the fix only
  landed in x/sys v0.44.0, whose `go` directive is 1.25).
- No wire change — stderr/OS behavior only; socket goldens unchanged.

### 15 · Docs site (mkdocs) ✅ — impact M / cost M

`docs/` is now published via **mkdocs-material** to GitHub Pages (`mkdocs.yml` +
`docs/index.md` landing page). A `docs` workflow runs `mkdocs build --strict` on
every PR (catches broken links/nav) and deploys to Pages on push to `main`
(SHA-pinned `upload-pages-artifact`/`deploy-pages`, least-privilege
`pages: write`/`id-token: write` scoped to the deploy job). Root files
(Contributing/Security/Changelog) are linked out to GitHub from the nav to avoid
duplicating the canonical copies. Site: <https://schubydoo.github.io/claustrum/>.

### 16 · `/metrics` counters ✅ — impact L-M / cost M

Shipped opt-in Prometheus metrics (`metrics.go`):

- A process-wide atomic counter registry: connections, process spawns/exits,
  reattaches, stream/stdin bytes. Counting is always-on; the endpoint is the
  opt-in part.
- Exposed at `/metrics` via a stdlib `net/http` listener — **only** when
  `-metrics-addr` is set (off by default, no listener otherwise); stopped on
  teardown.
- Counts only (no command output/tokens), no auth → bind to loopback.
  Documented in [PROTOCOL.md](PROTOCOL.md) +
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).
- Pure stdlib, no wire change, goldens unchanged.

### 17 · Duplicate-`id` spawn policy ✅ — impact L / cost L

Clarified + pinned: reusing a still-live `id` succeeds and replaces the registry
entry (matching the reference's "both succeed"). **Divergence:** claustrum now
also tears down the now-orphaned previous process tree (reusing the #14
`procGroup` kill) — it would otherwise leak, unreachable via
`kill`/`stdin`/`reattach` and missed by `killAll`. Its subscribers are dropped
first so no stray exit/stdout frames arrive under the reused id. OS-level only,
no wire change (`TestSpawnDuplicateIDReplacesAndKillsOld`; documented in
[PROTOCOL.md](PROTOCOL.md)).

### 18 · Token from fd/stdin ✅ — impact L / cost L

Shipped `-token-fd <n>` (e.g. `0` for stdin):

- The `-serve` daemon reads the auth token from an open descriptor instead of
  `-token-file`, so it never touches disk.
- Since `-serve` self-daemonizes, the parent reads the fd and **forwards the
  token to the detached child over an inherited pipe** (`readTokenFD` +
  `daemonizeWithToken`; the child reads the fd named by
  `CLAUSTRUM_TOKEN_PIPE`) — never via disk, argv, or environ.
- Additive/off-wire: `-token-file` callers and the reference are unaffected.
- `readTokenFD` unit-tested; the full fd→pipe→auth path validated live
  (`server.ping` authenticates with the forwarded token; wrong token rejected).
- Documented in [PROTOCOL.md](PROTOCOL.md) +
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

### 19 · Docs-site visibility/formatting pass ✅ — impact L / cost L-M

Restructured the prose-heavy tables that read as thin, clipped slivers on the
published site: this backlog is now per-item sections (you're reading the
result), and the protocol reference's `files.*`/`git.*`/`process.*` method
tables became per-method sections with result lines + bullet notes — every
method now gets its own table-of-contents entry on the site. Earlier site fixes
in the same vein: `pymdownx.emoji` for Material icons (#75), `pymdownx.tilde` +
a 72rem content column (#77). Site-only; no wire/behavior change.

### 20 · Windows CI test runner ✅ — impact M / cost M

- The problem: the CI test matrix ran `ubuntu-latest` + `macos-latest`; the
  cross-build job proved the Windows targets *compile*, but nothing ever
  *executed* the `*_windows.go` code — in particular the #14 Job Object
  confinement/teardown in `sysproc_windows.go` shipped without ever having run
  in CI. Mutation testing quantified the hole: every `sysproc_windows.go`
  mutant (9) is structurally **NOT COVERED** on a Linux/macOS runner — the
  file is not even compiled there, so no test added on those platforms can
  ever reach it.
- Shipped: a `windows-latest` leg in the `test` matrix. The suites' Unix
  fixtures (`/bin/echo`-style commands, `sh -c` scripts) were replaced by the
  stdlib helper-process pattern (`helperproc_test.go`: the test binary doubles
  as a cross-platform echo/cat/sleep/… via `CLAUSTRUM_TEST_HELPER`), which
  also keeps the streamed bytes byte-identical across OSes — no CRLF or
  cmd.exe quoting drift against the committed goldens. `AF_UNIX` sockets work
  natively on Windows ≥ 1803, so the socket suite runs unchanged.
- `sysproc_windows_test.go` mirrors the Unix group-kill test against a real
  two-level process tree: job-wide `signal`, `KILL_ON_JOB_CLOSE` reap on
  `close`, the no-job/nil-receiver fallback to a parent-only kill, and close
  idempotency — behavioral coverage for all the previously unreachable
  `sysproc_windows.go` mutants.
- Targeted skip: `TestSocketFilesBattery` skips on Windows — its golden pins
  the Unix reference capture, including the `files.stat` mode string
  (`-rw-r--r--`), which Windows stat cannot reproduce. Everything else runs.
- Caveat: gremlins itself still runs on Linux only, so the mutation report
  will keep listing the 9 `sysproc_windows.go` mutants as not-covered — the
  coverage is real but lives in the Windows CI leg, not in the mutation run.
- Mutation baseline (gremlins `--integration`, 2026-06-10, post-#86):
  **93.91% efficacy** (185 killed / 12 lived / 6 timed out), mutator coverage
  75.48% (203 runnable / 64 not covered). The not-covered set is mostly the
  out-of-process daemon lifecycle
  in `server.go`/`main.go` (validated by the external battery; can't register
  in an in-process coverage profile), the `rpc.go` error-code constant
  literals (constants never appear in a coverage profile — an artifact, not a
  gap), and the Windows-only code above (now executed by the Windows CI leg).

### 21 · Exited-child group-kill guard + LIVED-mutant triage ✅ — impact S / cost S

- `kill` / `killAll` / the duplicate-id replace now **skip children that have
  already exited**: once `cmd.Wait` reaps a child its Unix pgid can be
  recycled, so the previous unconditional negative-pid SIGKILL could hit an
  unrelated process group (Windows was already immune — the job handle pins
  identity). OS-level hardening, documented as a divergence in
  [`PROTOCOL.md`](PROTOCOL.md) `process.kill`; no wire frame depends on the
  signal side effect. Found by an independent review pass pre-v1.1.0.
- The 12 LIVED mutants from the post-#20 run (94.06% efficacy, 190 K / 12 L,
  mutator coverage 75.94%) triaged. **Seven are equivalent or impractical** —
  the historical set with shifted line numbers — don't chase them:
  `bridge.go:41` (zero-byte stdout write is a no-op), `install.go:183` (5-min
  http timeout magnitude needs a multi-minute hang), `install.go:221` (sort
  comparator under mtime ties; `sort.Slice` is unstable), `server.go:298`
  (bufio initial-size hint, not the byte-pinned 1 MiB cap),
  `methods_files.go:211` ×2 (per-file LimitReader terms; a truncated file
  always trips the cumulative cap), `metrics.go:61` (ReadHeaderTimeout
  magnitude). **Five were real assertion gaps, now killed**: `process.go:170`
  (a spurious confinement-failed warn is asserted absent), `process.go:279` ×3
  (the backpressure gate's three conjuncts: sole-over-cap write accepted on an
  empty queue, exact-cap fit accepted, queue never exceeds the cap while
  parked), `process.go:318` (a second stdin chunk after a successful write
  must still be delivered — the writer survives success).

### 22 · Spawn/exec syscall hardening — no daemon `chdir`, whole-group kill ✅ — impact S / cost S

A syscall-trace differential (run both daemons through one deterministic session
under `strace -f`, normalize, diff per logical op) surfaced two places where
claustrum's filesystem/process syscalls differ from the reference while emitting
**byte-identical frames** (the validation battery already pins every `git.*` and
`process.*` response). Both differences are kept on purpose — claustrum is the
safer of the two in each case. Recording them here so a future contributor
doesn't "re-align" them to the reference and quietly regress the safety:

- **`git.*` runs as `git -C <repo>`; the daemon never `chdir`s.** The reference
  `chdir`s its own process into the repo before each bare `git` call; claustrum
  passes `-C` and leaves the daemon cwd untouched. Because a connection's
  requests dispatch **concurrently**, a process-global `chdir` would race any
  other in-flight request that resolves a relative path — `-C` sidesteps it
  entirely. (The plumbing subcommands also differ —
  `rev-parse --is-inside-work-tree` / `symbolic-ref --short HEAD` vs the
  reference's `--git-dir` / `branch --show-current` — for the same resulting
  frames.)
- **`process.kill` signals the whole process group (`kill(-pgid, sig)`).** The
  reference `pidfd_send_signal`s only the direct child, orphaning its
  grandchildren; claustrum's negative-pgid kill tears down the whole tree. This
  is the Unix half of the #14 / #21 process-group teardown (with the #21
  exited-pgid guard); the `process.kill` divergence is already noted in
  [`PROTOCOL.md`](PROTOCOL.md). Same exit frame either way.

Wire-neutrality is enforced by the byte-identical battery; the differential
itself self-calibrates to **zero** contractual divergences on a claustrum-vs-
claustrum self-diff. Found during the post-v1.1.0 parity-audit sweep (the tooling
lives in `scratch/`, gitignored). The third finding from the same sweep —
`files.list` stat-per-entry vs the reference's `getdents` `d_type` — was probed
to be **byte-identical** even on symlink/dangling/self entries, so it needs no
divergence note.

## Deliberate divergences (post-parity, opt-in)

Unlike everything above, these **knowingly change a frame/behavior** from the
reference. They follow the "match upstream first, then improve" plan: only
consider them now that the harness proves parity, and document each as an
*intentional* divergence in [`PROTOCOL.md`](PROTOCOL.md) + the PR if adopted.

### D1 · Re-harden `-cli-zst` checksum ✅ (Option A) — impact M / cost L

- The reference verifies `-cli-checksum` only on the `-cli-url` download path,
  **not** on the local `-cli-zst` (SFTP) blob; PR #29 dropped our verification
  there to stay 1:1.
- **Shipped** as an opt-in divergence: `-cli-zst` is now SHA-256-verified
  **when (and only when) a `-cli-checksum` is supplied** — a mismatch is
  rejected with the same `checksum mismatch: …` error (source blob left
  intact).
- An absent/empty checksum stays trusting, so a caller that passes no checksum
  is byte-identical to the reference.
- The observable delta (documented in [PROTOCOL.md](PROTOCOL.md) + PR), for a
  *supplied wrong* checksum only: a valid blob the reference would install now
  returns `checksum mismatch` (was success), and a corrupt blob returns
  `checksum mismatch` instead of `decompressing: …`.
- Verified by a live ref-vs-claustrum differential.

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach ✅ — impact M / cost L

- `process.spawn` / `process.reattach` accept an optional `"wantPid":true` param.
  When set, the reply gains `pid` (the child's OS pid) and `startTime`. The
  reference has no such param, so this is the first wire-surface *extension*
  (vs D1, which changes an install-path behavior).
- `startTime` is an **opaque daemon token** (CL-8): the daemon's epoch-seconds
  wall clock captured at spawn, returned identically on spawn and reattach for
  the same id. A client persists it and compares a daemon value against a later
  daemon value for the same id to detect PID reuse / orphans — it is **not** an
  OS-comparable start time (don't equality-check it against psutil `create_time`).
- **Default path is byte-identical:** absent/false, both fields are omitted
  (`omitempty`) and the frame is exactly the old `{"success":true}` /
  `{found,running,firstSeq,lastSeq}` — battery **496/496** vs reference
  `d20a77da`.
- The fields live on a dedicated `spawnResult` struct, so they can never leak
  into the `successResult` shared by `process.stdin`/`process.kill`.
- Tolerant both directions: an older daemon ignores the unknown param; an older
  client never sees the extra fields — so a CT-1 client may send `wantPid`
  unconditionally (graceful degradation).
- Contract fixed by the sibling **clauster** client. Shipped in #105; documented
  in [PROTOCOL.md](PROTOCOL.md) (`process.spawn` + `process.reattach`).

### CT-2 · Opt-in `-keep-children` serve flag ✅ — impact M / cost L

- A `-serve` flag (off the wire — no method/frame/capability change). **Off by
  default**, graceful shutdown kills the whole child tree, unchanged. **Set**, it
  leaves spawned children running so they survive a daemon restart/upgrade,
  logging one honest line with the surviving count. The new daemon does not
  re-adopt the survivors; an out-of-band consumer reconciles them via the CT-1
  `pid`/`startTime`.
- **Caveat: survivors lose their stdio.** The pipes' daemon-side ends die with
  the daemon — the child sees EOF on stdin, and a later stdout/stderr write gets
  SIGPIPE (terminates by default) or EPIPE if SIGPIPE is ignored (Node's
  default). Documented in [PROTOCOL.md](PROTOCOL.md); only children that
  tolerate dead stdio genuinely survive.
- **POSIX-only.** On Windows children are confined to a Job Object
  (`KILL_ON_JOB_CLOSE`) that the OS terminates on daemon exit regardless, so the
  flag is **ignored with a startup warning** rather than silently killing while
  claiming to keep (`honorKeepChildren`). The hosted channel that uses it is
  POSIX-only anyway.
- **Supporting fix (default path):** shutdown teardown now runs synchronously on
  the main goroutine. It previously ran in a goroutine that raced the accept
  loop's return out of `run()`/`main` — `main` could exit the process first,
  skipping child teardown entirely. So this also makes the *default* "kill on
  shutdown" reliable (it was racy before). No wire effect — battery stays 496/496.
- Documented in [PROTOCOL.md](PROTOCOL.md) (`-serve` flags); verified end-to-end
  on POSIX (child survives with the flag, killed without) plus per-OS unit tests.

### CT-3 · Opt-in `claustrum.conf` config file ✅ — impact M / cost L

- **A single place to turn deviations on, absent ⇒ stock.** An optional
  `key = value` file (`claustrum.conf`) read from the directory holding the
  binary. If it's missing, unreadable, non-regular, or malformed, claustrum
  behaves as a stock replica — every key gates an already-opt-in divergence.
  Zero new dependency (not YAML): stdlib `bufio` + `strings.Cut`, `#` comments,
  **unknown keys and invalid values ignored** (forward-compatible, fail-safe).
- **Keys mirror the flags; precedence is explicit CLI flag > config > default**
  (resolved via `flag.Visit`, so `-keep-children`/`-metrics-addr` on the command
  line still win). Current keys:
  - `version-override = <commit-sha>` — the **drop-in stamp** (see below).
  - `keep-children = true|false` — default for CT-2.
  - `metrics-addr = host:port` — default for the `/metrics` listener.
- **`version-override` — make claustrum a permanent drop-in.** The desktop client
  decides whether to re-upload the daemon by running `<bin> --version` on the
  cached `~/.claude/remote/srv/<pinned-sha>/server` and matching
  `/claude-ssh\s+(\S+)/` against the SHA it pins; it skips the upload only when
  that token equals the pin. Stock claustrum prints `claustrum <ver> …`, so the
  client re-SFTPs the reference over it **every session**. With
  `version-override` set to that **bare commit SHA** (the reference versions
  itself by **git SHA-1, 40 hex** — 64-hex also accepted; anything else is a
  no-op), claustrum prints `claude-ssh <sha> (via Claustrum <ver>, built <t>)` —
  the client captures only the first token, hits the cache, and stops
  overwriting. Source the SHA from `scripts/UPSTREAM_SHA` and drop the binary at
  `~/.claude/remote/srv/<sha>/server` alongside the config.
- **Off the wire, off by default.** `-version` is CLI stdout, not a JSON-RPC
  frame; auth, the socket, and every method/frame are untouched, and
  `server.version` / `server.capabilities` still report claustrum's own version
  (masked in the battery). No file → byte-identical stock output.
- **Fail-safe & hardened** (any doubt → stock, startup never fails), all stdlib /
  cross-platform: regular-file-only via `Lstat`/`IsRegular` (rejects
  symlink/FIFO/device/directory → can't block startup), `io.LimitReader` ≤ 64 KiB,
  per-key validation (`version-override` gated to `^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`
  and lower-cased; `metrics-addr` printable-ASCII only), values used as data
  never as a format string.
- Verified: unit tests (each key's valid/invalid forms, unknown-key and malformed
  lines, case-insensitive keys, CLI-over-config precedence, non-regular-directory,
  and a `//go:build unix` FIFO case) + an end-to-end smoke test (stock / valid
  git-SHA impersonation with the client regex confirming a pin match / pre-prefixed
  & invalid → stock / uppercase normalized). Documented in
  [PROTOCOL.md](PROTOCOL.md) (`-version`).

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding **required** new params to existing methods. *(An **optional**,
  gracefully-ignored param whose result fields vanish by default — the D1 /
  CT-1 pattern — is the sanctioned exception: it leaves the default frame
  byte-identical and degrades both ways.)*

Any of these would need a deliberate, documented protocol version bump.
