# claustrum — shipped ledger

This document records the completed hardening work on claustrum: the CI, test,
release, and safety items that changed the daemon from a bare reimplementation
into a maintained drop-in. Every item here is **done**. This document is
history, not a backlog.

Most of this work is **wire-neutral**. It changes tooling, tests, or OS-level
behaviour, but it does not change a JSON-RPC frame. The exceptions are the items
that knowingly make a frame or a behaviour different from the reference daemon.
Those items are the numbered **deliberate divergences** (D1-D14) and the
**client-driven extensions** (CT-1..CT-5). They no longer live here.

## Deliberate divergences and the decision rules → docs/DIVERGENCES.md

The divergence catalog and **THE RULE** now live in their own canonical home:
**[DIVERGENCES.md](DIVERGENCES.md)**. The catalog holds D1-D14 and CT-1..CT-5.
Each entry gives its default, how to activate it, why it exists, and its reopen
trigger. THE RULE is the four-rule, clause-(a)/(b)/(c) standard, and every
divergence is judged against it. A contributor needs two things most: the
byte-identical contract, and the list of places where claustrum departs from
that contract on purpose. The split makes both first-class. Before the split,
they were at the bottom of a done backlog.
[PROTOCOL.md](PROTOCOL.md) gives the per-method wire facts.
[ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance) gives the
driver-claim provenance.

## The shipped items

The impact/cost ratings are H/M/L/S, as the original triage set them. The
"where" part of each row points at the artifact that carries the item. The
split condensed the full measurement and correction notes out of this table.

