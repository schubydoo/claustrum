# Changelog

All notable changes to claustrum are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

## 1.0.0 (2026-06-08)


### Features

* clean-room claustrum daemon (18-method JSON-RPC over AF_UNIX) ([8cd3ceb](https://github.com/schubydoo/claustrum/commit/8cd3ceb8388846fe8f1cc3f30e1af834ce863b12))
* match the reference daemon's operational stderr logging ([#17](https://github.com/schubydoo/claustrum/issues/17)) ([693ca38](https://github.com/schubydoo/claustrum/commit/693ca385a6bbfafcba22de5f0ef91ea780db9c94))


### Bug Fixes

* -serve requires -token-file + defaults the socket ([#34](https://github.com/schubydoo/claustrum/issues/34)) ([a897794](https://github.com/schubydoo/claustrum/commit/a8977949c45f010c15b1423d90226e11ff4b70b9))
* **ci:** correct gremlins JSON keys in the mutation summary ([#11](https://github.com/schubydoo/claustrum/issues/11)) ([926ba7f](https://github.com/schubydoo/claustrum/commit/926ba7fe6083aadc2882dabadfe4c02ee01cc782))
* **ci:** run mutation testing on stable Go ([#9](https://github.com/schubydoo/claustrum/issues/9)) ([5e05634](https://github.com/schubydoo/claustrum/commit/5e0563480cc189dd7124da71fd364e0c01482745))
* default socket + best-effort -stop + -bridge dial framing ([#28](https://github.com/schubydoo/claustrum/issues/28)) ([0166c5e](https://github.com/schubydoo/claustrum/commit/0166c5e35d877f1ac652e2bd1eeffd5ea962dcd7))
* git.info branch resolution for unborn/detached HEAD ([#33](https://github.com/schubydoo/claustrum/issues/33)) ([2892446](https://github.com/schubydoo/claustrum/commit/28924466c7e4604da2c4ce50a9729b08ce837ad9))
* match reference -install checksum + error framing ([#29](https://github.com/schubydoo/claustrum/issues/29)) ([c3b9d17](https://github.com/schubydoo/claustrum/commit/c3b9d176db5a8c1ce74776c0eebaa52c8dbac118))
* match reference extract_tar side effects (destDir wipe, modes, .synced, archive consume) ([#15](https://github.com/schubydoo/claustrum/issues/15)) ([896fd5c](https://github.com/schubydoo/claustrum/commit/896fd5c93dc7cbf41113f44fd87a6083c29f39e8))
* match reference files.list symlink resolution and extract_tar entry-type rejection ([#16](https://github.com/schubydoo/claustrum/issues/16)) ([5a863d2](https://github.com/schubydoo/claustrum/commit/5a863d23c526fac196efa854d7b7a20aaff844e0))
* match reference process.stdin check order and exited-process error ([#36](https://github.com/schubydoo/claustrum/issues/36)) ([4357b34](https://github.com/schubydoo/claustrum/commit/4357b34c354684c7e052c84121cd4bbb34da088f))
* match reference request-size cap (1 MiB) and git non-repo result shapes ([#18](https://github.com/schubydoo/claustrum/issues/18)) ([ef3983f](https://github.com/schubydoo/claustrum/commit/ef3983fdbf08f9cd6340ada4f5020534d4815281))
* reject mistyped params and check auth before version ([#22](https://github.com/schubydoo/claustrum/issues/22)) ([f5dbd85](https://github.com/schubydoo/claustrum/commit/f5dbd85e30782d259ce7b2028e31a968a83abcf2))
* reject zip-slip paths in files.extract_tar (CodeQL go/zipslip) ([#14](https://github.com/schubydoo/claustrum/issues/14)) ([19adf3b](https://github.com/schubydoo/claustrum/commit/19adf3b0f3d5698eb8dc2ca015b872c5ab432ffb))
* strip trailing newline from -serve token file to match reference ([#37](https://github.com/schubydoo/claustrum/issues/37)) ([d906812](https://github.com/schubydoo/claustrum/commit/d906812f42859aaf51d0721722e948bbd6eabad6))

## [Unreleased]

### Added
- Initial clean-room daemon: JSON-RPC 2.0 over `AF_UNIX`, 18 methods
  (`server.*`, `files.*`, `git.*`, `process.*`), in-band token auth, per-process
  replay buffer with `reattach`.
- Modes: `-serve` (self-daemonizing RPC server), `-bridge` (stdio↔socket relay),
  `-stop`, `-version`, `-install` (CLI download/verify/extract/prune).
- Cross-platform builds for linux/darwin/windows × amd64/arm64 (`make all`).
- Docs: `README`, `docs/PROTOCOL.md`, `docs/ARCHITECTURE.md`, `docs/EXAMPLES.md`,
  `docs/IMPROVEMENTS.md`, `docs/UPSTREAM-TRACKING.md`.
- `scripts/check-upstream.sh` — drift detection against a reference build SHA.
- Unit tests (`*_test.go`) covering the wire surface: byte-exact frame encoding,
  JSON-RPC dispatch/auth/error routing, the per-process replay buffer, and env
  merging — run under the race detector in CI.
- Socket integration suite (`harness_test.go`, `integration_test.go`,
  `integration_fs_git_test.go`): boots the daemon on a temp `AF_UNIX` socket and
  drives all 18 methods over the real read-loop/auth/framing/stream-fanout path,
  with committed golden fixtures (`testdata/socket_*.golden.json`, regenerate via
  `go test -run Socket -update`) locking the response/error envelopes and the
  `files.*`/`git.*` results against regression.
- Broadened unit coverage: the `-install` pipeline (zstd decompress, checksum,
  prune, runnable check, facts JSON — network-free via local `.zst`/httptest),
  the `-bridge`/`-stop` clients, version resolution, and signal parsing. Total
  statement coverage ~70% (CI floor raised 50% → 65%).
- Fuzz targets (`fuzz_test.go`): `FuzzDispatch` (the parse/auth/version/route/
  param-presence surface, with side-effectful methods skipped) and
  `FuzzBindParams` (param-type binding). Seed corpora run in CI; ~1.5M executions
  clean under active `-fuzz`.
- Repository governance config (mirrors the sibling project, adapted for a Go
  daemon): declarative `.github/repo-config/` baselines (settings + labels) with
  an advisory, read-only `repo-config-drift` workflow; branch/tag protection
  `.github/rulesets/` (squash-only PRs, linear history, required checks,
  immutable `v*` tags); `CODEOWNERS` and a pull-request template.
- Issue templates (`.github/ISSUE_TEMPLATE/`): structured bug-report and
  feature-request forms (the latter flags JSON-RPC wire-surface impact), with
  blank issues disabled and a private security-advisory contact link.
- Mutation testing (`.github/workflows/mutation.yml`): on-demand + weekly
  gremlins run (advisory, report-only) auditing whether the tests actually
  assert on behavior; pinned `GREMLINS_VERSION` tracked by Renovate.
- Release automation (`.goreleaser.yaml` + `.github/workflows/release.yml`): on
  a `v*` tag, builds all 6 targets, archives + `checksums.txt`, a syft CycloneDX
  **SBOM** per archive, **cosign** keyless signatures (`.sigstore.json`), and a
  **SLSA provenance** attestation (`*.intoto.jsonl`) via slsa-github-generator —
  satisfying OpenSSF Scorecard's SBOM (full) and Signed-Releases (10/10) checks.
  Version/build-time are injected into the binary. `.github/zizmor.yml` allows
  the SLSA reusable workflow's required tag ref under an otherwise hash-pin-only
  policy.
- `golangci-lint` (`.golangci.yml`, v2) wired into the CI `lint` job — the
  standard set (errcheck, govet, ineffassign, staticcheck, unused) plus misspell
  and unconvert. Cleared its findings: removed an unused method and made a few
  intentionally-ignored errors explicit; `Close()` is excluded from errcheck.
- `renovate.json` — automated dependency updates (Go modules + SHA-pinned
  GitHub Actions, keeping the pins via `helpers:pinGitHubActionDigestsToSemver`),
  grouped minor/patch PRs with Conventional-Commits titles, `gomodTidy`
  post-update, weekly schedule, and a dependency dashboard.
- Expanded CI: `ci.yml` split into `lint` (gofmt + vet + `go mod tidy` clean),
  a `test` matrix (ubuntu + macos, `-race`), 6-target cross-build, and a 50%
  `coverage` floor, gated by a single `ci required checks passed` aggregator.
  New `security.yml` (CodeQL, gitleaks, trivy-fs, zizmor workflow-audit,
  dependency-review), `pr-title.yml` (Conventional-Commits check), and
  `scorecard.yml` (OSSF Scorecard). All actions SHA-pinned. The required checks
  are now the CI + security aggregators and the conventional-title check.
- Apache-2.0 license; independence & trademark `NOTICE`.

### Fixed
- Reference-parity corrections found by out-of-band differential probing —
  filesystem side effects, adversarial inputs, and CLI-mode wrappers, none of
  which the JSON-RPC frame battery alone could see:
  - `files.extract_tar` side effects (destDir wipe, fixed `0600`/`0700` modes,
    `.synced` marker, archive consumption); `files.list` follows symlinks for
    `isDir`; a 1 MiB request-line cap; `git.*` non-repo response shapes.
  - Dispatch precedence is **auth → version** (a request failing both reports
    `-32001`, not `-32600`); present-but-**mistyped params** are rejected
    `-32602 Invalid params`; a method with no namespace is `-32601 Invalid
    method format`.
  - CLI modes: `-bridge`/`-stop` default to `~/.claude/remote/rpc.sock`; `-stop`
    is best-effort (silent exit 0); `-bridge` dial errors are wrapped
    `dial server: …`; a no-mode invocation prints only the one-line error (no
    `flag.Usage()` dump).
  - `-install` verifies `-cli-checksum` only on `-cli-url` downloads (and there
    unconditionally — an empty checksum still fails); the local `-cli-zst` blob
    is not verified (see `docs/IMPROVEMENTS.md` D1); input/decompress failures
    are wrapped `opening input: …` / `decompressing: …`.
  - `-serve` requires `-token-file` (checked before the socket; the
    `CLAUDE_RPC_TOKEN` env is no longer accepted there), defaults `-socket` to
    `~/.claude/remote/rpc.sock`, and prefixes its errors `claustrum:` with
    `listen unix: …` / `read --token-file: …` wraps. This also closes a gap where
    `-serve -socket S` with no token started an **unauthenticated** (empty-token)
    daemon — it is now refused.
  - `git.info` resolves the branch via `symbolic-ref`, so an unborn HEAD (empty
    repo) reports the init branch and a detached HEAD reports
    `detached:<short-sha>` (instead of leaking git's error text or `HEAD`);
    `git.worktree_create` off an empty repo infers an orphan branch and succeeds.
  - `process.stdin` runs its checks in the reference's order **decode → exists →
    running**: invalid base64 is rejected `-32602 Invalid base64 data` *before*
    the process lookup (an unknown id with a bad payload still reports the decode
    error, not `Process not found`), and writing to a known-but-**exited**
    process now returns `-32602 Process not running` instead of a false
    `{"success":true}`. (The 32 KiB stream-frame cap and the `exitCode:-1`
    signal-death code were probe-confirmed to already match.)
  - `-serve` reads the `-token-file` as a **line**: a single trailing newline
    (`\n`/`\r\n`) is stripped (spaces and surrounding whitespace preserved),
    matching the reference. Previously the raw file bytes were used verbatim, so
    an uploaded token file ending in a newline made **every** client request fail
    auth even with the correct token — a drop-in blocker found by auditing the
    real deployment invocation (`scratch/probe/contract_probe.sh`).
  - Daemon stderr logging matched to the reference (`log`-package timestamps).

### Security
- `-serve` no longer starts an unauthenticated daemon when invoked without a
  token: `--token-file` is mandatory and is the sole token source (read once,
  then unlinked).

### Changed
- `go.mod` toolchain `go 1.23` → `go 1.24`; `klauspost/compress` → `v1.18.6`.
- Test + mutation hardening: statement coverage ~70% → ~79% (CI floor → 75%);
  mutation efficacy to its practical maximum (96.1% — the residual surviving
  mutants are provably equivalent/inherent).

[Unreleased]: https://github.com/schubydoo/claustrum/commits/main
