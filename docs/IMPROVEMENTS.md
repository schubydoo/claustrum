# claustrum — improvement backlog (stack-ranked)

Everything here **retains wire compatibility** (no change to method shapes, error
codes, or frame formats unless explicitly noted as protocol-safe). Ranked by
**lowest cost / highest impact** first. Scoring: Impact and Cost are H/M/L;
"Score" is a rough impact÷cost ordering, not a formula.

> Compatibility rule of thumb: anything touching `rpc.go`, `methods_*.go`,
> `process.go`, or `results.go` must keep the validation battery byte-identical.

## Tier 1 — quick wins (do first)

| # | Improvement | Impact | Cost | Why / compatibility |
|---|---|---|---|---|
| 1 | ~~**CI workflow** (`go vet`, `gofmt -l`, `make all`, `go test`)~~ ✅ **done** | H | L | Gates every PR; catches cross-compile + format regressions. Pure additive. Shipped as `.github/workflows/ci.yml`. |
| 2 | ~~**In-repo Go test suite**~~ ✅ **done** | H | M | Shipped: `harness_test.go` + `integration_test.go` + `integration_fs_git_test.go` boot the daemon on a temp socket and assert each method's frames over the real wire path — CI now gates compatibility without the reference binary. |
| 3 | ~~**Golden-frame fixtures**~~ ✅ **done** | H | M | Shipped: `testdata/socket_*.golden.json` (responses/errors, `files.*`, `git.*`), asserted byte-equal; regenerate with `go test -run Socket -update`. Locks the contract so refactors can't drift silently. |
| 4 | **Atomic `-install` extract** | M | L | Decompress to `cliPath.tmp` then `rename()` into place, so an interrupted install never leaves a half-written/partially-runnable CLI. Behavior-compatible (same facts on success). |
| 5 | **Timeouts on `git`/`exec` calls** | M | L | `git.*` shells out with no deadline; a wedged git can hang a request goroutine. Wrap in `exec.CommandContext` with a generous timeout (as `isRunnable` already does). Results unchanged on the happy path. |
| 6 | **pre-commit + `gofmt`/`vet` hooks** | M | L | Keeps contributors green locally; same tools as CI. |
| 7 | ~~**`go vet`-clean + `staticcheck`** in CI~~ ✅ **done** | M | L | Shipped via `golangci-lint` (`.golangci.yml`, standard set incl. staticcheck + govet + errcheck + ineffassign + unused, plus misspell/unconvert), wired into the CI `lint` job. |

## Tier 2 — medium

| # | Improvement | Impact | Cost | Why / compatibility |
|---|---|---|---|---|
| 8 | **Bounded replay buffer (ring)** | M-H | M | The per-process buffer is currently unbounded — a noisy long-lived process grows memory without limit. Cap by bytes/frames and advance `firstSeq` as old frames drop. **Protocol-safe**: `reattach` already returns `firstSeq`, so clients are expected to handle a moved floor. Make the cap generous + configurable. |
| 9 | **stdin backpressure** | M | M | `process.stdin` writes synchronously; a slow child can block the caller. Add a bounded async writer with a "queue full" backpressure signal (the reference exposes a `stdin backpressure: queue full` guard). No wire change. |
| 10 | **Fuzz the JSON-RPC parser** | M | L-M | `go test -fuzz` on the request decoder/dispatch — cheap hardening against malformed lines. |
| 11 | **Release automation** | H | M | Build the 6 targets reproducibly, publish checksums + SBOM + provenance, cosign-sign artifacts, GitHub Releases via release-please. High distribution value; supply-chain rigor for a downloadable binary. |
| 12 | **Pin the Go toolchain** | M | L | Add a `toolchain`/CI matrix so release builds use a fixed Go (e.g. 1.23.x) for reproducible, verifiable binaries. |
| 13 | **Structured/leveled logging** | M | L-M | Replace ad-hoc `fmt.Fprintf(stderr,…)` with a tiny leveled logger — **but keep the exact existing log strings** (`[Server]`, `[process.Manager]`, `[frameSink]`, `[shellenv]`) so anything that greps them still works. |

## Tier 3 — larger / lower-priority

| # | Improvement | Impact | Cost | Why / compatibility |
|---|---|---|---|---|
| 14 | **Windows process-tree kill via Job Objects** | M | M-H | Current Windows `kill` is a best-effort `TerminateProcess` of the parent; a child subtree can leak. A Job Object kills the whole tree like a Unix process group. Windows-only, no wire change. |
| 15 | **Docs site (mkdocs)** | M | M | Promote `docs/` to a published, community-facing site. |
| 16 | **`/metrics` or counters** | L-M | M | Optional observability (spawns, bytes streamed, reattach counts). Off by default; no wire change. |
| 17 | **Duplicate-`id` spawn policy** | L | L | Decide/clarify behavior when `process.spawn` reuses a live id (today it replaces the registry entry). Document + test. |
| 18 | **Token from fd/stdin** | L | L | Allow passing the auth token via an fd in addition to `-token-file`/env, for callers that don't want a temp file. Additive. |

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods.

Any of these would need a deliberate, documented protocol version bump.
