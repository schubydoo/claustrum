# Contributing

Thanks for your interest in claustrum!

claustrum is a small, dependency-light Go daemon with one hard rule. It must stay
behaviorally compatible with the protocol it implements. A change that alters the
JSON-RPC surface, the error codes, or the frame shapes must keep the validation
battery green. The battery is described below.

## Dev setup

```sh
git clone https://github.com/schubydoo/claustrum
cd claustrum
go build ./...            # Go 1.25+, held below 1.27 (see docs/UPSTREAM-TRACKING.md); deps: klauspost/compress, golang.org/x/sys + Microsoft/go-winio (both Windows-only)
make build               # -> ./claustrum
make hooks               # one-time: install the pre-commit hook (see below)
```

`make hooks` points `core.hooksPath` at the tracked `.githooks/` dir. A
zero-dependency `pre-commit` hook then runs before each commit. It runs the same
fast gates that CI gates on: `gofmt`, `go vet`, and `go mod tidy` cleanliness. If
`golangci-lint` is installed, the hook runs it too. If you stage a `.changeset/`
fragment, the hook also tests the shape of that fragment. The hook needs no
external tooling, and no Python `pre-commit` framework. To bypass it for an
in-progress commit, run `git commit --no-verify`.

## Before opening a PR

- Build all targets. `make all` must cross-compile cleanly for all six
  platforms: linux, darwin, and windows, on amd64 and arm64. OS-specific code
  lives in `*_unix.go` and `*_windows.go`. Keep the JSON-RPC surface identical
  across them.
- Format, vet, and lint. `gofmt -l .` must print nothing. `go vet ./...` must be
  clean. `golangci-lint run ./...` must be clean. Its configuration is in
  `.golangci.yml`.
- Tests. Run `go test -race ./...`. The in-repo suite (`*_test.go`) covers the
  wire surface two ways. The unit tests cover frame encoding, dispatch, auth and
  error routing, the replay buffer, env merging, and the `-install` pipeline. The
  socket-integration suite boots the daemon and asserts every method's frames
  against golden fixtures in `testdata/`. CI gates the in-repo suite on every PR. The
  cross-binary validation battery diffs frames against the reference daemon. It
  lives in the gitignored `scratch/` tree. See Compatibility below.

  Add tests for new or changed behavior in the same PR. CI enforces this with
  a 95% statement-coverage floor. The `coverage` job in
  `.github/workflows/ci.yml` holds that floor. The suite currently sits near
  99%, so an untested change shows up as a drop.
- Compatibility. If you touch the wire surface (`rpc.go`, `methods_*.go`,
  `process.go`, `results.go`), re-run the validation battery in `scratch/`.
  Make sure that the frames stay byte-identical. A change that diverges on
  purpose must say so in the PR. It must add an entry to the divergence catalog
  in [docs/DIVERGENCES.md](docs/DIVERGENCES.md). It must record its wire frames
  in [docs/PROTOCOL.md](docs/PROTOCOL.md).
- Docs. Update `docs/` for any user-visible behavior change. The site is
  built with mkdocs-material and published to GitHub Pages. CI runs
  `mkdocs build --strict` on every docs change. A broken link or a bad nav entry
  fails that job. To preview the site on your own machine, run these commands:

  ```sh
  python3 -m venv .venv
  .venv/bin/pip install -r docs/requirements.txt
  .venv/bin/mkdocs serve            # live preview at http://127.0.0.1:8000
  .venv/bin/mkdocs build --strict   # the exact check CI runs
  ```

  `docs/requirements.txt` is a hash-pinned lock file, compiled from
  `docs/requirements.in`. Do not edit it by hand. To change the docs toolchain,
  edit the `.in` file and regenerate the lock file. Renovate does this
  regeneration for version bumps:

  ```sh
  pip install uv
  uv pip compile --generate-hashes docs/requirements.in --output-file=docs/requirements.txt
  ```
- Conventional Commits. PR titles follow
  [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`,
  `docs:`, `chore:`, `ci:`, and others. PRs are squash-merged, so the
  title becomes the commit subject. Titles are for a clean history only. They do
  not drive releases. See [Changesets](#changesets).
- Changeset. A user-facing PR adds a `.changeset/*.md` fragment. An
  internal-only PR does not. See [Changesets](#changesets).

## Changesets

Releases are changesets-only, through knope. The `.changeset/*.md` fragments
drive both the version bump and the CHANGELOG. Commit messages do not.
`knope.toml` sets `ignore_conventional_commits = true`.

A changeset body must be a single line. knope takes the first line as the
entry summary. Any further content makes the entry render as a `####` heading
block instead of a bullet. Such content is a second line, or a paragraph after a
blank line, such as an "Upgrade note". A lone heading among bullets is what
breaks the changelog. This already shipped twice, in the 1.7.2 and 1.7.3 release
notes. Fold every detail into that one line. Several sentences on it are fine. `scripts/lint_changesets.py`
enforces the rule, in CI and in the `make hooks` pre-commit hook.

On a user-facing PR, add a fragment. Run `knope document-change`, or create
`.changeset/<short-slug>.md`:

```markdown
---
default: minor
---

Short, imperative summary of the change.
```

`default:` sets the version bump and the changelog section. Three values set the
bump: `major` → Breaking changes, `minor` → Features, `patch` → Fixes. Three custom
types set only the changelog section, and the bump stays `patch`: `perf` → Performance,
`build` → Build System & Dependencies, `revert` → Reverts. At release time, the PR
number is appended to each entry automatically.

You do not need a fragment for an internal-only PR. Internal-only means CI,
workflows, `scripts/` tooling, refactors, tests, and docs. If a PR changes Go
source without a fragment, the advisory `changeset-check` workflow nudges you.
Apply the `no-changelog` label to acknowledge an intentional omission.

This is how a release happens. On a push to `main`, `knope-prepare.yml` consumes
the pending fragments. It opens a `chore: prepare release X.Y.Z` PR. That PR
bumps `VERSION` and `CHANGELOG.md`, and it stamps `buildstamp.go`. The stamping
is described below. Merging that PR tags `vX.Y.Z` and creates the GitHub Release.
The tag push triggers the signed `release.yml` build, through goreleaser.
Merging the release PR is the human approval gate.

`buildstamp.go` is generated. Do not edit it by hand. `scripts/write_build_stamp.py`
rewrites its two consts during `prepare-release`. A `go install pkg@vX.Y.Z` build
compiles from the module cache, and it carries no `vcs.*` metadata and no
`-ldflags`. The rewritten consts let such a build report its release version and
time. See `buildstamp.go` for the full rationale.

## Scope notes

- Add no new dependency without discussion. The binary is deliberately stdlib,
  plus zstd (`klauspost/compress`), plus `golang.org/x/sys` and
  `github.com/Microsoft/go-winio`. Those last two are Windows-only. The build
  uses `CGO_ENABLED=0`.
- No telemetry, ever.
- Keep host-specific working notes and reverse-engineering working notes out of
  the repo. The `scratch/` tree is gitignored on purpose.

## License

By contributing, you agree that your contributions are licensed under the
project's [Apache-2.0 License](LICENSE).
