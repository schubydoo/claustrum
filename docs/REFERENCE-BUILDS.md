# Reference build ledger

Every claustrum release tracks a specific reference `claude-ssh` daemon build,
identified by git SHA (see [Upstream tracking](UPSTREAM-TRACKING.md) for how new
builds are discovered and diffed). This page is the running history of those
builds and **what each one changed on the wire** — so the reconciliation trail is
recorded in one place instead of scattered across commit messages.

The currently pinned build lives in [`scripts/UPSTREAM_SHA`](https://github.com/schubydoo/claustrum/blob/main/scripts/UPSTREAM_SHA);
this ledger is the narrative behind the bumps.

## Builds

Newest first. "Wire change" means a change to the JSON-RPC surface claustrum must
match byte-for-byte; a pure rebuild with no wire change still gets a row (so a
re-published SHA is distinguishable from a real release).

| Reference SHA | Built (UTC) | Wire changes | Reconciled in |
|---|---|---|---|
| `5db5e4a1…` | 2026-07-06 | none (off-wire: `daemon.token` persistence) | this PR |
| `7c2f88d1…` | 2026-07-02 | **5 changes** — see below | [#120](https://github.com/schubydoo/claustrum/pull/120) |
| `d20a77da…` | 2026-06-09 | none (pure rebuild) | [#97](https://github.com/schubydoo/claustrum/pull/97) (pin bump only) |
| `7cbfa471…` | 2026-06-04 | `git.info` gained `root` (+ off-wire install/spawn churn — see below) | [#60](https://github.com/schubydoo/claustrum/pull/60) |
| `8de85faa…` | 2026-05-21 | baseline (initial clean-room target) | initial implementation |

### `5db5e4a12f88487e47c2c48259b69a2d630bb3f7` — 2026-07-06

**No wire change.** One off-wire feature: the daemon now **persists its auth
token** to `daemon.token` (mode `0600`) in the socket's directory — written
atomically at startup via an `os.CreateTemp("daemon.token-*")` + rename, and
unlinked on graceful shutdown. This lets a client reconnect to an already-running
daemon and re-authenticate after the original `-token-file` was unlinked / the
`-token-fd` pipe closed. Matched here in `tokenpersist.go` (wired into
`runServe`/`teardown`); documented in
[PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken).

The change is **exhaustively bounded** — four independent diffs against `7c2f88d`
all converge on token-persistence and nothing else:

- **Function table** (all 6484 funcs via `.gopclntab`): only `daemon.persistToken`
  + `daemon.removePersistedToken` (and 6 log closures) added; only `daemon.Stop`
  (928→1024) and `daemon.runChild` (1024→1088) resized; **0 removed**. No
  `server`/`rpc`/`methods`/`results`/`process`/`git`/`files` function moved.
- **`.rodata` string constants** (exact byte accounting): exactly **5** new
  strings — `daemon.token`, `daemon.token-*`, and the three `[daemon] failed to
  {persist,stat,remove} …token…` messages — matching every byte of the packed-blob
  growth; **0** real removals. (`[daemon] failed to unlink token file %q: %v` and
  `claude-ssh: cannot resolve home directory: %v` already existed in `7c2f88d`.)
- **Frame battery + parity harness**: `7c2f88d` ≡ `5db5e4a` ≡ claustrum,
  byte-identical.
- **Syscall differential** (`7c2f88d` vs `5db5e4a`, direct): the only divergence
  is the `daemon.token` atomic write at boot and stat+unlink on shutdown; every
  other phase (files / extract / git / spawn / internal) is syscall-identical.

Same toolchain as `7c2f88d` (`go1.23.12`, `klauspost/compress v1.18.4`), so the
function-table churn is real source. **First observed on this host** 2026-07-06
(committed), fetched by a Claude Desktop client on 2026-07-10; the `daemon.token`
path is the server half of client **reconnect** (a reattaching client reads it to
recover the token). Verified via `scratch/syscall-diff.sh` (`phase 6_end` OK after
the match) with the frame battery still byte-identical.

### `7c2f88d13e5f269762dd4d463aa4eb3102214110` — 2026-07-02

Method count **18 → 19**. Five additions, all matched byte-for-byte (values and
timing) against the reference:

1. **`process.killAndWait`** — new method (between `process.kill` and
   `process.reattach`). Blocks until the process is gone. Params `timeoutMs`
   (grace; non-positive/absent → 3000 ms default, capped at 600000 ms) and
   `escalate` (default `true`; `false` → leave a stubborn process running and
   report `died:false`). Result `{found,died[,alreadyExited][,escalated]}`.
2. **`process.stdin.offset` contract** — `process.stdin` replies now always carry
   `applied` (cumulative decoded bytes); an `offset` param makes stdin idempotent
   across reconnects (duplicate → `duplicate:true` no-op, ahead-of-applied →
   `-32003` gap error, partial overlap applies only the fresh tail).
3. **`process.reattach`** gained `stdinApplied` (the cumulative counter, so a
   reconnecting client resumes stdin at the right offset).
4. **`git.info`** gained `repoSlug` (`owner/repo` from `remote.origin.url`, only
   when the path is exactly two segments) and `defaultBranch` (from
   `refs/remotes/origin/HEAD`); both always present, including on the non-repo
   body.
5. **`server.capabilities`** gained a `features` array
   (`["process.stdin.offset"]`).

Internal-only (no wire change, verified byte-identical): `git.list_branches`
switched to `--sort=refname` (same lexical order), `git.worktree_create` grew a
`safeRefName` ref-name guard. Same toolchain as `d20a77da` (`go1.23.12`), so the
function-table churn is real source, not compiler noise. **Not** a wire method:
`git.refs` / `process.validateGroupKillPid` / `killProcessGroup` are internal
symbols — `server.capabilities` is authoritative on the method set.

**First observed in a shipping client:** Claude Desktop for **Linux 1.18286.0**
(2026-07-02) embeds a manifest pinning exactly this SHA (readable offline from the
`.deb` — see [Upstream tracking](UPSTREAM-TRACKING.md) step 1). That client calls
15 of the 19 methods; it does **not** call `process.killAndWait` (nor
`server.version` / `server.shutdown`, which it drives via the `--stop` CLI), so a
real-session capture against 1.18286.0 won't exercise the new method — the
synthetic battery stays the gate for it. It also confirms D1's shape: `--install`
carries `--cli-checksum` on the `--cli-url` download path but ships the
`--cli-zst` SFTP fallback **unverified**, exactly the trust boundary claustrum
mirrors by default.

### `d20a77da22b7d4822f758654b226299ad7021c22` — 2026-06-09

**Pure rebuild — no wire change.** The only source delta was in the `-install`
bootstrap path (an internal `ccd-cli-version` cache-existence check in
`ensureCli`), which is off the JSON-RPC wire; the full frame battery stayed
byte-identical. Pin bumped without code changes.

### `7cbfa471529b0dd33a5cc2f69c41c11bfe7fef6f` — 2026-06-04

**`git.info` gained `root`** (`git rev-parse --show-toplevel` — the repo top-level
even when `path` is a subdirectory). The static drift check passed on this build;
only the byte-for-byte frame battery caught the new field — the lesson that the
battery, not the static check, is the authoritative gate.

**Fuller change set (re-audited 2026-07-02).** A structural function-table diff of
`8de85faa → 7cbfa471` (both `go1.23.12`, same deps → churn is real source) shows
`root` was *not* the only thing that landed in this build. Two more app-code
changes rode along — the original entry recorded only the wire-visible one. Both
are already covered by current claustrum; neither is a missed divergence:

- **Install path gained an HTTPS download + SHA-256 verify + robust rename.** New
  `internal/fetch.fileSHA256` and `internal/fetch.renameRobust`, a grown
  `decompressAndInstall`, and a tls/http2/crypto string avalanche (the `-cli-url`
  HTTPS client stack linking in). **Off the JSON-RPC wire** (`-install` mode).
  claustrum mirrors the substance: `install.go` verifies SHA-256 (`verifyChecksum`,
  unconditional on the `-cli-url` path) and downloads via `httpGet`. The one
  un-mirrored detail is `renameRobust`'s EEXIST-clear before rename; claustrum uses
  a plain atomic `os.Rename`, which POSIX-replaces a target *file* anyway, so the
  gap is a minor Windows / directory-target robustness nuance with **no wire
  impact**.
- **`process.(*Manager).Spawn` failure-path refactor.** +448 bytes, a new
  `failSpawn` helper (the old inline `Spawn.Printf.func5` is gone). This one *is* on
  a wire-reachable path (spawn failure), so it was the killAndWait-shaped risk: a
  change the happy-path battery never stresses. **Differentially probed
  2026-07-02** (`d20a77da` reference vs claustrum, three failure modes:
  ENOENT-absolute, not-in-PATH, exec-a-directory) → **byte-identical**. The refactor
  moved the failure log into a helper but kept the synchronous `-32603` +
  Go-exec-error-string contract claustrum already emits (`methods_process.go`
  `processSpawn` → `codeInternal`). No divergence.

The lesson generalizes the `root` one: a one-line ledger entry can under-record a
build. Diff the **function table**, not just the wire, on every bump — an off-wire
or failure-path change won't show up in the frame battery.

### `8de85faaa11694321e937499a18c7ab88f37c76c` — 2026-05-21

The baseline the clean-room reimplementation was first built against.

## When you bump the pin

After reconciling a new build (per the reconcile step in [Upstream tracking](UPSTREAM-TRACKING.md)):

1. Add a row + a detail section here (newest first).
2. Update [`scripts/UPSTREAM_SHA`](https://github.com/schubydoo/claustrum/blob/main/scripts/UPSTREAM_SHA).
3. Document any wire change in [`PROTOCOL.md`](PROTOCOL.md).
