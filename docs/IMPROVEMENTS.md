# claustrum — shipped ledger

This is the record of completed hardening work on claustrum: the CI, test,
release, and safety items that took the daemon from a bare reimplementation to a
maintained drop-in. Every item here is **done**. It is history, not a backlog.

Most of this work is **wire-neutral** — it changes tooling, tests, or OS-level
behaviour without touching a JSON-RPC frame. The exceptions are the items that
knowingly change a frame or behaviour from the reference daemon. Those are the
numbered **deliberate divergences** (D1-D14) and **client-driven extensions**
(CT-1..CT-5), and they no longer live here.

## Deliberate divergences and the decision rules → docs/DIVERGENCES.md

The divergence catalog (D1-D14, CT-1..CT-5 — each with its default, how to
activate it, why it exists, and its reopen trigger) and **THE RULE** (the
four-rule, clause-(a)/(b)/(c) standard every divergence is judged against) now
live in their own canonical home: **[DIVERGENCES.md](DIVERGENCES.md)**. That
split makes the two things a contributor needs most — the byte-identical contract
and the list of where claustrum deliberately departs from it — first-class
instead of buried at the bottom of a done backlog. Per-method wire facts are in
[PROTOCOL.md](PROTOCOL.md); driver-claim provenance is in
[ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance).

## The shipped items

Impact/cost ratings are H/M/L/S as originally triaged. "Where" points at the
artifact that carries each item; the exhaustive measurement and correction notes
condensed out of this table are parked in
`scratch/docs-overhaul-2026-08-08/forensics/docs_IMPROVEMENTS.md`.

