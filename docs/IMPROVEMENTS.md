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

(The `ldd` half is **linux-only**: `libc_other.go` returns `""` without probing
off linux, so no such bound exists on darwin or windows.)

`git.*` and the `-install` libc probe shelled out with no deadline; a wedged
git/ldd left a request goroutine waiting with no bound. Both are now wrapped in
`exec.CommandContext`: the `ldd --version` probe (`lddProbeTimeout`, 5 s,
security fix S4) and every `git` invocation (`gitTimeout` 60 s).

⚠️ **For the `ldd` half, the honest-path cost is far narrower than D11's — but the
entry used to state that absolutely, which a wall-clock deadline cannot support.**
(The `gitTimeout` half is a different matter and is owned by **D5**, which records
a claustrum-only `-32603` carrying `signal: killed` when the deadline fires — a
real on-wire cost, not a narrow one.) `lddProbeTimeout` is a wall-clock deadline and so
cannot separate a hostile `ldd` from a slow one — but unlike D11, a timeout
usually changes **nothing observable**, because the fallback value and the true
value coincide (the comparison is about the two bounds' *shape*; D11's is now off
by default, this one is not):

- On a **musl** host the probe is never spawned at all: `detectLibcWith` returns
  `"musl"` on the loader glob before reaching `ldd`, so it cannot time out.
- On a **glibc** host the timeout fallback is `"glibc"` (`classifyLibc`: no musl
  loader, `lddErr != nil` → `"glibc"`) — the same answer the probe would have
  given.

So the `libc` field moves only on a host where `ldd` *reports* musl while
`/lib/ld-musl-*.so.*` does **not** match — close to unreachable in practice.

🔴 **But that is only the honest-but-slow direction, and the bound is NOT narrow
overall.** Against the *stalled* `ldd` this cap exists for, the divergence is
total: `detectLibc()` is called unconditionally in the facts literal
(`install.go`, before any branch), so claustrum returns `"glibc"` at 5 s and then
goes on to install the CLI and print a **complete `__INSTALL_RESULT__` line**,
where the reference — assumed unbounded here, not probed — emits **nothing at
all**. A facts
line, a `cliWasPresent`, an installed binary versus silence. That is the same
shape D11 and D12 lead with, on this bound's own motivating path — and now that
D11's deadline defaults off, D12's download timeout is the only `-install`
wall-clock bound applied unconditionally everywhere. This one is *not*
unconditional twice over: it is linux-only, and even on linux `detectLibcWith`
returns on a matching `/lib/ld-musl-*.so.*` glob before `ldd` is spawned, so it
cannot fire there. The predicate is the **glob**, not the host — a musl box whose
loader that glob misses does reach the bound, which is exactly the narrow case this
entry's own `libc`-delta argument turns on.

⚠️ And the cap addresses the *stall* half only. A **hostile** `ldd` resolved
earlier in `PATH` that answers in 1 s is untouched by the deadline, and
`classifyLibc` then trusts its `musl` banner verbatim — so "security fix S4"
overstates what a timeout can buy.

Neither direction is measured on either binary.

The fixture that would settle it must use a **musl** banner, not a glibc one: a
stand-in `ldd` on `PATH` that sleeps 6 s, prints `musl libc (x86_64)` **and exits
0**, with the musl loader glob masked. The exit code is load-bearing:
`classifyLibc` takes the musl banner only when `lddErr == nil`, and a real musl
`ldd --version` prints to stderr and exits 1 — a faithful stand-in would fail the
control for a reason unrelated to the timeout. (It also narrows the divergence set
further — glob misses ∧ exits 0 ∧ output says musl ∧ slower than 5 s — so
"close to unreachable" is if anything understated.) Expected: reference `libc:"musl"`, claustrum
`libc:"glibc"`. **Control that must fire:** the same stand-in answering instantly
must give `"musl"` on *both*, proving the fixture can produce the non-default
value at all — a control that merely "gives the same value on both" is satisfied
by a broken fixture. A glibc-banner slow arm is worth a second row, and must show
`"glibc"` on both.

⚠️ **A timeout is NOT interchangeable with an ordinary git failure**, though this
entry used to say so. That held only while "failure" meant *nothing happened*.
`git.worktree_remove` now treats a failed git as permission to delete the
worktree directory itself, so a caller with a side effect must distinguish our
deadline from git's verdict — `gitDeadline` returns that third bit — or our own
safety cap authorises a destructive act the reference cannot perform.

CORRECTION, 2026-08-02: this used to end "read-only callers are unaffected and
still just check `ok`". They are not unaffected. `git.status` and (since the
stdout-only fix) `git.list_branches` REPORT the failure as `-32603` carrying the
Go error string, so when our deadline kills git they put a claustrum-only frame
on the wire: measured, `err.Error()` is **`signal: killed`**, not `context
deadline exceeded` — `Cmd.Wait` prefers the SIGKILLed process's `ExitError` over
the context error. The reference showed no deadline at or below the 75 s probed
and simply blocks, emitting nothing. Unreachable for it, so not a parity break, but it is ours and it is on
the wire.

⚠️ **The 60 s bound is softer than it reads.** `CombinedOutput` waits for the
output pipe to close, not just for git to exit, so a git that spawns a child which
outlives it stays blocked past the deadline — the orphan holds the pipe open.
Measured: a stub `sleep 30` under `sh` took the full 30 s against a 300 ms cap,
while the same stub written as `exec sleep 30` returned at once. Closing that gap
means reading the streams explicitly rather than using `CombinedOutput`, which is
more code and more divergence; recorded here rather than fixed, so the entry does
not promise a bound the code does not deliver.

The timeout reply shape is also not unchanged: `git.worktree_remove` answers
`{"success":false,"error":"git worktree remove timed out after 1m0s; no cleanup
was attempted, and git may have partially removed the worktree"}`, a frame the
reference never emits. ⚠️ This used to add that **no other frame moves because of
the deadline**. That is false, and it is the second time this sentence has had to
be walked back — the first version claimed "every reference-reachable frame stays
byte-identical", which was false for the whole window between the deadline work
and the stdout-only fix, and the "scoped" replacement is false too. The deadline
is the shared `gitTimeout`, applied independently at all three helpers (`git`,
`gitStdoutErr`, `gitDeadline`), so a kill can surface through **any** call site:
`gitStdoutErr` turns it into a claustrum-only `-32603 "signal: killed"` on
`git.status` and `git.list_branches`, and the repo-detection calls can answer
`isRepo:false` instead. More than one arm moves, and the full set has not been
enumerated against the code — so no count is asserted here. `methods_git.go`
carries the same retraction.

The wording deliberately claims only what the daemon can observe — the SIGKILLed
git unlinks as it goes, so the directory state is not knowable from here.

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

Shipped in #58: each per-process buffer is capped at **16 MiB** of **serialized
frame bytes**, each frame's JSON line including its trailing newline (claustrum's
own buffer was unbounded before #58 — a noisy long-lived process grew memory
without bound); the oldest frames drop and `firstSeq` advances past the cap.
`reattach` returns `firstSeq`, so clients handle the moved floor.

**The cap is parity, not tuning.** #58 chose 50 MiB before the reference's value
was known; the 2026-07 sweep measured the reference at **16 MiB**, and claustrum
was corrected to match. Because `firstSeq` is wire-visible, the bound is part of
the observable contract — it is **not** a free local tuning knob. Re-measure
before changing it.

⚠️ **CORRECTION (2026-08-02): the ACCOUNTING UNIT above was wrong, and this entry
stated it most strongly of all the records.** It read "16 MiB of base64 data" and
claimed the 2026-07 sweep had *measured* "identical accounting (base64 length,
whole frames dropped oldest-first)". The constant was right; the unit was not.
The reference counts the serialized frame **including its trailing newline**,
where claustrum counted `len(f.Data)` only — so on small-frame workloads
claustrum retained ~18.05 MiB of line bytes against the reference's 16 MiB, and
`reattach{fromSeq:0}`'s `firstSeq` diverged. The exit frame is the clearest case:
no `Data` at all, so it cost 0 here and ~60 B there.

Naming the accounting method as *measured* is why this survived: PR #174 compared
at ~8.7 KB frames, where the two hypotheses agree to within rounding, and
concluded the accounting already matched. **Reproduce with SMALL frames.**

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
Scorecard SBOM + Signed-Releases (10/10). Automated version PRs are handled by
**knope** (`knope.toml` + `knope-prepare.yml` / `knope-release.yml`, changesets-only;
migrated from `release-please.yml`) with `pr-auto-update.yml`, all as
claustrum-ci[bot].

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

