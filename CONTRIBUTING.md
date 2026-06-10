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
go build ./...            # Go 1.24+; deps: klauspost/compress + golang.org/x/sys (Windows-only)
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
- **Docs** — update `docs/` for any user-visible behavior change.
- **Conventional Commits** — PR **titles** follow
  [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `chore:`, `ci:`, …). PRs are squash-merged, so the
  title becomes the commit subject.

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
