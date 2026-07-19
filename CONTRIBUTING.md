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
go build ./...            # Go 1.25+; deps: klauspost/compress + golang.org/x/sys (Windows-only)
make build               # -> ./claustrum
make hooks               # one-time: install the pre-commit hook (see below)
```

`make hooks` points `core.hooksPath` at the tracked `.githooks/` dir, so a
zero-dependency `pre-commit` hook runs the same fast checks CI gates on —
`gofmt`, `go vet`, `go mod tidy` cleanliness, and `golangci-lint` if it's
installed — before each commit. It needs no external tooling (no Python
`pre-commit` framework); bypass it for an in-progress commit with
`git commit --no-verify`.

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
- **Compatibility** — if you touch the wire surface (`rpc.go`, `methods_*.go`,
  `process.go`, `results.go`), re-run the validation battery in `scratch/` and
  confirm frames stay **byte-identical**. A change that intentionally diverges
  must say so in the PR and update [docs/PROTOCOL.md](docs/PROTOCOL.md).
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
- **Changeset** — if your PR is user-facing, add a `.changeset/*.md` fragment
  (see [Changesets](#changesets)). No fragment ⇒ no changelog entry and no version
  bump. Internal-only PRs (CI, tooling, refactor, tests) don't need one.

## Changesets

Releases are **changesets-only** (knope): the `.changeset/*.md` fragments — not
commit messages — drive both the version bump and the CHANGELOG
(`knope.toml` sets `ignore_conventional_commits = true`).

On a user-facing PR, add a fragment — run `knope document-change`, or create
`.changeset/<short-slug>.md`:

```markdown
---
default: minor
---

Short, imperative summary of the change.
```

`default:` sets the bump and changelog section: `major` → Breaking changes,
`minor` → Features, `patch` → Fixes, `perf` → Performance, `build` → Build System
& Dependencies, `revert` → Reverts. The PR number is appended to each entry
automatically at release time.

**When you don't need one:** internal-only PRs — CI, workflows, `scripts/` tooling,
refactors, tests, docs. The advisory `changeset-check` workflow nudges when a PR
changes Go source without a fragment; apply the **`no-changelog`** label to
acknowledge an intentional omission.

**How a release happens:** on push to `main`, `knope-prepare.yml` consumes pending
fragments and opens a `chore: prepare release X.Y.Z` PR (bumps `VERSION` +
`CHANGELOG.md`); merging it tags `vX.Y.Z` and creates the GitHub Release, which
triggers the signed `release.yml` build (goreleaser). Merging the release PR is the
human approval gate.

## Scope notes

- **No new dependencies** without discussion — the binary is deliberately
  stdlib + zstd (`klauspost/compress`) + `golang.org/x/sys` (Windows-only),
  `CGO_ENABLED=0`.
- **No telemetry, ever.**
- Keep host-specific or reverse-engineering working notes out of the repo (the
  `scratch/` tree is gitignored on purpose).

## License

By contributing, you agree that your contributions are licensed under the
project's [Apache-2.0 License](LICENSE).
