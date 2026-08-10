# Reference build ledger

Every claustrum release tracks a specific reference `claude-ssh` daemon build,
identified by git SHA (see [Upstream tracking](UPSTREAM-TRACKING.md) for how new
builds are discovered and diffed). This page is the running history of those
builds and what each one changed on the wire, so the reconciliation trail lives
in one place instead of scattered across commit messages.

The currently pinned build lives in
[`scripts/UPSTREAM_SHA`](https://github.com/schubydoo/claustrum/blob/main/scripts/UPSTREAM_SHA);
this ledger is the narrative behind the bumps.

## Builds

Newest first. "Wire change" means a change to the JSON-RPC surface claustrum must
match byte-for-byte; a pure rebuild with no wire change still gets a row, so a
re-published SHA is distinguishable from a real release.

| Reference SHA | Built (UTC) | Wire changes | Reconciled in |
|---|---|---|---|
| `5db5e4a1…` | 2026-07-06 | none (off-wire: `daemon.token` persistence) | [PR 131](https://github.com/schubydoo/claustrum/pull/131) |
| `7c2f88d1…` | 2026-07-02 | 5 changes — see below | [PR 120](https://github.com/schubydoo/claustrum/pull/120) |
| `d20a77da…` | 2026-06-09 | none (pure rebuild) | [PR 97](https://github.com/schubydoo/claustrum/pull/97) (pin bump only) |
| `7cbfa471…` | 2026-06-04 | `git.info` gained `root` (+ off-wire churn — see below) | [PR 60](https://github.com/schubydoo/claustrum/pull/60) |
| `8de85faa…` | 2026-05-21 | baseline (initial clean-room target) | initial implementation |

Each per-build section below uses the same three parts: the **wire delta** (what
claustrum must match byte-for-byte), any **off-wire churn** (source that moved but
never reaches the JSON-RPC surface), and **how it was bounded** (the measurement
that confirmed nothing else changed).

### `5db5e4a12f88487e47c2c48259b69a2d630bb3f7` — 2026-07-06

**Wire delta.** None.

**Off-wire churn.** The daemon now persists its auth token to `daemon.token`
(mode `0600`) in the socket's directory — written atomically via an
`os.CreateTemp("daemon.token-*")` + rename at startup, unlinked on graceful
shutdown — so a client can reconnect to an already-running daemon and
re-authenticate after the original `-token-file` was unlinked / the `-token-fd`
pipe closed. Matched here in `tokenpersist.go` (wired through `runServe` /
`teardown`); the wire contract is in
[PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken).

**How it was bounded.** The frame battery plus differential binary analysis
against `7c2f88d` converge on token persistence and nothing else — no other
method or phase changed; forensics parked outside the committed tree.

### `7c2f88d13e5f269762dd4d463aa4eb3102214110` — 2026-07-02

**Wire delta.** Method count **18 → 19**; five changes, all matched byte-for-byte
(values and timing) against the reference:

1. **`process.killAndWait`** — new method, between `process.kill` and
   `process.reattach`. Blocks until the process is gone, then reports the outcome
   as a result. Params `timeoutMs` (grace) and `escalate`; result
   `{found,died[,alreadyExited][,escalated]}`. Defaults, the grace clamp and the
   escalation timing are canonical in
   [PROTOCOL.md → process.killAndWait](PROTOCOL.md#processkillandwait).
2. **`process.stdin.offset` contract** — `process.stdin` replies now always carry
   `applied` (cumulative decoded bytes); an `offset` param makes stdin idempotent
   across reconnects (duplicate → `duplicate:true` no-op, ahead-of-applied →
   `-32003` gap error, partial overlap applies only the fresh tail).
3. **`process.reattach`** gained `stdinApplied` (the cumulative counter, so a
   reconnecting client resumes stdin at the right offset).
4. **`git.info`** gained `repoSlug` (`owner/repo` from `remote.origin.url`) and
   `defaultBranch` (from `refs/remotes/origin/HEAD`); both are always present,
   including on the non-repo body. The slug rule (host must be `github.com`, both
   segments charset-checked — not "exactly two path segments") is authoritative in
   [PROTOCOL.md → git.info](PROTOCOL.md#gitinfo), backed by its 42-shape table.
5. **`server.capabilities`** gained a `features` array
   (`["process.stdin.offset"]`).

**Off-wire churn.** `git.list_branches` switched to `--sort=refname` (same lexical
order); `git.worktree_create` grew a `safeRefName` ref-name guard — both verified
byte-identical. `git.refs` / `process.validateGroupKillPid` / `killProcessGroup`
are internal symbols, not wire methods — `server.capabilities` is authoritative on
the method set.

**How it was bounded.** The frame battery gates all five changes; differential
binary analysis confirmed the off-wire deltas are real source, not compiler noise
(forensics parked outside the committed tree). Provenance: Claude Desktop for Linux
1.18286.0 (2026-07-02) embeds a manifest pinning this SHA and calls 15 of the 19
methods — not `process.killAndWait` (nor `server.version` / `server.shutdown`,
driven via the `--stop` CLI) — so a real-session capture won't exercise the new
method and the synthetic battery stays its gate. That client also bears on D1's
trust boundary — `--install` carries `--cli-checksum` on the `--cli-url` download —
and a later argv capture (2026-08-10, two cold starts on one host) independently
confirms it. What Desktop supplies on the `--cli-zst` SFTP
fallback is recorded, with its limits, at [D1](DIVERGENCES.md#d1).

### `d20a77da22b7d4822f758654b226299ad7021c22` — 2026-06-09

**Wire delta.** None (pure rebuild).

**Off-wire churn.** The only source delta was an internal `ccd-cli-version`
cache-existence check in the `-install` bootstrap, off the JSON-RPC wire.

**How it was bounded.** The full frame battery stayed byte-identical; the pin was
bumped without code changes.

### `7cbfa471529b0dd33a5cc2f69c41c11bfe7fef6f` — 2026-06-04

**Wire delta.** `git.info` gained `root` (`git rev-parse --show-toplevel` — the
repo top-level even when `path` is a subdirectory).

**Off-wire churn.** Two more app-code changes rode along (re-audited 2026-07-02),
both already covered by claustrum and neither a missed divergence:

- The `-install` path gained an HTTPS download + SHA-256 verify + robust rename.
  claustrum mirrors the substance — `install.go` verifies SHA-256
  (`verifyChecksum`, unconditional on the `-cli-url` path) and downloads via
  `fetchToFile` (streamed to a temp file). The one un-mirrored detail is the
  reference's EEXIST-clear before rename; claustrum uses a plain atomic
  `os.Rename`, which POSIX-replaces a target *file* anyway — a minor Windows /
  directory-target robustness nuance with no wire impact.
- A `process.Spawn` failure-path refactor. This one is on a wire-reachable path
  (spawn failure), so it was the killAndWait-shaped risk: a change the happy-path
  battery never stresses.

**How it was bounded.** The static drift check passed on this build; only the
byte-for-byte frame battery caught `root` — the lesson that the battery, not the
static check, is the authoritative gate. The spawn refactor was differentially
probed 2026-07-02 across three failure modes → byte-identical: it kept the
synchronous `-32603` + Go-exec-error-string contract claustrum already emits
(`methods_process.go` `processSpawn` → `codeInternal`). Generalized lesson: a
one-line ledger entry can under-record a build, because an off-wire or
failure-path change never surfaces in the frame battery — so the bump procedure
compares more than the wire (see [Upstream tracking](UPSTREAM-TRACKING.md));
forensics parked outside the committed tree.

### `8de85faaa11694321e937499a18c7ab88f37c76c` — 2026-05-21

The baseline the clean-room reimplementation was first built against.

## When you bump the pin

After reconciling a new build (per the reconcile step in
[Upstream tracking](UPSTREAM-TRACKING.md)):

1. Add a row + a detail section here (newest first).
2. Update [`scripts/UPSTREAM_SHA`](https://github.com/schubydoo/claustrum/blob/main/scripts/UPSTREAM_SHA).
3. Document any wire change in [`PROTOCOL.md`](PROTOCOL.md).
