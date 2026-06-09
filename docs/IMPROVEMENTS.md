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
| 8 | ~~**Bounded replay buffer (ring)**~~ ✅ **done** | M-H | M | Shipped in #58: each per-process buffer is capped at 50 MiB of base64 data (was unbounded — a noisy long-lived process grew memory without bound); the oldest frames drop and `firstSeq` advances past the cap. **Protocol-safe** — `reattach` returns `firstSeq`, so clients handle the moved floor. |
| 9 | **stdin backpressure** | M | M | `process.stdin` writes synchronously; a slow child can block the caller. Add a bounded async writer with a "queue full" backpressure signal (the reference exposes a `stdin backpressure: queue full` guard). No wire change. |
| 10 | ~~**Fuzz the JSON-RPC parser**~~ ✅ **done** | M | L-M | Shipped `fuzz_test.go`: `FuzzDispatch` (parse→auth→version→route→param-presence, side-effectful methods skipped so a coverage-guided fuzzer can't drive spawn/extract_tar/read) + `FuzzBindParams` (param-type binding, pure). Seeds run in CI; ~1.5M execs clean under active `-fuzz`. _Optional follow-up:_ a short `-fuzztime` CI job for ongoing fuzzing. |
| 11 | ~~**Release automation**~~ ✅ **done** | H | M | Shipped `.goreleaser.yaml` + `release.yml`: 6-target builds, checksums, syft CycloneDX SBOM, cosign signing, and SLSA `*.intoto.jsonl` provenance — satisfies Scorecard SBOM + Signed-Releases (10/10). Also shipped `release-please.yml` + `pr-auto-update.yml` for automated version PRs (claustrum-ci[bot]). |
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

## Deliberate divergences (post-parity, opt-in)

Unlike everything above, these **knowingly change a frame/behavior** from the
reference. They follow the "match upstream first, then improve" plan: only
consider them now that the harness proves parity, and document each as an
*intentional* divergence in [`PROTOCOL.md`](PROTOCOL.md) + the PR if adopted.

| # | Improvement | Impact | Cost | Why / divergence |
|---|---|---|---|---|
| D1 | **Re-harden `-cli-zst` checksum** | M | L | The reference verifies `-cli-checksum` only on the `-cli-url` download path, **not** on the local `-cli-zst` (SFTP) blob — so PR #29 dropped our verification there to stay 1:1 (maintainer-approved baseline). A future hardening could re-add SHA-256 verification on the `-cli-zst` path when a checksum is supplied (or reject `-cli-checksum`+`-cli-zst` together rather than silently ignoring it). **Divergence:** changes the `cliError` for a mismatched local blob from `decompressing: …` back to `checksum mismatch: …`. Maintainer flagged this to revisit. |

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods.

Any of these would need a deliberate, documented protocol version bump.
