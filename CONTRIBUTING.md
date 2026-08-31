# Contributing

Thanks for your interest in claustrum!

claustrum is a small, dependency-light Go daemon with one hard rule: **stay
behaviorally compatible** with the protocol it implements. Changes that alter the
JSON-RPC surface, error codes, or frame shapes must keep the validation battery
green (see below).

## Dev setup

```sh
git clone https://github.com/schubydoo/claustrum
cd claustrum
go build ./...            # Go 1.25+, held below 1.27 (see docs/UPSTREAM-TRACKING.md); deps: klauspost/compress, golang.org/x/sys + Microsoft/go-winio (both Windows-only)
make build               # -> ./claustrum
make hooks               # one-time: install the pre-commit hook (see below)
```

`make hooks` points `core.hooksPath` at the tracked `.githooks/` dir, so a
zero-dependency `pre-commit` hook runs the same fast checks CI gates on —
`gofmt`, `go vet`, `go mod tidy` cleanliness, `golangci-lint` if it's installed,
and a changeset-shape check when you stage a `.changeset/` fragment — before each
commit. It needs no external tooling (no Python `pre-commit` framework); bypass
it for an in-progress commit with `git commit --no-verify`.

## Before opening a PR

- **Build all targets** — `make all` must cross-compile cleanly for all six
  platforms (linux/darwin/windows × amd64/arm64). OS-specific code lives in
  `*_unix.go` / `*_windows.go`; keep the JSON-RPC surface identical across them.
- **Format, vet, lint** — `gofmt -l .` empty, `go vet ./...` clean, and
  `golangci-lint run ./...` clean (config in `.golangci.yml`).
- **Tests** — `go test -race ./...`. The in-repo suite (`*_test.go`) covers the
  wire surface two ways: unit tests (frame encoding, dispatch/auth/error routing,
  the replay buffer, env merging, the `-install` pipeline) **and** a
  socket-integration suite that boots the daemon and asserts every method's frames
  against golden fixtures in `testdata/` — CI gates this on every PR. The
  cross-binary validation battery that diffs frames against the reference daemon
  lives in `scratch/` (gitignored — see Compatibility below).

  **Add tests for new or changed behavior in the same PR.** CI enforces this with
  a 95% statement-coverage floor (`coverage` job in `.github/workflows/ci.yml`) —
  the suite currently sits near 98%, so an untested change shows up as a drop.
- **Compatibility** — if you touch the wire surface (`rpc.go`, `methods_*.go`,
  `process.go`, `results.go`), re-run the validation battery in `scratch/` and
  confirm frames stay **byte-identical**. A change that intentionally diverges
  must say so in the PR, add an entry to the divergence catalog in
  [docs/DIVERGENCES.md](docs/DIVERGENCES.md), and record its wire frames in
  [docs/PROTOCOL.md](docs/PROTOCOL.md).
- **Docs** — update `docs/` for any user-visible behavior change. The site is
  built with mkdocs-material and published to GitHub Pages; CI runs
  `mkdocs build --strict` on every docs change (a broken link or bad nav fails
  the check). To preview locally:

  ```sh
  python3 -m venv .venv
  .venv/bin/pip install -r docs/requirements.txt
  .venv/bin/mkdocs serve            # live preview at http://127.0.0.1:8000
  .venv/bin/mkdocs build --strict   # the exact check CI runs
  ```

  `docs/requirements.txt` is a **hash-pinned lock** compiled from
  `docs/requirements.in` — don't hand-edit it. To change the docs toolchain, edit
  the `.in` and regenerate (Renovate does this automatically for version bumps):

  ```sh
  pip install pip-tools
  pip-compile --generate-hashes --strip-extras \
    --output-file=docs/requirements.txt docs/requirements.in
  ```
- **Conventional Commits** — PR **titles** follow
  [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `chore:`, `ci:`, …). PRs are squash-merged, so the
  title becomes the commit subject. Titles are for a clean history only — they do
  **not** drive releases (see [Changesets](#changesets)).
- **Changeset** — user-facing PRs add a `.changeset/*.md` fragment; internal-only
  PRs don't (see [Changesets](#changesets)).

## Changesets

Releases are **changesets-only** (knope): `.changeset/*.md` fragments — not commit
messages — drive both the version bump and the CHANGELOG (`knope.toml` sets
`ignore_conventional_commits = true`).

**A changeset body must be a single line.** knope takes the first line as the
entry summary; any further content — a second line, or a blank-line-separated
paragraph such as an "Upgrade note" — makes it render as a `####` heading block
instead of a bullet, and a lone heading among bullets is what breaks the
changelog. This already shipped twice, in the 1.7.2 and 1.7.3 release notes. Fold
every detail into that one sentence. `scripts/lint_changesets.py` enforces it, in
CI and in the `make hooks` pre-commit hook.

On a user-facing PR, add a fragment — run `knope document-change`, or create
`.changeset/<short-slug>.md`:

```markdown
---
default: minor
---

Short, imperative summary of the change.
```

`default:` sets the version bump and the changelog section. Three values set the
bump: `major` → Breaking changes, `minor` → Features, `patch` → Fixes. Three custom
types set only the changelog section (the bump stays `patch`): `perf` → Performance,
`build` → Build System & Dependencies, `revert` → Reverts. The PR number is appended to each entry
automatically at release time.

**When you don't need one:** internal-only PRs — CI, workflows, `scripts/` tooling,
refactors, tests, docs. The advisory `changeset-check` workflow nudges when a PR
changes Go source without a fragment; apply the **`no-changelog`** label to
acknowledge an intentional omission.

**How a release happens:** on push to `main`, `knope-prepare.yml` consumes pending
fragments and opens a `chore: prepare release X.Y.Z` PR (bumps `VERSION` +
`CHANGELOG.md`, and stamps `buildstamp.go` — see below); merging it tags `vX.Y.Z`
and creates the GitHub Release, which triggers the signed `release.yml` build
(goreleaser). Merging the release PR is the human approval gate.

**`buildstamp.go` is generated — don't edit it by hand.** `scripts/write_build_stamp.py`
rewrites its two consts during `prepare-release` so `go install pkg@vX.Y.Z` builds
(which compile from the module cache and carry no `vcs.*` metadata or `-ldflags`)
can still report their release version and time. See `buildstamp.go` for the full
rationale.

## Scope notes

- **No new dependencies** without discussion — the binary is deliberately
  stdlib + zstd (`klauspost/compress`) + `golang.org/x/sys` and
  `github.com/Microsoft/go-winio` (both Windows-only), `CGO_ENABLED=0`.
- **No telemetry, ever.**
- Keep host-specific or reverse-engineering working notes out of the repo (the
  `scratch/` tree is gitignored on purpose).

## License

By contributing, you agree that your contributions are licensed under the
project's [Apache-2.0 License](LICENSE).
