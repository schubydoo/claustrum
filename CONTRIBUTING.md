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
go build ./...            # Go 1.23+; only dep is github.com/klauspost/compress
make build               # -> ./claustrum
```

## Before opening a PR

- **Build all targets** — `make all` must cross-compile cleanly for all six
  platforms (linux/darwin/windows × amd64/arm64). OS-specific code lives in
  `*_unix.go` / `*_windows.go`; keep the JSON-RPC surface identical across them.
- **Format + vet** — `gofmt -l .` must be empty and `go vet ./...` clean.
- **Tests** — `go test -race ./...`. Unit tests for the wire surface (frame
  encoding, dispatch/error routing, the replay buffer, env merging) live in
  `*_test.go`; the full cross-binary validation battery that diffs frames against
  the reference daemon lives in `scratch/` (gitignored — see Compatibility below).
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
  stdlib + one zstd library, `CGO_ENABLED=0`.
- **No telemetry, ever.**
- Keep host-specific or reverse-engineering working notes out of the repo (the
  `scratch/` tree is gitignored on purpose).

## License

By contributing, you agree that your contributions are licensed under the
project's [Apache-2.0 License](LICENSE).
