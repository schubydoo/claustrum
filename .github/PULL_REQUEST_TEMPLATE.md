<!--
PR titles use Conventional Commits (feat: / fix: / docs: / chore: / ci: …) —
the title becomes the squash-merge commit subject. Titles don't drive releases:
a user-facing change needs a `.changeset/*.md` fragment (`knope document-change`);
internal-only PRs apply the `no-changelog` label instead. See CONTRIBUTING.md.
-->

## What & why

<!-- A sentence or two on the change and the motivation. -->

## Wire surface

<!-- The one hard rule: stay byte-identical to the reference daemon's JSON-RPC
     frames. Tick the box that applies. -->

- [ ] **No wire-surface change** — does not touch `rpc.go`, `methods_*.go`,
      `process.go`, or `results.go`.
- [ ] **Intentional protocol change** — documented in
      [`docs/PROTOCOL.md`](../docs/PROTOCOL.md) and described below.

## Checklist

- [ ] `gofmt -l .` is empty and `go vet ./...` is clean
- [ ] `go test -race ./...` passes
- [ ] `make all` cross-compiles all six targets
- [ ] If the wire surface changed: re-ran the `scratch/` validation battery and
      frames stay byte-identical (or the divergence is documented above)
- [ ] No new dependencies / no telemetry
