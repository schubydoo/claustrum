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
`git` invocation (`gitTimeout` 60s). Happy-path results/frames unchanged; an
attack/pathological-path-only divergence from the reference (which has no
deadline).

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
the context error. The reference has no deadline and simply blocks, emitting
nothing. Unreachable for it, so not a parity break, but it is ours and it is on
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
reference never emits. It is confined to this pathological path, and **no OTHER
frame moves because of the deadline** — which is the scoped form of a claim that
used to read "every reference-reachable frame stays byte-identical". That whole-
wire version was false for the entire window between the deadline work and the
stdout-only fix, for the reason recorded in the CORRECTION above: `git.status`
and `git.list_branches` put a claustrum-only `-32603` on the wire when our own
deadline killed git. It is arguably true again now, which is exactly the trap —
it was restated as scope rather than deleted so the two copies cannot drift apart
a second time.

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

## Deliberate divergences (post-parity)

Unlike everything above, these **knowingly change a frame/behavior** from the
reference. They follow the "match upstream first, then improve" plan: only
consider them now that the harness proves parity, and document each as an
*intentional* divergence in [`PROTOCOL.md`](PROTOCOL.md) + the PR if adopted.

**Most are opt-in; some are always-on, and the entry says which.** An always-on
divergence needs a reason the opt-in shape does not work — usually that the thing
it prevents is unrecoverable (D2) or that the reference's own behavior on that
path is an **unbounded wait** rather than a frame (D4, D5). *Unbounded wait*, not
"hang" — the reference recovers the moment the wait's cause clears, and saying
otherwise is a correction D4 already had to make once.

D4–D9 were shipped without numbers and are catalogued here retrospectively. Each
carries the evidence that was already in [`PROTOCOL.md`](PROTOCOL.md) rather than
a new claim — **including, for D8, the explicit statement that no measurement of
the reference exists on that path.**

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

*(**D3 is reserved** by the open extract-cap PR — the number is taken even though
its entry is not on `main` yet. Recorded here because an unrecorded reservation is
exactly how D2 nearly got reused.)*

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

- claustrum caps every git invocation at 60 s (`methods_git.go`, all three call
  sites). **The reference showed no deadline at or below 75 s** on
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
- **A second arm reaches the wire and this entry used to omit it:** on `git.status`
  and `git.list_branches` the same cap surfaces as `-32603` with `err.Error()` =
  **`signal: killed`** (§5 above). Unreachable on the reference, so not a parity
  break — but it is ours and it is on the wire.
- Documented in [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove` (and §5 for
  the `signal: killed` arm).

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

### D8 · `remote-server.log` is declined rather than shared ✅ (always-on) — impact L / cost L

- ⚠️ **Only the DECLINE is the divergence.** Recreating `remote-server.log` fresh
  on every start (unlink + create, not truncate in place) is **measured parity** —
  `server.go` records the probe: a planted `666 root` log came back `600 claude`
  on the reference (the *owner* changed, so it recreated the file) and `666 root`
  on claustrum, which truncated and wrote into it. claustrum was changed to match.
  The owner is the right observable precisely because `chmod` cannot forge it.
- The divergence is the **fallback**: when the existing log cannot be replaced —
  a sticky directory holding another user's file — claustrum declines the log
  entirely and the daemon's output falls back to the launcher's inherited stdio.
- **The weakest-evidence entry in this section, and it says so.** This is a
  hardening on an attack path **the reference was never measured on**, so the
  claim is not that the reference behaves differently — only that claustrum's
  behavior here is a deliberate choice. The log-handling behavior is otherwise
  identical (the banner word and the level tag are an intentional rebrand, so the
  log's *contents* never were).
- 🔬 **"Not measured" here means "not yet measured", and the run is cheap.** On an
  ephemeral VM: `chmod 1777` the socket dir, plant a root-owned `0600` log, start
  the reference `-serve` as an unprivileged user, then read the file's owner and
  the launcher's inherited stderr. Two outcomes, both decisive — the reference
  writes into the foreign file (D8 becomes parity-measured) or it falls back too
  (D8 retires). Do not leave this a judgement call by default.
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
  - `listen-pipe = true|false` — default for CT-5 (Windows-only).
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

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding **required** new params to existing methods. *(An **optional**,
  gracefully-ignored param whose result fields vanish by default — the D1 /
  CT-1 pattern — is the sanctioned exception: it leaves the default frame
  byte-identical and degrades both ways.)*

Any of these would need a deliberate, documented protocol version bump.
