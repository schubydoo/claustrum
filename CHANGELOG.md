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
- Repository governance config (mirrors the sibling project, adapted for a Go
  daemon): declarative `.github/repo-config/` baselines (settings + labels) with
  an advisory, read-only `repo-config-drift` workflow; branch/tag protection
  `.github/rulesets/` (squash-only PRs, linear history, required checks,
  immutable `v*` tags); `CODEOWNERS` and a pull-request template.
- Expanded CI: `ci.yml` split into `lint` (gofmt + vet + `go mod tidy` clean),
  a `test` matrix (ubuntu + macos, `-race`), 6-target cross-build, and a 50%
  `coverage` floor, gated by a single `ci required checks passed` aggregator.
  New `security.yml` (CodeQL, gitleaks, trivy-fs, zizmor workflow-audit,
  dependency-review), `pr-title.yml` (Conventional-Commits check), and
  `scorecard.yml` (OSSF Scorecard). All actions SHA-pinned. The required checks
  are now the CI + security aggregators and the conventional-title check.
- Apache-2.0 license; independence & trademark `NOTICE`.

[Unreleased]: https://github.com/schubydoo/claustrum/commits/main
