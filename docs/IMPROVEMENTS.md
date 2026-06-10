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

`process.stdin` used to write synchronously, so a slow/non-reading child blocked
the dispatch goroutine once the 64 KB pipe filled. **Parity gap** — a probe
showed the reference returns `{success:true}` immediately (async/queued) where
claustrum blocked. Now each proc has a `stdinWriter` goroutine draining a
bounded (`stdinQueueCap`, 8 MiB) FIFO queue; `process.stdin` enqueues and
returns immediately, and a full queue applies backpressure + logs the
reference's `stdin backpressure: queue full` guard. Re-probe: claustrum now
matches the reference (success in ~350 ms vs previously blocked); `-serve`
battery byte-identical. The exact queue threshold is a stderr-log edge, not a
wire frame.

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

go.mod now carries an explicit `toolchain go1.24.4` directive alongside
`go 1.24`; with CI/release on `go-version-file: go.mod`, setup-go provisions
that exact toolchain, so release builds are reproducible against a known patch.
Renovate can bump the patch over time.

### 13 · Structured/leveled logging ✅ — impact M / cost L-M

Shipped a tiny leveled logger (`logging.go`): the daemon's diagnostic
`log.Printf("[Component] …")` calls now go through
`logDebugf`/`logInfof`/`logWarnf`/`logErrorf`, which prepend a level tag
*before* the existing `[Server]`/`[process.Manager]`/`[frameSink]`/`[shellenv]`
prefixes — left **byte-intact** so greps keep working. Threshold from
`CLAUSTRUM_LOG_LEVEL` (`debug`|`info`|`warn`|`error`), **defaulting to `debug`**
so output is unchanged unless an operator raises it. Still routes through the
stdlib default logger (timestamps + `log.SetOutput` test capture intact). The
CLI's fatal `claustrum: …` startup errors are left as-is — they're user-facing
exit messages, not diagnostic logs. Stderr-only; the wire surface is untouched
(goldens unchanged).

## Tier 3 — larger / lower-priority

### 14 · Windows process-tree kill via Job Objects ✅ — impact M / cost M-H

Spawned children are now confined to a Windows Job Object (`confineProcess` in
`sysproc_windows.go`); `process.kill`/`killAll` call `TerminateJobObject`,
tearing down the whole tree instead of just the parent (the old best-effort
`TerminateProcess` leaked grandchildren). The job carries
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, so the handle is closed on child exit
(reaping stragglers) and the tree also dies if the daemon itself exits. Unix is
unchanged (process-group kill). A new cross-platform `procGroup` abstraction
unifies both; Job Object failure falls back to the old parent-only kill, so
spawns never fail. Added dependency `golang.org/x/sys` (pinned v0.33.0,
**Windows-only** — not compiled into other targets; discussed/approved). No
wire change — stderr/OS behavior only; socket goldens unchanged.

### 15 · Docs site (mkdocs) ✅ — impact M / cost M

`docs/` is now published via **mkdocs-material** to GitHub Pages (`mkdocs.yml` +
`docs/index.md` landing page). A `docs` workflow runs `mkdocs build --strict` on
every PR (catches broken links/nav) and deploys to Pages on push to `main`
(SHA-pinned `upload-pages-artifact`/`deploy-pages`, least-privilege
`pages: write`/`id-token: write` scoped to the deploy job). Root files
(Contributing/Security/Changelog) are linked out to GitHub from the nav to avoid
duplicating the canonical copies. Site: <https://schubydoo.github.io/claustrum/>.

### 16 · `/metrics` counters ✅ — impact L-M / cost M

Shipped opt-in Prometheus metrics (`metrics.go`): a process-wide atomic counter
registry (connections, process spawns/exits, reattaches, stream/stdin bytes)
exposed at `/metrics` via a stdlib `net/http` listener — **only** when
`-metrics-addr` is set (off by default, no listener otherwise). Counts only (no
command output/tokens), no auth → bind to loopback (documented in
[PROTOCOL.md](PROTOCOL.md) +
[SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
Counting is always-on/atomic; the endpoint is the opt-in part. Stopped on
teardown. Pure stdlib, no wire change, goldens unchanged.

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

Shipped `-token-fd <n>` (e.g. `0` for stdin): the `-serve` daemon reads the auth
token from an open descriptor instead of `-token-file`, so it never touches
disk. Since `-serve` self-daemonizes, the parent reads the fd and **forwards the
token to the detached child over an inherited pipe** (`readTokenFD` +
`daemonizeWithToken`; child reads the fd named by `CLAUSTRUM_TOKEN_PIPE`) —
never via disk, argv, or environ. Additive/off-wire: `-token-file` callers and
the reference are unaffected. `readTokenFD` unit-tested; full fd→pipe→auth path
validated live (`server.ping` authenticates with the forwarded token; wrong
token rejected). Documented in [PROTOCOL.md](PROTOCOL.md) +
[SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

### 19 · Docs-site visibility/formatting pass ✅ — impact L / cost L-M

Restructured the prose-heavy tables that read as thin, clipped slivers on the
published site: this backlog is now per-item sections (you're reading the
result), and the protocol reference's `files.*`/`git.*`/`process.*` method
tables became per-method sections with result lines + bullet notes — every
method now gets its own table-of-contents entry on the site. Earlier site fixes
in the same vein: `pymdownx.emoji` for Material icons (#75), `pymdownx.tilde` +
a 72rem content column (#77). Site-only; no wire/behavior change.

## Deliberate divergences (post-parity, opt-in)

Unlike everything above, these **knowingly change a frame/behavior** from the
reference. They follow the "match upstream first, then improve" plan: only
consider them now that the harness proves parity, and document each as an
*intentional* divergence in [`PROTOCOL.md`](PROTOCOL.md) + the PR if adopted.

### D1 · Re-harden `-cli-zst` checksum ✅ (Option A) — impact M / cost L

The reference verifies `-cli-checksum` only on the `-cli-url` download path,
**not** on the local `-cli-zst` (SFTP) blob; PR #29 dropped our verification
there to stay 1:1. **Shipped** as an opt-in divergence: `-cli-zst` is now
SHA-256-verified **when (and only when) a `-cli-checksum` is supplied** — a
mismatch is rejected with the same `checksum mismatch: …` error (source blob
left intact); an absent/empty checksum stays trusting, so a caller that passes
no checksum is byte-identical to the reference. **Divergence** (documented in
[PROTOCOL.md](PROTOCOL.md) + PR): for a *supplied* wrong checksum, a valid blob
the reference would install now returns `checksum mismatch` (was success) and a
corrupt blob returns `checksum mismatch` instead of `decompressing: …`.
Verified by a live ref-vs-claustrum differential.

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods.

Any of these would need a deliberate, documented protocol version bump.
