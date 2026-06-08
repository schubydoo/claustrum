# Changelog

All notable changes to claustrum are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

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
  - Daemon stderr logging matched to the reference (`log`-package timestamps).

### Changed
- `go.mod` toolchain `go 1.23` → `go 1.24`; `klauspost/compress` → `v1.18.6`.
- Test + mutation hardening: statement coverage ~70% → ~79% (CI floor → 75%);
  mutation efficacy to its practical maximum (96.1% — the residual surviving
  mutants are provably equivalent/inherent).

[Unreleased]: https://github.com/schubydoo/claustrum/commits/main