| # | Item | I/C | What shipped · where |
|---|------|-----|----------------------|
| 1 | CI workflow | H/L | `go vet`, `gofmt -l`, `make all`, `go test` gate every PR (`.github/workflows/ci.yml`). |
| 2 | In-repo Go test suite | H/M | `harness_test.go` + integration suites boot the daemon on a temp socket and assert each method's frames over the real wire — CI gates compatibility without the reference binary. |
| 3 | Golden-frame fixtures | H/M | `testdata/socket_*.golden.json`, asserted byte-equal; regenerate with `go test -run Socket -update`. |
| 4 | Atomic `-install` extract | M/L | `ensureCLI` stages at `.fetch-<random>` (not `<cliPath>.tmp`, so the orphan sweep can reclaim it) then `os.Rename`s into place; an interrupted install never leaves a half-written `cliPath`. End state and `__INSTALL_RESULT__` facts are byte-compatible. |
| 5 | Timeouts on `git`/`exec` calls | M/L | Wrapped git and the linux-only `ldd` libc probe in `exec.CommandContext`. **Both halves became numbered divergences — D5 (`gitTimeout`) and D14 (`ldd` probe) — now opt-in and off by default. See [DIVERGENCES.md](DIVERGENCES.md).** |
| 6 | pre-commit + `gofmt`/`vet` hooks | M/L | Zero-dependency hook in `.githooks/`, installed via `make hooks`; mirrors CI's lint order. Bypass with `--no-verify`. |
| 7 | `go vet`-clean + `staticcheck` in CI | M/L | `golangci-lint` (`.golangci.yml`: staticcheck + govet + errcheck + ineffassign + unused + misspell/unconvert) wired into the CI `lint` job. |
| 8 | Bounded replay buffer (ring) | M-H/M | Each per-process buffer capped at **16 MiB of serialized frame bytes** (each frame's JSON line incl. trailing newline); oldest frames drop and `firstSeq` advances. **The cap is parity, not tuning** — the reference's 16 MiB was measured, and `firstSeq` is wire-visible. Re-measure before changing. |
| 9 | stdin backpressure | M/M | A per-proc `stdinWriter` goroutine drains a bounded (`stdinQueueCap`, 8 MiB) FIFO; `process.stdin` enqueues and returns immediately, matching the reference's async behaviour. A full queue applies backpressure and logs `stdin backpressure: queue full`. |
| 10 | Fuzz the JSON-RPC parser | M/L-M | `fuzz_test.go`: `FuzzDispatch` (side-effectful methods skipped) + `FuzzBindParams`; seeds run in CI. |
| 11 | Release automation | H/M | `.goreleaser.yaml` + `release.yml`: 6-target builds, checksums, syft SBOM, cosign signing, SLSA provenance. Version PRs via **knope** (changesets-only). |
| 12 | Pin the Go toolchain | M/L | `go.mod` carries an explicit `toolchain` directive; CI/release use `go-version-file: go.mod`, so builds are reproducible against a known patch. |
| 13 | Structured/leveled logging | M/L-M | `logging.go`: a tiny leveled logger. The level tag is prepended **before** the `[Component]` prefixes (greps keep matching); threshold from `CLAUSTRUM_LOG_LEVEL`, **defaulting to `debug`** (always-on emit; the var only raises the floor). Stderr only — wire surface untouched. |
| 14 | Windows process-tree kill via Job Objects | M/M-H | Spawned children confined to a Job Object (`sysproc_windows.go`); `kill`/`killAll` call `TerminateJobObject` for whole-tree teardown, with `KILL_ON_JOB_CLOSE`. Adds `golang.org/x/sys` (Windows-only). No wire change. |
| 15 | Docs site (mkdocs) | M/M | `docs/` published via mkdocs-material to GitHub Pages; a `docs` workflow runs `mkdocs build --strict` on every PR and deploys on push to `main`. |
| 16 | `/metrics` counters | L-M/M | `metrics.go`: process-wide atomic counters, always-on; the `/metrics` endpoint is served **only** when `-metrics-addr` is set (off by default). Counts only, no auth → bind loopback. No wire change. |
| 17 | Duplicate-`id` spawn policy | L/L | Reusing a still-live `id` succeeds and replaces the registry entry (reference parity); claustrum also tears down the now-orphaned previous tree so it cannot leak (`TestSpawnDuplicateIDReplacesAndKillsOld`). No wire change. |
| 18 | Token from fd/stdin | L/L | `-token-fd <n>`: `-serve` reads the auth token from an open descriptor, never touching disk; the parent forwards it to the detached child over an inherited pipe (`CLAUSTRUM_TOKEN_PIPE`). Additive/off-wire. |
| 19 | Docs-site visibility/formatting pass | L/L-M | Restructured prose-heavy tables into per-item / per-method sections so every method gets its own table-of-contents entry on the published site. Site-only. |
| 20 | Windows CI test runner | M/M | Added a `windows-latest` leg to the test matrix; Unix fixtures replaced by the stdlib helper-process pattern (`helperproc_test.go`, `CLAUSTRUM_TEST_HELPER`) so streamed bytes stay byte-identical across OSes. `TestSocketFilesBattery` skips on Windows (its golden pins a Unix mode string). |
| 21 | Exited-child group-kill guard + LIVED-mutant triage | S/S | `kill`/`killAll`/duplicate-id replace now skip already-exited children so a recycled pgid can't be SIGKILLed (Unix); documented in [PROTOCOL.md](PROTOCOL.md) `process.kill`. Post-run mutant triage closed 5 real assertion gaps. |
| 22 | Spawn/exec syscall hardening | S/S | A `strace -f` differential found two syscall-level divergences kept on purpose because claustrum is the safer side: `git.*` runs `git -C <repo>` (the daemon never `chdir`s, so concurrent requests don't race), and `process.kill` signals the whole process group. Frames are byte-identical. |
| 23 | Recover from handler panics | M/S | The per-request goroutine wraps dispatch in `defer recover()`, so a handler panic no longer takes the whole daemon down. The reply (`-32603 "recovered panic: <v>"`) is **claustrum's own and is NOT a parity claim** — the path is unreachable, so no client can observe it. Recover is scoped to dispatch only; `server.shutdown` still replies nothing. |

---

*Footnote — the "496/496" figure.* Older records quote a battery result of
"496/496 frames". That figure is **historical and not reproducible**: its unit is not what the
harness counts today (a reconstruction puts it at output lines, not frames), the
June 2026 harness that produced it has been overwritten, and the battery has
grown since. **Recount at the time of writing rather than quoting any figure.**
Full reconstruction in the parked forensics.