| # | Item | I/C | What shipped · where |
|---|------|-----|----------------------|
| 1 | CI workflow | H/L | `go vet`, `gofmt -l`, `make all`, `go test` gate every PR (`.github/workflows/ci.yml`). |
| 2 | In-repo Go test suite | H/M | `harness_test.go` and the integration suites boot the daemon on a temp socket. They assert each method's frames over the real wire. Thus CI gates compatibility without the reference binary. |
| 3 | Golden-frame fixtures | H/M | The tests assert `testdata/socket_*.golden.json` byte-equal. To regenerate them, run `go test -run Socket -update`. |
| 4 | Atomic `-install` extract | M/L | `ensureCLI` stages the file at `.fetch-<random>`, not at `<cliPath>.tmp`, so the orphan sweep can reclaim it. `ensureCLI` then renames the file into place with `os.Rename`. An interrupted install never leaves a half-written `cliPath`. The end state and the `__INSTALL_RESULT__` facts stay byte-compatible. |
| 5 | Timeouts on `git`/`exec` calls | M/L | Wrapped git and the linux-only `ldd` libc probe in `exec.CommandContext`. **Both halves became numbered divergences: D5 (`gitTimeout`) and D14 (`ldd` probe). Both are now opt-in and off by default. See [DIVERGENCES.md](DIVERGENCES.md).** |
| 6 | pre-commit + `gofmt`/`vet` hooks | M/L | A zero-dependency hook in `.githooks/`; `make hooks` installs it. It uses the same lint order as CI. `--no-verify` bypasses it. |
| 7 | `go vet`-clean + `staticcheck` in CI | M/L | The CI `lint` job runs `golangci-lint` (`.golangci.yml`: staticcheck + govet + errcheck + ineffassign + unused + misspell/unconvert). |
| 8 | Bounded replay buffer (ring) | M-H/M | A cap of **16 MiB of serialized frame bytes** applies to each per-process buffer. The count includes each frame's JSON line and its trailing newline. The oldest frames drop, and `firstSeq` advances. **The cap is parity, not tuning** — the reference's 16 MiB was measured, and `firstSeq` is wire-visible. Measure again before you change it. |
| 9 | stdin backpressure | M/M | A per-proc `stdinWriter` goroutine drains a bounded FIFO (`stdinQueueCap`, 8 MiB). `process.stdin` puts the data in the queue and returns immediately, which matches the reference's async behaviour. A full queue applies backpressure and logs `stdin backpressure: queue full`. |
| 10 | Fuzz the JSON-RPC parser | M/L-M | `fuzz_test.go` adds `FuzzDispatch` and `FuzzBindParams`. `FuzzDispatch` skips the side-effectful methods. CI runs the seeds. |
| 11 | Release automation | H/M | `.goreleaser.yaml` + `release.yml`: 6-target builds, checksums, syft SBOM, cosign signing, SLSA provenance. **knope** opens the version PRs (changesets-only). |
| 12 | Pin the Go toolchain | M/L | `go.mod` carries an explicit `toolchain` directive. CI and release use `go-version-file: go.mod`, so builds are reproducible against a known patch. |
| 13 | Structured/leveled logging | M/L-M | `logging.go` holds a tiny leveled logger. The logger puts the level tag **before** the `[Component]` prefixes, so existing greps keep matching. `CLAUSTRUM_LOG_LEVEL` sets the threshold, which **defaults to `debug`**: the logger emits everything, and the variable only raises the floor. The logger writes to stderr only and does not touch the wire surface. |
| 14 | Windows process-tree kill via Job Objects | M/M-H | A Job Object confines the spawned children (`sysproc_windows.go`). `kill` and `killAll` call `TerminateJobObject` to tear down the whole tree, with `KILL_ON_JOB_CLOSE`. This item adds `golang.org/x/sys` (Windows-only). No wire change. |
| 15 | Docs site (mkdocs) | M/M | mkdocs-material publishes `docs/` to GitHub Pages. A `docs` workflow runs `mkdocs build --strict` on every PR and deploys on a push to `main`. |
| 16 | `/metrics` counters | L-M/M | `metrics.go` holds process-wide atomic counters, which are always on. The daemon serves the `/metrics` endpoint **only** when `-metrics-addr` is set (off by default). It gives counts only and has no auth → bind loopback. No wire change. |
| 17 | Duplicate-`id` spawn policy | L/L | A spawn that reuses a still-live `id` succeeds and replaces the registry entry (reference parity). Claustrum also tears down the previous tree, which is now an orphan, so that tree cannot leak (`TestSpawnDuplicateIDReplacesAndKillsOld`). No wire change. |
| 18 | Token from fd/stdin | L/L | With `-token-fd <n>`, `-serve` reads the auth token from an open descriptor, so the operator writes no token file. The parent sends the token to the detached child over an inherited pipe (`CLAUSTRUM_TOKEN_PIPE`). Additive/off-wire. |
| 19 | Docs-site visibility/formatting pass | L/L-M | Changed the prose-heavy tables into per-item and per-method sections, so every method gets its own table-of-contents entry on the published site. Site-only. |
| 20 | Windows CI test runner | M/M | Added a `windows-latest` leg to the test matrix. The stdlib helper-process pattern (`helperproc_test.go`, `CLAUSTRUM_TEST_HELPER`) replaced the Unix fixtures, so the streamed bytes stay byte-identical on every operating system. `TestSocketFilesBattery` skips on Windows, because its golden pins a Unix mode string. |
| 21 | Exited-child group-kill guard + LIVED-mutant triage | S/S | `kill`, `killAll`, and the duplicate-id replace now skip the children that already exited, so the daemon cannot SIGKILL a recycled pgid (Unix). [PROTOCOL.md](PROTOCOL.md) records this at `process.kill`. A mutant triage after the run closed 5 real assertion gaps. |
| 22 | Spawn/exec syscall hardening | S/S | A `strace -f` differential found two syscall-level divergences. Claustrum keeps both on purpose, because claustrum is the safer side. First, `git.*` runs `git -C <repo>`: the daemon never calls `chdir`, so concurrent requests do not race. Second, `process.kill` signals the whole process group. The frames are byte-identical. |
| 23 | Recover from handler panics | M/S | The per-request goroutine wraps dispatch in `defer recover()`, so a handler panic no longer stops the whole daemon. The reply (`-32603 "recovered panic: <v>"`) is **claustrum's own and is NOT a parity claim** — the path is unreachable, so no client can observe it. The recover applies to dispatch only. `server.shutdown` still replies nothing. |

---

*Footnote — the "496/496" figure.* Older records quote a battery result of
"496/496 frames". That figure is **historical and not reproducible**, for three
reasons. Its unit is not the unit the harness counts today: a reconstruction
puts the unit at output lines, not frames. The June 2026 harness that produced
the figure was overwritten. The battery also grew after that date.
**Count again at the time of writing. Do not quote any figure.** The parked
forensics hold the full reconstruction.
