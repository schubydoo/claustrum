<!--
PR titles use Conventional Commits (feat: / fix: / docs: / chore: / ci: …).
The title becomes the squash-merge commit subject. Titles don't drive releases:
a user-facing change needs a `.changeset/*.md` fragment (`knope document-change`);
internal-only PRs apply the `no-changelog` label instead. See CONTRIBUTING.md.
-->

## What & why

<!-- A sentence or two on the change and the motivation. -->

## Wire surface

<!-- The one hard rule: stay byte-identical to the reference daemon's JSON-RPC
     frames. Tick the box that applies. -->

- [ ] No wire-surface change. It does not touch `rpc.go`, `methods_*.go`,
      `process.go`, or `results.go`.
- [ ] Intentional divergence. It has an entry in
      [`docs/DIVERGENCES.md`](../docs/DIVERGENCES.md), and its frames are in
      [`docs/PROTOCOL.md`](../docs/PROTOCOL.md) or in the page of its method
      under `docs/protocol/`. It is described below.

## Checklist

- [ ] `gofmt -l .` is empty, and `go vet ./...` and `golangci-lint run` are clean
- [ ] `make test` passes
- [ ] `make all` cross-compiles all six targets
- [ ] If the wire surface changed: a golden fixture (`testdata/socket_*.golden.json`)
      changed only for a new reference frame or a divergence documented above
- [ ] No new dependencies / no telemetry