### 23 · Recover from handler panics ✅ — impact M / cost S

The per-request goroutine now wraps dispatch in `defer recover()`. Before this, a
panic in any handler crashed the **whole** daemon — an unrecovered panic in any
goroutine takes the process down — orphaning managed children and leaving a stale
socket, so reconnects failed `connection refused` rather than `no such file`.

Surviving the request is strictly better than dying on it, and that is the whole
justification: a daemon that supervises child processes should not lose them to a
bug in one handler.

**The reply frame is claustrum's own, and it is NOT a parity claim.** The reply
code is **−32603**, which is `codeInternal` — the JSON-RPC 2.0 standard *Internal
error* code, not something specific to this protocol. The message prefix
(**`recovered panic: `**), the log line
(`[Server] recovered panic: method=%s id=%v: %v`) and the id rendering
(`idForLog`, matching claustrum's other log lines) are all claustrum's own
conventions.

**Nothing about this frame is probe-verified, and nothing about it needs to be:**
the path is unreachable, so no client can observe the frame and it cannot diverge
from anything. No input is known to reach a handler panic — two fuzz waves (the
3481-case run plus a gap-closing run over malformed frames, `auth`/`jsonrpc`, and
multi-param combos) found zero, and claustrum's own panic sites are each either
an unreachable stdlib guard (`time.Timer.Stop/Reset` on an uninitialised timer,
which `time.NewTimer` precludes) or an already-bounds-guarded slice
(`WriteStdin`'s offset/dedup, `frameSink`'s eviction). The recover is therefore
blanket-defensive by design.

The recover is scoped to **dispatch only** — the response write stays outside it,
so a panic in `writeResponse` cannot produce a second frame for one id — and
`server.shutdown`, which must reply nothing, still replies nothing even if its
handler panics.

- **Default path byte-identical:** the recover fires only on a panic, which does
  not occur on any reachable input, so the differential battery is unchanged
  (612/612).
- Provoked in test through the `dispatchRequest` seam (`server.go`); reverting the
  recover makes the test crash the binary (an unrecovered goroutine panic), which
  is the mutant signal.

## Deliberate divergences (post-parity)

Unlike everything above, these **knowingly change a frame/behavior** from the
reference. They follow the "match upstream first, then improve" plan: only
consider them now that the harness proves parity, and document each as an
*intentional* divergence in [`PROTOCOL.md`](PROTOCOL.md) + the PR if adopted.

**Most are opt-in; some are always-on, and the entry says which.** An always-on
divergence needs a reason the opt-in shape does not work — usually that the thing
it prevents is unrecoverable (D2) or that the reference's own behavior on that
path is an **unbounded wait** rather than a frame (D4, D5) — **or that it is an
accepted artifact no honest caller can reach, which is D9's case rather than a
decision a flag would improve.** *Unbounded wait*, not "hang" — the reference
recovers the moment the wait's cause clears, and saying otherwise is a correction
D4 already had to make once.

D4–D9 were shipped without numbers and are catalogued here retrospectively. Each
carries the evidence that was already in [`PROTOCOL.md`](PROTOCOL.md) rather than
a new claim — **with one exception: D8's reference behaviour was unmeasured when
this catalogue was written and was measured on 2026-08-06, so that entry does
carry a new claim, and says so.**

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

### D2 · Refuse a home directory as a destructive path target ✅ — impact H / cost L

**Status: both methods shipped.** `files.extract_tar` landed first; this entry
completes it with `git.worktree_remove`, which shares the predicate
(`homeguard.go`).

- **Not opt-in, and deliberately so.** Every other entry in this section is off by
  default; this one is always on, because the thing it prevents is unrecoverable
  and a flag to re-arm it would be a footgun with a switch.
- Two methods hand a caller-supplied path to `os.RemoveAll`: `files.extract_tar`
  wipes `destDir` before unpacking, and `git.worktree_remove` deletes
  `worktreePath` when git exits non-zero. **Both are `~`-expanded first**
  (`bindParams` → `expandPaths`, on every request), so `"~"` reaches
  `os.RemoveAll($HOME)` on either.
- ⚠️ **`git.worktree_remove` is the more exposed of the two.** It has no `IsAbs`
  and no `isFilesystemRoot` gate at all, so `wipesHomeDir` is the *only* thing
  between `worktreePath` and the delete — where `extract_tar` keeps two checks
  behind it. That also means the guard incidentally closes a second hole there:
  `"worktreePath":"/"` previously reached `os.RemoveAll("/")` unguarded, and a
  root contains the home directory.
- **The predicate resolves relative paths** (`filepath.Abs`) rather than only
  cleaning them. Without that, every relative input compares unequal to an
  absolute home and the guard answers "safe" whatever the path resolves to.
  Measured on an unguarded tree: with the daemon's working directory inside a
  home directory, `"worktreePath":".."` deletes it. (`os.RemoveAll` special-cases
  a trailing `"."` and returns `EINVAL`; `".."` has no such guard.) Raised in
  review on #231.
- **This fired.** On 2026-08-02 an in-repo fuzzer sent `"destDir":"~"` at a live
  daemon and destroyed the maintainer's home directory. `"~"` is the first value
  in its adversarial list that survives the gate — everything before it is
  rejected as non-absolute (`..`, `C:\`, `\`) or as a root (`/`, `//`, `///`).
- The pre-existing gate, `IsAbs(p) && !isFilesystemRoot(p)`, could not catch it
  and was not wrong: it was written to close a **Windows volume-root** hole
  (#224). "Absolute and not a filesystem root" is exactly what a home directory
  is. The lesson is the shape of the question — not *"is this path special?"* but
  *"does deleting it delete something the caller cannot have meant?"*
- **Containment is the test** (`homeguard.go`): refuse when the home directory is
  at or under the target, i.e. the home directory itself plus every ancestor
  (`/home`, `/Users`, a drive root). **Descendants stay allowed** — extracting
  into `~/.claude/…` is the daemon's own install path, so a guard over the whole
  home subtree would refuse the product's own use.
- **MEASURED: the reference destroys the home directory on BOTH methods.** Probed
  2026-08-06 on an ephemeral Linux VM, against `5db5e4a` (the pin) and
  `8de85fa` — identical results on both builds:

  | probe | reference reply | fixture home afterwards |
  |---|---|---|
  | `worktree_remove` `worktreePath:"~"` | `{"success":true}` | **deleted outright** |
  | `extract_tar` `destDir:"~"` | `{"success":true,"fileCount":1}` | **wiped**, then recreated empty + the archive member |
  | `extract_tar` `destDir:"~/sub"` (control) | `{"success":true,"fileCount":1}` | untouched — only `sub/` replaced |

  Two instrument checks ran first, because a "nothing happened" result would
  otherwise be indistinguishable from a probe that never reached the code:
  `files.validate` on `~/KEEP.txt` returned `valid:true` (the pinned `HOME` took
  and `~` resolved to the fixture), and `worktree_remove` on an ordinary
  non-worktree directory deleted it (the fallback arm is reachable).
- **So D2 is a knowing divergence from measured behaviour on both methods, and
  they weigh the same.** An earlier version of this entry called the
  `extract_tar` half "unmeasured" and therefore lighter. That was an absence
  claim with no probe behind it; the probe now exists and says otherwise.
  Matching the reference is the hard rule for *frames*; it was never a commitment
  to reproduce accidental data loss.
- ⚠️ **The probe is destructive and must never run on a real host.** It pins
  `HOME` to a `/tmp` fixture and carries a canary in the real home to catch an
  override that did not take. On a host where that override failed, it would
  delete the user's home — which is the incident, repeated. Ephemeral VM only.
- **Not a security boundary.** A caller holding the socket and token can already
  run arbitrary commands via `process.spawn` (SECURITY.md). This stops an
  accidental, generated or fat-fingered path — the shape the incident had — so
  lexical containment is the right depth. Symlinks are not resolved.
- Documented in [PROTOCOL.md](PROTOCOL.md) under both methods. Tests are in
  `homeguard_test.go`; the destructive call is seamed (`wipeDestDir`) or aimed
  at a `t.TempDir()` home, so the suite is safe to run against an **unfixed**
  tree — verified by reverting both guards and watching it fail.

### D3 · Make the `files.extract_tar` size cap opt-in ✅ (opt-in) — impact H / cost M

- `maxExtractBytes` shipped as a hardcoded **512 MiB, on by default**, with no
  flag and no config key. It arrived with PR 56 as a size-bomb hardening; the
  sibling guard from that same commit got a PROTOCOL.md entry and this one got none — so it was an **undocumented** divergence for its whole
  life.
- **The reference applies no cap at any size the probe could reach.** Measured at
  `5db5e4a` with a 629 MB payload (600 MiB of zeros, 610 KB compressed) — enough
  to disprove a 512 MiB cap, not enough to prove there is none above 629 MB:

  | binary | reply | bytes on disk |
  |---|---|---|
  | reference `5db5e4a` | `{"success":true,"fileCount":1}` | 629145600 |
  | claustrum, cap off (new default) | `{"success":true,"fileCount":1}` | 629145600 |
  | claustrum, cap opted in at 512 MiB | `{"success":false,"fileCount":0,"error":"extraction size limit exceeded"}` | 0 |

- **This was a live user-facing break, not a theoretical one.** A tree over the
  cap got an error the reference never produces, and **Claude Desktop owns the
  argv**, so there was no way through from the caller's side.
- **Shipped as: default `0` = unlimited = byte-identical to the reference.** The
  cap survives as an opt-in via `-max-extract-bytes <n>` **and** the
  `max-extract-bytes` key in `claustrum.conf` (precedence: explicit CLI flag >
  config > default, matching keep-children / listen-pipe / metrics-addr). The
  config key is the one that matters — see the argv point above.
- **Disabled bypasses `io.LimitReader` entirely** (`io.Copy(out, tr)`) rather
  than passing a huge bound. The `max-total+1` arithmetic is what defines the
  boundary behaviour; routing the unlimited case through it would invent a
  boundary where the reference has none.
- Two defects the measurement exposed, fixed with the flip: the cap arm returned
  the **partial `fileCount`** where the four arms that reject the archive outright
  (create, mkdir-parent, zip-slip, unsupported type) all answer `0`, and it left
  the truncated entry on disk. It now returns `0` and removes that entry. (Four
  *other* arms — mid-stream `tr.Next`, `TypeDir` mkdir, `io.Copy`, `write .synced`
  — do still return the partial count; the cap arm was grouped with the wrong set,
  not with all of them.)
- The differential battery stays **byte-identical**, and the 629 MB probe above is
  the direct parity evidence. (Deliberately no frame count: a run's size changes as
  the battery grows, so a number quoted here goes stale the way `496/496` did.
  Recount at the time of writing if you need one.)

### D4 · `files.read` refuses a non-regular file ✅ (always-on) — impact M / cost L

- **Shipped in PR 56, unnumbered until now.** `files.read` rejects anything that
  is not a regular file with `-32602 files.read: not a regular file`
  (`methods_files.go`, `Mode().IsRegular()`); **the reference does not refuse
  it.** Two shapes were measured, and they behave differently from each other: a
  FIFO (the reference *blocks* rather than reading) and `/dev/null` (it reads,
  answering `{"content":"","exists":true}`). Sockets and block devices were not
  measured — "does not refuse" is the claim, not "reads".
- **Always-on, because the reference's behavior on the motivating path is not a
  frame.** A read of a FIFO with no writer blocks in `open`, so the reference
  emits nothing for as long as that holds — a frame-diffing comparison cannot even
  see the request. claustrum turns that into an immediate, actionable error.
- **It is not a permanent hang on the reference, and PROTOCOL.md used to say it
  was.** Measured: the reference replies the instant a writer opens and stays
  responsive throughout. The correction stands with the divergence; the guard's
  justification is "unbounded wait", not "deadlock".
- **`/dev/null` is the cost, not a second decision.** The check is
  `Mode().IsRegular()`, so it also rejects character devices the reference reads
  happily (`{"content":"","exists":true}`). Narrowing it to permit *some* would
  reopen the hazard on `/dev/zero` and `/dev/random`, which are unbounded reads
  the reference has no protection against either.
- Documented in [PROTOCOL.md](PROTOCOL.md) → `files.read` → *Non-regular files*.

### D5 · 60 s `gitTimeout` on every git invocation ✅ (always-on) — impact M / cost L

- claustrum caps every git invocation at 60 s (`methods_git.go` — the shared
  `gitTimeout`, applied independently at all three helpers: `git`, `gitStdoutErr`,
  `gitDeadline`, so every call site through them is covered). **The reference showed no deadline at or below 75 s** on
  `git.worktree_remove`: measured, no reply at 75 s where claustrum answered at
  60.1 s, with a fast-git control proving the fixture could answer at all. (This
  was pointer-class until that run.) That is the measured scope — one method, one
  ceiling; "the reference runs git with no deadline" is the natural reading of it,
  not a second measurement.
- **Always-on for the same reason as D4** — the behavior being replaced is an
  unbounded wait, which no opt-in default can improve on for a caller who does not
  know the flag exists.
- **A timeout must not be read as "git refused".** On `git.worktree_remove` it
  answers `{"success":false,"error":"git worktree remove timed out after 1m0s;
  no cleanup was attempted, and git may have partially removed the worktree"}`
  and the daemon removes nothing. The wording claims only what the daemon can
  observe: the git it SIGKILLed unlinks as it goes, so the on-disk state is not
  knowable from here. Before that was separated out, a wedged git produced a
  deletion **plus** `{"success":true}` — an outcome the reference cannot reach.
- ⚠️ **The cap is softer than it reads**: it waits on git's output pipe, so a git
  that spawns a surviving child stays blocked past the deadline (§5 above).
- **A further arm reaches the wire and this entry used to omit it:** on
  `git.status` and `git.list_branches` the same cap surfaces as `-32603` with
  `err.Error()` = **`signal: killed`** (§5 above). It is not the only one — a
  killed repo-detection call answers `isRepo:false` where the reference emits
  nothing — and §5 records why no total is asserted. Unreachable on the reference,
  so not a parity break, but it is ours and it is on the wire.
- Documented in [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove`, and →
  `git.list_branches` for the `signal: killed` arm — that bullet is the only
  PROTOCOL record of the `signal: killed` *frame*, and `git.status`
  carries the identical frame for the identical reason.

### D6 · `-cli-version` must name a single path component ✅ (always-on) — impact H / cost L

- The install's clearing step is an `os.RemoveAll` on
  `filepath.Join(cliDir, cliVersion)`, so a version that reaches outside the
  cli-dir deletes unrelated data recursively. Two escapes, both measured against
  `5db5e4a`, and **the reference destroys the target on both**: `../victim`
  (`Join` cleans, so the path lands beside the cli-dir) and `link/1.0.0` (an
  intermediate symlink under the cli-dir, followed at open time).
- claustrum answers `cli version "…" must be a single path component` in
  `cliError` and touches nothing. `.` and `..` are refused for the same reason,
  and both `/` and `\` are rejected on every OS so the accepted set does not
  change with the platform.
- **A single component rather than a containment check**, because a *lexical*
  containment check accepts `link/1.0.0` — it is lexically inside — and
  `EvalSymlinks` would only add a TOCTOU window before the `RemoveAll`. Nesting
  costs nothing to give up: the reference does not support a nested version
  either, failing `sub/2.0.0` at temp-file creation because it never creates the
  parent. **A final component that is itself a symlink stays legal and safe** —
  `os.RemoveAll` unlinks a symlink rather than following it — so the rule is
  narrower than "no symlinks".
- **Every honest path is byte-identical** (the `-install` CLI transcript, not a
  JSON-RPC frame) — the real client passes a bare version
  string (`1.0.86`, `2.0.0-beta.1`, a commit sha, `latest`, `1.0.86+build.5`, all
  measured as accepted). Same shape as D8 and D1.
- Documented in [PROTOCOL.md](PROTOCOL.md) → `-install`.

### D7 · `-cli-version` must not collide with the install temp sweep ✅ (always-on) — impact M / cost L

- The orphan sweep claims `.fetch-*` and `*.zst` and runs after *every* attempted
  install, so `-cli-version .fetch-x` or `1.0.zst` installs correctly and is
  deleted moments later in the same run. Measured at `5db5e4a`: reference **and**
  claustrum both finished with an empty cli-dir and **no `cliError`** — reporting
  success while having installed nothing.
- claustrum answers `cli version "…" collides with the install temp sweep`
  instead. **Unlike D6 this gives up exact parity**, on the grounds that an error
  beats a success that installed nothing.
- The sweep predicate and this check **share one definition**, so they cannot
  drift apart.
- Documented in [PROTOCOL.md](PROTOCOL.md) → `-install`.

### D8 · `remote-server.log` is declined rather than shared ✅ (always-on) — impact M / cost L

- ⚠️ **Only the DECLINE is the divergence.** Recreating `remote-server.log` fresh
  on every start (unlink + create, not truncate in place) is **measured parity** —
  `server.go` records the probe: a planted `666 root` log came back `600 claude`
  on the reference (the *owner* changed, so it recreated the file) and `666 root`
  on claustrum, which truncated and wrote into it. claustrum was changed to match.
  The owner is the right observable precisely because `chmod` cannot forge it.
- The divergence is the **fallback**: when the existing log cannot be replaced —
  a sticky directory holding another user's file — claustrum declines the log
  entirely and the daemon's output falls back to the launcher's inherited stdio.
- ✅ **MEASURED 2026-08-06 — the reference writes into the foreign file.** This
  entry used to say the reference had never been measured here; it has been now,
  on an ephemeral VM. Socket dir `1777` (sticky, so a non-owner cannot unlink),
  holding a **root-owned, mode 0666** `remote-server.log` — world-writable on
  purpose, because at `0644` the daemon could not write for a trivial permission
  reason and the run would prove nothing about intent. Daemon started as an
  unprivileged user:

  | | log after start | canary | launcher stdio |
  |---|---|---|---|
  | reference `5db5e4a` | `root` `0666`, **162 B of its own output** | **destroyed** | quiet |
  | claustrum | `root` `0666`, 24 B (untouched) | **intact** | **carries the banner** |
  | *control:* the same fixture in a NON-sticky dir | both → `schuby` `0600`, recreated | gone on both | quiet |

  The control is what makes the sticky row readable: it shows both binaries doing
  the known unlink-and-recreate when replacement is *possible*, so the sticky
  difference is about the refused replacement and not about a daemon that failed
  to start.
- **So the divergence is real**: the reference truncates and writes its
  diagnostics into a file owned by another user; claustrum declines and falls back
  to inherited stdio.
- ⚠️ **It is hardening, NOT a defect claim, and the entry should not read as one.**
  Reaching it needs a local user who can already plant a file in that directory —
  the same class of precondition claustrum's own
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)
  puts out of scope ("reports that … amount to 'the operator can run commands on
  their own host'"). The justification for D8 does not depend on the reference
  being wrong: **a daemon should not write into a file it does not own.** That is
  a design position, and it is the whole of it.
- The log-handling behavior is otherwise identical — though the *contents* never
  were, since the banner word and the level tag are an intentional rebrand.
- **Why always-on rather than opt-in**, against the rule that opt-in is the
  default: the trigger is **unreachable on the deployed path**. The socket
  directory is `~/.claude/remote/`, per-user and not world-writable, so a foreign
  log cannot be planted there and the fallback never fires — Desktop sees
  identical behaviour either way. It fires only in a shared directory, which is
  also the only place the reference's behaviour is a disclosure risk. A flag would
  gate a branch no honest deployment reaches.
- **Reopen trigger** (the old one — "any measurement of that log path" — is spent):
  a deployment that puts the socket directory somewhere shared *and* needs the log
  file, since there the fallback sends diagnostics to the launcher's stdio instead.
  If that stream is one a client parses, D8's fallback becomes visible to it.
- Documented in [PROTOCOL.md](PROTOCOL.md) → *Daemon log*.

### D9 · Namespace-wide params binding is stricter than the reference's ✅ (always-on) — impact L / cost L

- **The only divergence in the repo that had a single record.** It was documented
  in [PROTOCOL.md](PROTOCOL.md) → *Params* and nowhere else — no number, no
  backlog entry, no catalogue line — so nothing pointed at it from either index.
- claustrum binds `params` into **one struct per namespace** (`pathParams`,
  `gitParams`), so a field that is valid for the *namespace* but unused by *this
  method* still participates in decoding: a type-mismatched value there answers
  `-32602` (e.g. `files.stat {"maxBytes":"{"}`, `git.status
  {"baseRepo":[1,2]}`). The reference binds only the field the specific method
  reads, ignores the rest regardless of type, and runs with defaults.
- A genuinely unknown key — in neither struct — is ignored by both.
- **Found by differential fuzzing, and accepted rather than fixed**: it surfaces
  only under adversarial params, and a real client never sends them. Wire-visible
  and always-on, which is why it earns a number even though no honest caller can
  reach it.
- Documented in [PROTOCOL.md](PROTOCOL.md) → *Params are bound per namespace*.

### D10 · Make the `-install` CLI size cap opt-in ✅ (opt-in) — impact H / cost M

- **The sibling of the `files.extract_tar` cap, from the same wave.** `maxCLIBytes`
  shipped as a hardcoded **512 MiB, on by default**, with no flag and no config
  key, in PRs 57 and 59 — the same hardening round as the extract cap. It
  governs **two** reads: the decompressed size written by `zstdDecompress` and the
  HTTP response body in `httpGet`. It had no PROTOCOL.md entry either.
- **Measured at `5db5e4a`** on the `-cli-zst` path with a 600 MiB payload
  (21 KB compressed):

  | binary | `cliError` |
  |---|---|
  | reference `5db5e4a` | `installed cli at <path> is not runnable` |
  | claustrum, capped (old default) | `decompressing: decompressed CLI exceeds 536870912 bytes` |
  | claustrum, cap off (new default) | `installed cli at <path> is not runnable` |

  The reference decompressed the whole payload and got as far as the runnability
  check — a 600 MiB file of zeros is not executable — so the cap is the only thing
  that differed. That disproves a cap at or below 600 MiB; it does not prove there
  is none above it.
- **The `-cli-url` half was measured separately**, because `maxCLIBytes` gates the
  download body too and the `-cli-zst` run does not exercise that path. A 629 MB
  **incompressible** body (so the body itself exceeds the cap) over a localhost
  server: the reference downloads it all and reaches the same runnability check,
  and claustrum with the cap off matches. The **control** — the same fixture with
  `-max-cli-bytes 536870912` — answers `download failed: response exceeds
  536870912 bytes`, which is what proves the probe reaches `httpGet`'s limit at
  all rather than passing for an unrelated reason.
- **`maxCLIBytes` shipped as a hardcoded 512 MiB in PRs 57 and 59** — the same
  hardening round as the `files.extract_tar` cap, which gets the same flip
  in its own PR. Neither had a flag, a config key, or a PROTOCOL.md entry.
- **Shipped as: default `0` = unlimited = byte-identical to the reference**, with
  the cap opt-in via `-max-cli-bytes <n>` **and** the `max-cli-bytes` key in
  `claustrum.conf` (explicit flag > config > default). The config key is the one
  that matters: Desktop owns the argv on `-install` too, so a flag alone would be
  unreachable for the people who need it.
- **Both call sites bypass their `LimitReader` entirely when disabled** rather
  than passing a huge bound — the `cap+1` arithmetic is what defines the boundary,
  and routing the unlimited case through it would invent one.
- The error strings are unchanged when the cap is opted in, so a host that wants
  the bound keeps exactly the behaviour it had.
- 🔧 **The blob is now STREAMED, never buffered — this ships with the flip and is
  not separable from it.** Turning the cap off removes the only bound on what used
  to be `io.ReadAll` (download) and `os.ReadFile` (local blob), which would leave
  `-install` unbounded in memory — measured on claustrum, and reason enough on its
  own; the reference's own memory behaviour was never measured and no claim about
  it is needed here. So the blob is a
  **path** throughout: the download streams to `<cli-dir>/.blob-<random>` — or to
  `$TMPDIR/claustrum-fetch-<random>` on a first install, since `fetchToFile` runs
  before the cli-dir is created — and is
  hashed as it arrives, the local blob is hashed in one bounded pass, and
  `zstdDecompress` opens the path. A path rather than a reader because `ensureCLI`
  retries `stageAndInstall` once and needs a source readable **twice**.
- ⚠️ **`.blob-`, not `.fetch-`, and that is load-bearing.** Giving the blob a
  lifetime on disk put it in reach of `sweepFetchTemps`, which runs after every
  attempted install and claims `.fetch-*` and `*.zst`. A `.fetch-*` blob would be
  deleted by a concurrent install's sweep in the same pass as the staging file,
  so the retry that the sweep is *supposed* to trigger would re-enter with no
  source — defeating `errStagingVanished` in exactly the case it exists for. The
  invariant is that **the blob must be invisible to BOTH cli-dir housekeeping
  passes** — `isSweptName` must not claim it *and* `pruneCLI` must not census it.
  Those fail differently, and fixing only the first converts one into the other:
  a name the sweep ignores still reaches the prune, where an in-flight blob sorts
  newest, takes a `-cli-keep` slot and evicts a real version. `blobTempPrefix` is
  defined once and read by the creator, both passes **and `validateCLIVersion`** —
  a housekeeping name rule the validator does not consult is one an operator walks
  into with `-cli-version`, which is how `.blob-x` would have installed as an
  immortal, never-pruned binary. Same fourth reader `isSweptName` has, and for the
  same reason. Tests assert every half. Kept beside the
  destination rather than in the OS temp dir because `/tmp` is a tmpfs on many
  hosts, which would put the blob back in RAM and undo this whole change.
- **Measured, peak RSS, 400 MiB incompressible payload** (`/proc/<pid>/status`
  `VmHWM`, polled):

  | path | before | after |
  |---|---|---|
  | `-cli-zst` (was `os.ReadFile`) | 410 MB | **9 MB** |
  | `-cli-url` (was `io.ReadAll`) | 886 MB | **10 MB** |

  Peak memory is now **flat in the blob size**. `-cli-url` was ~2× the blob before
  because `io.ReadAll` holds the old and new buffers together while it grows.
  **Control:** the same 400 MiB as *zeros* — a 14 KB blob, identical decompressed
  size — peaks at 9 MB on both binaries, which is what shows the after-figure is
  baseline rather than workload.

  ⚠️ **An earlier version of this entry reported 885 MB → 419 MB and blamed a
  residual on the zstd decoder. That was an instrument error, not a finding.**
  `wait4`'s `ru_maxrss` reports the high-water mark across *all* reaped children,
  so every measurement after a large one silently inherited its number — which is
  why repeated runs returned byte-identical values. There is no decoder residual;
  only the first reading in each of those runs was real.
- ⚠️ **One string can move**, on a path nothing provokes: a `-cli-zst` blob that
  opens but fails **mid-read** now surfaces at decompress as `decompressing: `
  rather than `opening input: `. Recorded rather than claimed identical.
- Error ordering is preserved deliberately: the download temp falls back to the OS
  temp dir when the cli-dir does not exist yet, because `ensureCLI` creates that
  directory *after* the download and owns the `mkdir cli dir: ` error.

### D11 · Make the `-install` runnability probe deadline opt-in ✅ (opt-in) — impact M / cost L

- `isRunnable` runs `<cli> --version` to decide whether an installed CLI works.
  **The reference showed no deadline at or below 45 s on a CLI that never answers,
  and none at or below 90 s on one that eventually does.** Measured with a planted
  CLI that hangs on `--version`:

  | binary | outcome |
  |---|---|
  | reference `5db5e4a` | **still running when the harness killed it at 45 s** |
  | claustrum, bounded (the old hardcoded 15 s default) | returns at **15 s** |
  | claustrum, deadline off (the new default) | **still running when killed at 45 s** — matching the reference *within that window*; fixture is a planted `sleep 120`, not row 1's hang-forever CLI, which is why the paragraph below names it |
  | *control:* a CLI that answers instantly | **0 s on all three** |

  The control is what makes the 45 s mean something: without it, "the reference
  did not finish" is indistinguishable from a fixture that could never finish.
  This bounds the reference's deadline at *above 45 s*, not at *absent* — and the
  deadline-off row inherits exactly that limit, since it is the same 45 s cut.

  The deadline-off row was **measured on the flip branch**, not derived: a planted
  `sleep 120` CLI, `-install -cli-dir <d> -cli-version v1`, cut at 45 s, which
  exited 124 having emitted no `__INSTALL_RESULT__` line. Two controls fired — the
  same fixture with `-cli-probe-timeout 15s` returned at 15 s with `cliError "cli
  v1 missing and no --cli-url or --cli-zst provided"` (reproducing the old default
  exactly), and an instant CLI returned at 0 s with `cliWasPresent:true`.

  The **third column of the 20 s table below** was measured on the same branch and
  on its own fixture, since the cache-hit run above cannot stand in for a
  `-cli-zst` install: a 20 s CLI compressed to a fresh blob, installed into an
  empty cli-dir with the deadline off, returned at **20 s** with no `cliError`,
  `cliWasPresent:false` and `v1` present in the cli-dir. Controls: the same
  `-cli-zst` shape with an instant CLI returned at 0 s identically, and a **second
  blob of the same 20 s content** with `-cli-probe-timeout 15s` failed at 15 s with `cliError "installed
  cli at <path> is not runnable"` and an **empty** cli-dir — reproducing the old
  default's row exactly, which is what makes the deadline the only variable.
  ⚠️ Each arm needs its **own** blob: the first run consumes it, and a re-used
  blob makes the next arm answer `opening input: … no such file or directory` at
  0 s, which reads exactly like "no divergence".
- 🔴 **A bound is not a hang detector, and the "no observable delta on any honest
  path" framing this entry was drafted with is FALSE.** `isRunnable` is a wall-clock deadline, so it cannot separate "never
  answers" from "answers slowly" — and a CLI that answers honestly in 20 s (a cold
  binary on a network filesystem, a first run behind Gatekeeper, a loaded host)
  is not a broken one. **Measured 2026-08-07.** The 20 s row and its control were
  run on the reference and on claustrum-bounded-at-15 s with a CLI that sleeps,
  prints its version and exits 0; the third column was run **on the flip branch**
  with the same `-cli-zst` fixture family, a fresh blob per arm. The 90 s row is
  **reference-only** — no claustrum column was re-run for it, since a 15 s bound
  makes the outcome identical to the 20 s row and no deadline makes it identical to
  the 20 s row's third column:

  | scenario | reference `5db5e4a` | claustrum, bounded at 15 s (old default) | claustrum, deadline off (new default) |
  |---|---|---|---|
  | 20 s CLI, installed via `-cli-zst` | **installs it**, no `cliError`, returns at 20 s | **fails** at 15 s: `cliError "installed cli at <path> is not runnable"`, staged binary deleted, cli-dir left **empty** — **and the blob is consumed anyway**, so a re-run has nothing to install from | **installs it**, no `cliError`, returns at 20 s |
  | **90 s** CLI, same fixture shape | **installs it**, waiting 91 s | (not run) | (not run) |
  | *control:* the same fixture with a CLI that answers instantly | installs, 0 s | installs, 0 s | installs, 0 s |

  The 90 s row is why the flip below is the parity-correct answer rather than a
  larger constant: it moves the reference from "no deadline at or below 45 s" to
  **no deadline at or below 90 s**, so any claustrum deadline at or below 90 s
  diverges for some honest input and picking 30 s or 60 s only moves the boundary.
  (What the reference does above 90 s is still unmeasured — "effectively
  unbounded for any real CLI" is the practical reading, not a result.) The instant-CLI row is its control too: same fixture family,
  same harness, and it completes on both.

  The control is what makes it attributable: the only variable between the rows is
  how long the CLI takes to answer. The reference column above is the **no-cache**
  shape. On the three cached shapes it is expected to cache-hit and report
  `cliWasPresent:true` having installed nothing, since its guard showed no cut-off
  at or below 90 s — **derived, not separately measured**, and above 90 s neither
  binary has been probed. So the divergence is not confined to a broken
  CLI — claustrum **fails an install the reference completes, and discards a
  working binary**, on an input no one would call adversarial.
- **The delta is bounded by the deadline, not by honesty.** A CLI answering within
  the configured deadline is expected to behave identically on both — **derived,
  not measured**: only 0 s and 20 s were run against the then-hardcoded 15 s, so the
  boundary itself is inferred from the constant rather than bisected. Everything slower diverges, and how it diverges depends on
  which probe site sees it — see the two-site bullet below.
- ⚠️ **TWO probe sites, three different outcomes — "the observable" is not one
  string.** `isRunnable` is called on the cache-hit guard (`isRegularFile &&
  isRunnable`) and again after extraction. A timeout on the first is
  indistinguishable from a cache miss, so what follows depends on the flags rather
  than on the timeout. All four measured 2026-08-07 **against the then-hardcoded
  15 s**; at the default there is no deadline and none of these four shapes occurs.
  Rows 1 and 4 were re-run on the flip branch with `-cli-probe-timeout` and
  reproduced (row 1 at both 15 s and 5 s, row 4 at 15 s); rows 2 and 3 were not
  re-run, and that the flag reproduces them follows from the code — it sets the same
  `cliProbeTimeout` the old constant occupied — rather than from a measurement. The cached CLI is the
  same 20 s fixture throughout — what varies is what the source flag supplies,
  which is why row two needs a replacement that answers in time:

  | shape | claustrum, deadline opted in (figures: 15 s) | note |
  |---|---|---|
  | cached slow CLI, **no** source flag | `cliError "cli <v> missing and no --cli-url or --cli-zst provided"`, 15 s | the working CLI is still on disk and is reported missing; a plainly absent file produces this string too, so it does not identify a timeout |
  | cached slow CLI **+** a source (`-cli-zst` or `-cli-url`) supplying a CLI that answers **in time** | **no `cliError` at all**, 15 s | silently reinstalled — the observable is the *absence* of an error, plus `cliWasPresent:false`. On `-cli-zst` the blob is consumed here too: the consume rule keys on decompression succeeding, not on which shape ran |
  | cached slow CLI **+** a source supplying the **same slow** CLI | `cliError "installed cli at <path> is not runnable"`, **~2× the deadline** (measured **30 s** at 15 s) | both probes time out. The cached working binary **survives** — the rename never runs. With `-cli-zst` the operator's blob is consumed as well; with `-cli-url` there is none to lose, since the download temp is removed by its own defer either way. Arguably the likeliest shape in practice: a CLI is usually slow because the *host* is, and the fresh copy runs on the same host |
  | no cache, slow CLI arriving via `-cli-zst` | `cliError "installed cli at <path> is not runnable"`, 15 s | staged binary deleted, cli-dir empty, blob consumed |

  Silent recovery therefore needs the *replacement* to be fast: it is the
  stale-hanging-CLI story, not the slow-CLI one.

- ⚠️ **The facts frame is not the whole end state: with the deadline opted in, the
  cache-hit shapes also run the sweep, and the silent one runs the prune.** (At the
  default the cache-hit guard passes, so none of this is reached.) The reference touches the cli-dir
  only when it attempts an install, so on a cache hit it touches nothing
  (`install.go` records that contrast itself). Claustrum's cache-hit guard fails
  instead, so it falls into `ensureCLI` and `sweepFetchTemps` runs on every cached
  shape — removing `.fetch-*` and `*.zst` litter — and on the silent shape the
  install succeeds, so `pruneCLI` runs too and can **evict a CLI version the
  reference would leave in place** under `-cli-keep`. Derived from the call order,
  **not measured**. The fixture: a cli-dir with four versions, `-cli-keep 3`, a
  `leftover.zst` and a `.fetch-orphan`, the 20 s CLI cached, and `-cli-zst`
  supplying a fast replacement — claustrum should sweep and prune, the reference
  leave all six. **Control that must fire:** the same directory with an instant
  CLI, where both must cache-hit and leave all six.
- **`cliWasPresent` flips, and it is a structured field rather than a string.**
  With the deadline opted in, in all three cached shapes above it comes back
  `false` for a CLI that is present and works — including the silent one, where it is the ONLY field in the facts
  that moves. The reference, which showed no deadline at or below 45 s, has no
  equivalent 15 s cut-off to trip. Worth more attention than the `cliError`
  wording: a client reads this field, it does not parse prose.
- **Trade:** matching means reintroducing an *unbounded wait* in `-install` — not a
  hang, per this section's own rule. The recovery half **is** observed, twice: the
  reference answered a 20 s CLI at 20 s and a 90 s CLI at 91 s, so "it answers as
  soon as the CLI does" is measured up to 90 s, not merely the natural reading. That is
  still the right call, but the cost is higher than this entry used to admit — it
  is not only "a hanging CLI is handled", it is also "a slow-but-working CLI can
  fail its install and lose **both** its binary and the blob it came from" (on the
  `-cli-zst` shape; the cached shapes leave the working binary in place) —
  recovery then needs a fresh upload, because the consume rule keys on
  decompression succeeding, not on the install succeeding. Raising the deadline
  would have reduced the second without giving up the first — 15 s was never
  measured against anything — but that was rejected in favour of defaulting it off,
  because every finite value merely moves the boundary.
- **Why it stopped being always-on.** It did clear the bar this section sets —
  the reference's behaviour on the motivating path is an apparently unbounded wait
  (no bound at or below 90 s) rather than a frame, the same justification D4 and D5
  use explicitly, D12 restates in its Trade bullet, and D13 supplies in a different
  form ("an input no honest caller produces"). What the 2026-08-07 measurement added
  is a *cost* the bar does not weigh: an honest-but-slow CLI pays for it, and
  Desktop owns the argv on `-install`, so the caller who pays cannot decline. That
  is the same argument D3 and D10 used, and it is why this took the same shape they
  did rather than a larger constant.
- ✅ **The flip was verified against the reference, not just argued.** Measured
  2026-08-07 on the cached-slow shape, cli-dir seeded with a 20 s `v1` plus a
  `.fetch-orphan` and a `leftover.zst`, no source flag:

  | arm | elapsed | facts | cli-dir after |
  |---|---|---|---|
  | reference `5db5e4a` | 21 s | `cliWasPresent:true`, no `cliError` | `.fetch-orphan leftover.zst v1` |
  | claustrum, deadline **off** (new default) | 20 s | `cliWasPresent:true`, no `cliError` | `.fetch-orphan leftover.zst v1` |
  | *control:* claustrum, `-cli-probe-timeout 5s` | 5 s | `cliWasPresent:false`, `cliError "cli v1 missing and no --cli-url or --cli-zst provided"` | `v1` |

  The control is what makes row 2 a measurement rather than blindness: it moves
  **every** observable — elapsed, `cliWasPresent`, `cliError`, and the directory,
  where the sweep removes the litter the other two arms leave in place. So this run
  also settles the **sweep half** of the sweep/prune contrast below on D11's own
  fixture: the reference cache-hits and touches nothing, and at the new default so
  does claustrum. The prune half is still derived.
- **Shipped as: default `0` = no deadline**, which matches the reference on every
  input measured (still running at 45 s on a CLI that never answers; installing one
  that answers at 90 s). What it does above 90 s remains unmeasured, so this is
  parity with the observed behaviour, not a proof of no deadline at all. With
  the bound opt-in via `-cli-probe-timeout <duration>` **and** the
  `cli-probe-timeout` key in `claustrum.conf` (explicit flag > config > default).
  The config key is the one that matters, for the same reason as `max-cli-bytes`:
  Desktop owns the argv on `-install`. The value is a Go duration (`30s`, `2m`);
  `time.ParseDuration` rejects a bare number rather than reading it as nanoseconds
  — **except for zero, and the set of zeroes is unbounded.** Two halves: *unitless*
  zero is exactly `0`, `+0` and `-0` (Go's parser special-cases the string `"0"`);
  *with a unit*, every zero-valued duration parses whatever its sign, so `0s`,
  `0h0m0s`, `-0m` and `-0.0s` all arrive as `0`. So does `-0.4ns`, which is a
  genuinely negative input truncated to zero rather than a spelling of zero at all.
  Every one of them slips past the `d >= 0` guard and lands on the disabled value,
  which is why this is a documentation problem and not a safety one: **no accepted
  input turns the deadline ON by accident.** Do not special-case `-0s` in a later
  reader — `-0m` behaves identically. The two paths differ
  on a negative: `-cli-probe-timeout -1s` normalises to disabled and logs an
  `[Install]` warning, while `cli-probe-timeout = -1s` in the config is **dropped
  silently**, leaving the default. Either way the deadline ends up off.
- **Disabled bypasses `context.WithTimeout` entirely** rather than passing a huge
  duration — the same rule D3 and D10 apply to their `io.LimitReader`s. A
  far-future deadline is a different thing that merely looks equivalent, and it
  would keep `exec.CommandContext`'s kill-on-cancel path in play where the
  reference showed no such cut-off.
- **What still ships bounded on the `-install` path:** only the linux-only `ldd`
  probe (tier item 5), which is not a D-number and is not proposed for a flip.
  D12's download bound took this same flip alongside it.

### D12 · Make the `-install` CLI download bound opt-in ✅ (opt-in) — impact M / cost L

> ⚠️ **The same flip D11 took is proposed for this bound in a follow-up PR** (default
> off = unbounded = parity). It has not been made; everything below describes what
> ships today, which is always-on.

- The download runs with `http.Client{Timeout: 5 * time.Minute}` (added in PR 59
  on `httpGet`, now `fetchToFile` after the streaming change in D10), which bounds
  the whole exchange. **The reference showed no bound at or below 400 s.**
  Measured against a server that sends `200 OK` with a `Content-Length` and then
  never sends the body:

  | binary | outcome |
  |---|---|
  | reference `5db5e4a` | **still downloading at 400 s**, killed by the harness |
  | claustrum | returns at **300 s** with `cliError "download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)"` |
  | *control:* the same server style serving a real 629 MB body | completes on both |

  400 s is deliberately past claustrum's own 300 s bound, so the run
  discriminates. The control matters for the same reason as D11's: a stall probe
  where nothing can ever succeed cannot tell "no deadline" from "broken fixture",
  and the D10 measurement supplies exactly that positive case on the same path.
- **No observable delta in the facts frame for a download that completes inside
  5 minutes** — derived from the constant, not bisected, exactly as D11's boundary
  is. A server sending its body at any usable rate is expected to behave
  identically. (On disk claustrum does create a download temp a SIGKILL can leave
  behind — `<cli-dir>/.blob-<random>` when the cli-dir already exists, and
  **`$TMPDIR/claustrum-fetch-<random>` when it does not, which is the first-install
  case**: `fetchToFile` runs *before* `ensureCLI`'s `os.MkdirAll`, so its
  `os.CreateTemp` in the cli-dir fails and falls back — putting the blob on the
  very `/tmp` that keeping it beside the destination exists to avoid. No claim is
  made here about whether the reference creates anything equivalent — that was
  never measured, as D10 and PROTOCOL both record.) The divergence appears
  against a stalled or black-holed download — where the reference was still
  waiting at 400 s — **and, by the same argument as D11, against an honest
  download that is merely too slow.** `http.Client.Timeout` bounds the whole
  exchange including the body read, so a real 600 MiB body over a link that needs
  six minutes trips it exactly as a black hole does. The 629 MB control passed
  because it *arrived in time*, not because it was honest.
- ✅ **The honest-but-slow half is MEASURED, on a fixture that straddles the
  retracted bound.** It used to be derived from `http.Client.Timeout` semantics
  alone, on the grounds that discriminating it needed a >5-minute run. That
  requirement was real: an earlier draft of this entry used a 14 s dribble and
  claimed it settled the question, which it does not — 14 s is far under the
  retracted 300 s, so the **old always-on build would have installed it too**, and
  both arms answer "installs" whether or not the divergence exists. Re-run
  2026-08-07 with a valid 30-byte zstd CLI served **one byte at a time over a
  target 335 s**, correct `-cli-checksum`, three concurrent servers so every arm
  sees the same conditions:

  | arm | elapsed | facts |
  |---|---|---|
  | reference `5db5e4a` | **324 s** | installs, **no `cliError`** |
  | claustrum, `-cli-download-timeout 5m` (the retracted default) | **300 s** | `cliError "download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)"` |
  | claustrum, bound **off** (new default) | **324 s** | installs, **no `cliError`** |

  Row 2 is what the 14 s fixture could not produce: an **honest** download, failed
  by the value that shipped, completed by the reference and by the new default. The
  three arms differ in exactly one variable.

  ⚠️ **Generalisable, and it is why the first fixture was useless:** a dribble
  discriminates a bound **you can set**, because you place the fixture above it.
  Discriminating the *reference's* requires exceeding a value you do not know — so
  the only usable fixture is one that exceeds the value **under test on your own
  side**. "Dribbling slowly discriminates at any duration" is true of claustrum and
  false of the reference arm.

  ⚠️ **A second confounder, also worth keeping.** An earlier attempt served an
  INVALID body (plain `xxxx`); the reference returned at **0 s** with
  `decompressing: invalid input: magic number mismatch` — not a download bound at
  all, but D13's decompress-first ordering short-circuiting on the first bytes. Any
  D12 fixture must carry a valid zstd blob, or D13 answers the question instead.
- **Trade:** matching means an `-install` that waits without bound on a network
  path the caller does not control — an *unbounded wait*, not a hang. The recovery
  half **is** observed, up to 324 s: the straddling run shows the reference
  completing a body that arrives over 324 s, and claustrum at the new default doing
  the same. Longer completions are unprobed. (The older *stall* fixture never sent
  a body, so it could not show recovery at all.)
- **Shipped as: default `0` = no bound.** On the 324 s dribble it matches the
  reference, measured. On the 400 s never-arrives body the reference's
  still-downloading row was measured, but **claustrum at the new default was not
  re-run there** — that half is derived from `Client.Timeout` being zero. Opt in
  with `-cli-download-timeout <duration>` **or** the `cli-download-timeout` key in
  `claustrum.conf` (explicit flag > config > default); the config key is the
  reachable one, because Desktop owns the argv on `-install`. (Scope: "matches the
  reference" is about this bound. D13 is a measured difference on the same
  `-cli-url` path.)
- **Zero is the stdlib's own "no timeout" sentinel**, so `http.Client{Timeout: 0}`
  IS the bypass — no huge-but-finite value stands in for "off". Same property D3
  and D10 get by skipping their `io.LimitReader`s, obtained here for free rather
  than by branching.
- ⚠️ **That frees the body read, not every clock on the path.** `fetchToFile`
  leaves `Transport` nil, so `http.DefaultTransport` applies
  `net.Dialer{Timeout: 30s}` and `TLSHandshakeTimeout: 10s`. A host that black-holes
  SYN still fails at 30 s with this bound off. Both are stdlib defaults, always-on
  and **unnumbered** — neither probed on the reference, so whether they diverge is
  open. Named here so "unbounded" is not read wider than it is.
- **Why it stopped being always-on.** Identical to D11's argument: the bar this
  section sets is cleared (the reference's behaviour on the motivating path is an
  apparently unbounded wait rather than a frame), but the bar does not weigh the
  *cost* — an honest-but-slow download pays it, and Desktop owns the argv, so the
  caller who pays cannot decline. The 324 s row makes that cost a measurement
  rather than an inference.
- ⚠️ **When opted in, it bounds the exchange, not the throughput.** A server
  dribbling bytes slower than the configured duration trips it, and one that
  finishes just inside does not — so it is a deadline, not a stall detector. That
  is exactly why it is no longer on by default.

### D13 · `-install` verifies the checksum before decompressing ✅ (always-on) — impact M / cost L

- On the `-cli-url` path the **reference decompresses first** and aborts on the
  first invalid bytes; **claustrum hashes the response as it streams to disk,
  verifies the checksum, then decompresses**. Of the three combinations measured,
  one reveals the order — a blob that is **both** corrupt zstd **and**
  wrong-checksummed:

  | input | reference | claustrum |
  |---|---|---|
  | corrupt blob **+ wrong checksum** | `decompressing: invalid input: magic number mismatch` | `checksum mismatch: expected=…, actual=…` |
  | *control:* corrupt blob + correct checksum | `decompressing: invalid input: …` | **same** |
  | *control:* valid blob + wrong checksum | `checksum mismatch: …` | **same** |

  Both controls come back identical, which is why the single differing row is
  attributable to ordering rather than to the fixture. Measured at `5db5e4a`.
- **Observable delta:** the `cliError` string, and only that — the two controls
  compared reply strings, not on-disk end state. Of the three combinations measured
  it differs on one — the combined failure; the other two are identical. That is a
  claim about the three rows above, not about every possible input, and D11 is the
  reminder of why the distinction matters.
- **Trade:** matching means feeding unverified bytes to the decompressor — giving
  up a verify-then-use property for parity on an input no honest caller produces.
- **The memory cost of verifying first is one pass, not a buffer.** The download
  streams to a temp file and is hashed on the way past (D10), so peak **RSS** is
  flat in the blob size — measured 886 MB → 10 MB on a 400 MiB
  payload. Verifying before decompressing therefore costs one full read of the
  blob from disk before decompression starts, and nothing resident. **No claim is
  made here about the reference's own memory behaviour**: whether it streams,
  buffers, or decompresses concurrently was never measured, and the trade above
  stands without it.
- **Not the same thing as D1.** D1 is about *whether* the local `-cli-zst` blob is
  verified at all; D13 is about the *order* of verify and decompress on the
  `-cli-url` download.

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
  `d20a77da` (see *About the 496/496 figure* below — it is historical, and its
  unit is not what today's harness counts).
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
  shutdown" reliable (it was racy before). No wire effect — battery stays 496/496
  (again historical; see the note below).
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
  - `listen-pipe = true|false` — default for CT-5 (Windows-only).
  - `max-extract-bytes = <n>` — default for D3; `0` (the default) is no cap.
  - `max-cli-bytes = <n>` — default for D10; `0` (the default) is no cap.
  - `cli-probe-timeout = <duration>` — default for D11; `0` (the default) is no
    deadline. A Go duration (`30s`, `2m`); a bare number is rejected rather than
    read as nanoseconds, except zero, which parses in unboundedly many spellings
    (`0`, `+0`, `-0`, plus any zero-valued duration with a unit, plus a negative
    that truncates to zero like `-0.4ns`) and always means disabled. A negative is dropped silently here; on the
    flag path a negative warns and normalises, while a bare number is rejected by
    `flag.Duration` before any mode runs (usage + exit 2, no facts line). Every
    one of these leaves the deadline off.
  - `cli-download-timeout = <duration>` — default for D12; `0` (the default) is no
    bound. Same duration parsing and the same zero/negative edges as
    `cli-probe-timeout` above, with the same consequence: every accepted oddity
    leaves the bound off, so none of them can switch it on.
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
  and lower-cased; `metrics-addr` printable-ASCII only; `max-extract-bytes` and
  `max-cli-bytes` parsed as a non-negative int64, `cli-probe-timeout` and
  `cli-download-timeout` via
  `time.ParseDuration` and rejected if negative, anything else ignored), values
  used as data never as a format string.
- Verified: unit tests (each key's valid/invalid forms, unknown-key and malformed
  lines, case-insensitive keys, CLI-over-config precedence, non-regular-directory,
  and a `//go:build unix` FIFO case) + an end-to-end smoke test (stock / valid
  git-SHA impersonation with the client regex confirming a pin match / pre-prefixed
  & invalid → stock / uppercase normalized). Documented in
  [PROTOCOL.md](PROTOCOL.md) (`-version`).

### CT-4 · Opt-in hardened token persistence — deferred idea — impact L / cost L

- **Context.** As of reference `5db5e4a`, the daemon persists its token to
  `daemon.token` (`0600`) beside the socket for client reconnect (parity, on by
  default — `tokenpersist.go`, [PROTOCOL.md → Token
  persistence](PROTOCOL.md#token-persistence-daemontoken)). Two accepted parity
  caveats: the file survives an unclean
  kill/crash (cleanup runs only on graceful shutdown), and on Windows `0600` is
  not an owner-only DACL.
- **Idea (not built).** A `claustrum.conf` key — e.g. `persist-token = false`
  (skip persistence entirely, trading away file-based reconnect) and/or a
  Windows owner-only DACL on the file — for operators who prefer a smaller
  on-disk token window over drop-in reconnect. Must stay **absent ⇒ stock**
  (default persists, byte-for-byte parity) like the other CT gates.
- **Why deferred.** No demand yet; the default matches the reference, and the
  socket directory is already owner-scoped in the real deployment. Recorded so
  the security trade-off (flagged in review) isn't lost.

### CT-5 · Opt-in `-listen-pipe` Windows named-pipe transport ✅ — impact M / cost M

- **Shipped.** `-listen-pipe` (config `listen-pipe = true|false`) makes `-serve`
  *additionally* serve the exact same NDJSON JSON-RPC dispatch over a Windows
  named pipe, concurrently with the `AF_UNIX` socket (`pipetransport.go` +
  `pipetransport_windows.go`; go-winio, compiled into Windows builds only). Off by
  default ⇒ stock; the socket, wire contract, field ordering, and framing are
  unchanged whether a request arrives over the socket or the pipe.
- **Why.** A Windows client that cannot consume an `AF_UNIX` socket — notably
  Python `asyncio`, whose Unix transports are implemented only on its Unix event
  loop while its Windows Proactor loop natively supports named pipes — otherwise
  can't attach. The pipe is the additive escape hatch (clauster's ask).
- **Discovery + auth.** claustrum picks the pipe name
  (`\\.\pipe\claustrum-<instance-id>`, client-opaque) and publishes it to
  `rpc.pipe` beside the socket (atomic write before accepting/ready; removed on
  graceful shutdown — the `rpc.sock`/`daemon.token` lifecycle). Same in-band
  `"auth"` + `daemon.token` handshake.
- **Security.** Owner-only DACL (SDDL `D:P(A;;GA;;;<current-user-SID>)`, the
  named-pipe analogue of the socket's `0600`), local-only, no new authenticated
  surface (see
  [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
  Windows-only: ignored with a warning
  elsewhere (`honorListenPipe`), setup failure non-fatal (socket still serves).
- **Tests.** Cross-platform unit coverage of the name-file lifecycle / SDDL /
  instance-id on the Ubuntu leg; a Windows-only integration test drives the real
  pipe end-to-end (authed round-trip + wrong-token-denied) on the `windows-latest`
  CI leg.

### Candidates identified but NOT taken (CLI-mode parity, 2026-08-02)

Both are places where claustrum was friendlier than the reference and matching it
cost something real. They are recorded so the code can point somewhere durable
rather than at "the backlog" — **no decision is implied**, and nothing here is
shipped or scheduled.

- **Conditional `-stop` socket unlink.** `-stop` removes the socket path on every
  exit, including when no daemon answered — matching the reference, measured on
  three arms. That means it removes a path it did not create and cannot identify
  the owner of; a live foreign listener keeps its descriptor but loses its name.
  `os.Remove` does not distinguish shapes either, so a regular file or an empty
  directory at the `-socket` path goes the same way, and only socket-shaped paths
  were put in front of the reference. A `stat`-first variant that removes only a
  socket would be strictly safer **and a divergence** — the reference is
  unconditional. Deciding that is a maintainer call, not a code cleanup.
- **Fail fast on a missing `-serve` token source.** The check now runs in the
  detached child, so the launcher reports its ~10 s accept timeout and the real
  reason reaches only the child's own log — reference parity, measured at 10.02 s
  against 10.07 s. Keeping the old parent-side check answered in 0.03 s and named
  the actual problem. That is better operator experience and a divergence.

## About the 496/496 figure

Two entries above quote "battery **496/496**". **The figure is historical: it is
not reproducible, and its unit is not what the harness counts today.** It is kept
rather than rewritten because those lines record what was claimed at the time.

- **It was real and contemporaneous.** PR 97 (June 2026) reports it as
  "byte-identical, **496/496 frames**" from `scratch/probe/validate.sh` — the same
  harness path in use today.
- **It cannot mean "frames" in today's sense.** A complete run measured
  2026-08-06 is **59 responses + 7 frames** (66 objects) across **612 lines** of
  output. 496 is nowhere near any object count and is the same order as the line
  count, so the most economical reading is that it counted output lines and was
  labelled "frames". **That is a reconstruction, not a measurement.**
- **The battery has grown since**, which fits the direction: process-frame capture
  was fixed and the tilde fixtures were added after that figure was recorded.
- **The June harness is gone.** `scratch/probe/validate.sh` and `battery.js` are
  gitignored, so no commit holds an earlier version, and the file has been
  overwritten in place. Nothing else in the tree preserves a copy or a saved run.

**Recount at the time of writing rather than quoting any figure from here** — a
replacement number recorded in July (48 responses + 7 frames) had already drifted
by August for exactly this reason.

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding **required** new params to existing methods. *(An **optional**,
  gracefully-ignored param whose result fields vanish by default — the D1 /
  CT-1 pattern — is the sanctioned exception: it leaves the default frame
  byte-identical and degrades both ways.)*

Any of these would need a deliberate, documented protocol version bump.
