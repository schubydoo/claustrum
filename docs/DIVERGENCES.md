# claustrum — deliberate divergences

Almost everything claustrum does is byte-identical to the reference daemon. This
file is the canonical catalog of the exceptions. The exceptions are the
behaviours that *knowingly* change a frame or an action. This file also gives the
rules that gate every one of them.

The [catalog table](#catalog) is the fast path. It gives one row per divergence,
with its default, how to activate it, and its reopen trigger. The
[rules](#how-we-decide-the-rules) come first for a reason: they decide which
shape an entry can take. The three shapes are always-on, opt-in, and conditional.

Per-method wire facts (params, result field order, error strings) live in
[PROTOCOL.md](PROTOCOL.md). Driver-claim provenance lives in
[ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance). We
condensed the exhaustive per-entry measurement forensics out of the committed
docs.

## The one hard rule

Byte-identical JSON-RPC frames are the product. A divergence needs a reason.
Matching does not need a reason. "Off by default" must mean byte-identical, not
almost byte-identical. An opt-in divergence that is not active leaves the wire
exactly as the reference leaves it.

[PROTOCOL.md](PROTOCOL.md) and the PR that shipped it also document every
intentional divergence below. The
[out-of-scope list](#explicitly-out-of-scope-would-break-compatibility) at the end
gives the inverse: the changes we will never make.

## How we decide (the rules)

The standard, in priority order:

> 1. Claude Desktop must keep working. Claustrum is a drop-in. If a behaviour
>    is reachable by Desktop, and matching the reference is what keeps Desktop
>    working, we match. How ugly the reference's behaviour is does not matter.
> 2. Match by default. A wart Desktop tolerates is the contract, not a bug.
>    Divergence needs a reason. Matching does not. The burden of proof is always
>    on the divergence.
> 3. A divergence earns ALWAYS-ON only if one of three conditions holds.
>    **(a)** Two things hold together. The reference's behaviour on that path
>    is not a frame at all, which means an unbounded wait, unbounded memory,
>    or unrecoverable data loss. And no honest caller can observe the
>    difference. **(b)** The trigger itself is unreachable on an honest path. **(c)** The trigger is reachable,
>    but both binaries fail the operation and the only delta is diagnostic
>    text.
> 4. Anything else that changes an honest-path frame is OPT-IN, default off, or
>    it does not ship.

Corollaries:

- Keeping a wart does not mean hiding it. We match the wart *and* we document
  it, so a user can see the edge before it causes damage.
- "The reference does it too" is a reason to match, never a reason to call it
  safe. D2 is the standing counter-example: the reference wipes a home directory,
  and we still refuse to do it.
- Every always-on divergence owes a reopen trigger. The reopen trigger is the
  observation that makes us take the divergence back out. An always-on
  divergence with no reopen trigger is a preference, not a decision.

### Reading the clauses

Clause (a) is an AND. Both halves must hold: the reference's behaviour is not a
frame, *and* no honest caller observes the difference. The second half is where
thresholds fail. A bound is a *threshold*. A threshold cannot separate a hostile
input from an input that is only slow or large, so an honest input trips it too.
The test is this question:

> When the guard fires on an honest input, who pays, and can they decline?

If the honest input is reachable, and the caller cannot turn the guard off, then
always-on is not justified. This holds no matter how the reference behaves on the
hostile path. This test flipped every timeout and size cap from always-on to
opt-in (D3, D5, D10, D11, D12, D14). D4 is the non-threshold sibling.

*Canonical example:* D2 satisfies both halves. The reference's home-wipe is
unrecoverable data loss, and no honest caller has a legitimate *use* for deleting
home. A caller can still reach that path by accident, which is exactly what the
guard is for.

Clause (b): the trigger is unreachable on an honest path. Most surviving
always-on entries use this form (D6, D7, D8, D9). Each entry has its own trigger,
and the glosses are not interchangeable. Two of the four are asserted rather than
enumerated: nobody ever enumerated Desktop's per-method param set against D9's
binding, and D6/D7 rest on an observed value plus a measured accepted-set. Read
those two as unenumerated, not established (rule 2 puts the burden on the
divergence).

*Canonical example:* D6. A `-cli-version` naming a destructive path outside the
cli-dir is not something any correct client emits.

Clause (c): both binaries fail, and the only delta is diagnostic text.
This clause is deliberately narrow, and we wrote it for D13. Measured, D13 does
not meet it, so the clause justifies no entry in this file today. A reader must
not take two things on trust. First, an `error.code` is not diagnostic text,
because a client branches on it. Second, on-disk state that a caller can
`files.stat` is not diagnostic text either. D13's honest-path rows differ in both,
so the clause keeps its literal wording and D13 sits [unresolved](#d13). We did
not widen the clause to fit its one candidate.

*Canonical example:* none currently qualifies.

### The "Desktop owns the argv" premise

Every opt-in tag rests on one claim about the driver: Claude Desktop owns the
daemon's argv on both `-serve` and `-install`. Therefore an operator cannot reach
a flag-only knob, and the `claustrum.conf` key (read beside the executable) is the
reachable one. This claim is the premise under D3, D4, D5, D10, D11, D12, D14 and
under the "(opt-in)" tag itself.

So much rests on the claim that it gets one canonical record. Its provenance, its
current evidence and its reopen trigger live in
[ARCHITECTURE.md → Driver claims and their provenance](ARCHITECTURE.md#driver-claims-and-their-provenance).
That section also holds the other two driver claims. Desktop parses `cliError`,
and Desktop uses the reported `libc` to choose which CLI build it downloads. If a
way for an operator to influence the daemon's argv is found, the claim reopens. A
Desktop release that adds such a way reopens it too. Such a route makes a
flag-only opt-in sufficient for Desktop-driven hosts. It does not moot the config
key, which serves every other driver.

## Conventions for opt-in divergences

These conventions hold for every opt-in entry. This section states them once
rather than repeating them in each entry:

- A flag and a matching `claustrum.conf` key. The config key is the reachable
  knob (see the argv premise above). Precedence is explicit CLI flag > config >
  default. claustrum resolves it with `flag.Visit`.
- Default off is the zero value, and disabled bypasses the guard entirely. A
  cap set to `0` skips its `io.LimitReader`. A timeout set to `0` skips
  `context.WithTimeout`. For the download, `0` instead relies on
  `http.Client{Timeout: 0}`, which is the stdlib's own "no timeout". Never use a
  huge-but-finite value. The `cap+1` / armed-cancel arithmetic is what defines the
  boundary. Routing the unlimited case through that arithmetic invents a boundary
  the reference does not have.
- No opt-in bound is a hang detector. Each bound is a threshold, so an
  honest-but-slow or honest-but-large input trips it too. That is precisely why
  they are off by default.
- At the shipped defaults, no claustrum-chosen `-install` wall-clock bound
  applies. Only stdlib transport clocks remain on the `-cli-url` path
  (`net.Dialer{Timeout: 30s}`, `TLSHandshakeTimeout: 10s`). Those two clocks are
  always-on, unnumbered, and unprobed on the reference.

## Catalog

| ID | What it does | Default | How to activate | Why (rule / clause) | Reopen trigger |
|----|--------------|---------|-----------------|---------------------|----------------|
| [D1](#d1) | SHA-256-verify the local `-cli-zst` blob | trusting (no verify) | conditional: the caller supplies `-cli-checksum` | rule 3: only a *wrong* checksum pays | Desktop supplying a checksum mismatching its own SFTP blob |
| [D2](#d2) | Refuse a destructive path that is or contains `$HOME` | always-on | always-on | rule 3 clause (a) | an honest caller legitimately targeting a path that is/contains home |
| [D3](#d3) | Cap `files.extract_tar` output size | off (`0` = unlimited) | `-max-extract-bytes` / `max-extract-bytes` | rule 4 (who-pays) | operator's cap refuses a legit extraction, or default lets a bomb through |
| [D4](#d4) | `files.read` refuses non-regular files | off | `-files-read-regular-only` / key | rule 4 | opt-in refuses a legit read, or default parks/OOMs the daemon in normal use |
| [D5](#d5) | Deadline on every `git` invocation | off (`0`) | `-git-timeout` / key | rule 4 | opt-in kills an honest slow git |
| [D6](#d6) | `-cli-version` must be a single path component | always-on | always-on | rule 3 clause (b) | Desktop passing a multi-component `-cli-version` |
| [D7](#d7) | `-cli-version` must not collide with the temp sweep | always-on | always-on | rule 3 clause (b) | Desktop passing `.fetch-*` or `*.zst` |
| [D8](#d8) | Never follow or write a foreign or symlinked `remote-server.log` | always-on | always-on | rule 3 clause (b): unreachable on the deployed path | a shared socket dir that also needs the log file, or the reference adding the same refuse-to-follow |
| [D9](#d9) | Namespace-wide params binding (type error in an unread field → `-32602`) | always-on | always-on | rule 3 clause (b) | a real client sending a type-mismatched unread namespace field |
| [D10](#d10) | Cap `-install` CLI size (decompressed + download body) | off (`0`) | `-max-cli-bytes` / key | rule 4 (who-pays) | Desktop ceasing to treat a disk-full message as terminal |
| [D11](#d11) | Deadline on the `<cli> --version` runnability probe | off (`0`) | `-cli-probe-timeout` / key | rule 4 | Desktop turning out not to parse `cliError` (retraction rider) |
| [D12](#d12) | Bound on the `-install` download exchange | off (`0`) | `-cli-download-timeout` / key | rule 4 | operator with the bound set reporting an honest slow download failed |
| [D13](#d13) | Verify checksum before decompressing (`-cli-url`) | always-on | always-on | **UNRESOLVED**: clause (c) written for it, measured not met | any change to how Desktop classifies `cliError` |
| [D14](#d14) | Deadline on the `ldd --version` libc probe (linux) | off (`0`) | `-libc-probe-timeout` / key | rule 4 | a slow `ldd` on a host where the deadline changes the reported `libc`. The host is a musl host the glob misses, or a mixed host. Or the reference bounding it above 45 s |
| [D15](#d15) | Verify a run-dir lock holder is our serve process before signalling it (macOS) | always-on | always-on | rule 3 clause (a) | the reference adding the same macOS check, or a macOS holder legitimately un-inspectable via `KERN_PROCARGS2` |
| [D16](#d16) | `git.status` of a linked worktree returns the status on Windows, where the reference errors `exit status 128` (Windows failure mechanism not yet pinned) | always-on (Windows) | always-on | claustrum-more-correct (D2/D8 pattern). **REACHABLE** | the reference fixing its Windows git.status, or a decision to reproduce its failure for strict 1:1 |
| [D17](#d17) | An abandoned `lsof` run reads as busy, not idle (macOS) | always-on (macOS) | always-on | rule 3 clause (a) | the reference distinguishing the two empty results, or an operator reporting a run dir the cleaner will not tidy because `lsof` keeps failing |
| [CT-1](#ct-1) | Opt-in `wantPid` → `pid` + `startTime` on spawn/reattach | off (fields omitted) | caller sends `"wantPid":true` | sanctioned optional-param extension | — (additive, degrades both ways) |
| [CT-2](#ct-2) | `-keep-children` leaves the child tree running on shutdown | off | `-keep-children` / `keep-children` key | off-wire opt-in extension | — |
| [CT-3](#ct-3) | `claustrum.conf` config file | absent ⇒ stock | create the file | the opt-in mechanism itself | — |
| [CT-4](#ct-4) | Hardened token persistence | not built (deferred idea) | — | deferred | — |
| [CT-5](#ct-5) | `-listen-pipe` Windows named-pipe transport | off | `-listen-pipe` / `listen-pipe` key (Windows) | additive opt-in transport | — |

Tags: **opt-in** = operator-declinable (flag + config key). **conditional** =
activated by the caller (D1). **always-on** = no switch. The CT block uses
"opt-in" in the looser sense of "off unless somebody asks for it". CT-1 is
caller-activated and CT-3 *is* the config mechanism, so neither one is
operator-declinable. Only CT-2 and CT-5 carry a flag and a key.

## Entries

### D1 · Re-harden the `-cli-zst` checksum (conditional) { #d1 }

- **Behavior.** The reference verifies `-cli-checksum` only on the `-cli-url`
  download, not on the local `-cli-zst` (SFTP) blob. When the caller supplies a
  `-cli-checksum`, and only then, claustrum SHA-256-verifies `-cli-zst`. On a
  mismatch claustrum answers `checksum mismatch: …` and leaves the source blob
  intact. An absent or empty checksum stays trusting → byte-identical to the
  reference.
- Why conditional, not opt-in. The *caller* activates it by supplying
  `-cli-checksum`. On `-install`, that caller is Desktop. An operator does not
  activate it. It therefore has no flag or config key, and it needs none. The
  delta requires a *wrong* checksum, because a correct one is byte-identical, so
  no honest caller pays for it.
- **Observable delta** (supplied-wrong checksum only): a valid blob that the
  reference installs returns `checksum mismatch`. A corrupt blob returns `checksum
  mismatch` instead of `decompressing: …`.
- **Observed once**, on 2026-08-10, during a download failure that the probe
  forced. This is the only capture that reached the SFTP rung. The captured
  Desktop `-cli-zst` invocation supplied no `-cli-checksum`, where the `-cli-url`
  call seconds earlier did supply one. The condition was therefore false, and no
  verification ran. This is one instance of one failure shape.
- **Reopen trigger.** Desktop supplying a `-cli-checksum` that does not match the
  blob it uploaded over SFTP. Conditional is not the same as unreachable.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`, and `install.go`.

### D2 · Refuse a home directory as a destructive path target (always-on) { #d2 }

- **Behavior.** Three methods hand a caller-supplied, `~`-expanded path to
  `os.RemoveAll`. `files.extract_tar` wipes `destDir`. When git exits non-zero for
  a non-locked reason, `git.worktree_remove` deletes `worktreePath`. A locked
  worktree is refused, not deleted. When `git.worktree_create` rolls back a
  worktree, it deletes `worktreePath`. A rollback follows a failed
  `git worktree add`, unless the caller's `timeoutMs` cut the add short.
  A cut-short add answers
  `timeout` and leaves the leaf. A rollback also follows a post-checkout drain
  that exceeded the caller `timeoutMs`, where the guard is defense-in-depth behind
  create's own containment. `wipesHomeDir` (`homeguard.go`)
  refuses any target that is or contains the home directory. Descendants stay
  allowed, because extracting into `~/.claude/…` is the daemon's own install path.
- **Containment is the test, and the predicate resolves relative paths**
  (`filepath.Abs`) before it compares them. This is why `wipesHomeDir` resolves
  the path. Without that resolution, `"worktreePath":".."` from a daemon whose cwd
  is home reached `os.RemoveAll` on home's parent (measured pre-`7d193f89`). On
  `git.worktree_remove` that `..` no longer reaches the delete, because the
  containment check below refuses a `".."` component first. But the resolution
  still guards
  `files.extract_tar`, and it is the guard's own design invariant regardless.
- **Since `7d193f89`, `git.worktree_remove` refuses a home path on its own, as
  parity, not as this divergence.** That build confined session worktrees to
  inside the repository. A `worktreePath` that is not strictly under `baseRepo` is
  now refused *with the reference's own "not inside the repository" wording*. The
  refusal comes before git, the `os.RemoveAll` fallback, or `wipesHomeDir` is
  reached. Every `~`-expanded home path is such a path. On that method's
  default branch `wipesHomeDir` is now
  defense-in-depth behind the reference's containment. It can still fire only in the
  exotic case of a repository that is itself an ancestor of home. On the
  `worktreeRoot` / `external_root` branch the in-repo containment does not apply,
  because the worktree is placed under the caller's root. So there `wipesHomeDir`
  is again the active home guard, and it runs before the delete, as it does on
  the default branch.
  `files.extract_tar` gained no such containment. So there too `wipesHomeDir` remains
  the primary and only home-directory guard, which is why this divergence stays
  always-on.
- **This fired.** On 2026-08-02 an in-repo fuzzer sent `"destDir":"~"` at a live
  daemon and destroyed the maintainer's home directory. `"~"` is the first value in
  the adversarial list that survives the old `IsAbs && !isFilesystemRoot` gate. A
  home directory is exactly "absolute and not a filesystem root".
- **Why always-on.** D2 satisfies both halves of rule 3 clause (a). At `7d193f89`
  the reference's behaviour is still unrecoverable data loss on `files.extract_tar`.
  Measured, `"destDir":"~"` wipes the home directory and answers `{"success":true}`,
  and that method gained no containment. `git.worktree_remove` no longer reaches it,
  because its own containment refuses the home path first. So there `wipesHomeDir` is
  defense-in-depth on the default branch, for the exotic repo-is-an-ancestor-of-home
  case. It is the active guard on the `external_root` branch, which skips that
  containment. And no honest
  caller has a legitimate *use* for deleting home. A caller can still reach that path
  by accident, which is the point.
- **Not a security boundary.** The socket + token already grant `process.spawn`
  ([SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
  This guard stops the accidental, generated, or mistyped path. It does not
  resolve symlinks.
- **Reopen trigger.** An honest caller legitimately naming a destructive target
  that *is or contains* a home directory.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → all three methods. Also `homeguard.go` and
  `homeguard_test.go` (`wipeDestDir` seams the destructive call, so the suite is
  safe against an unfixed tree). Measurement: forensics.

### D3 · Make the `files.extract_tar` size cap opt-in { #d3 }

- **Behavior.** `maxExtractBytes` caps extraction output. An over-cap extraction
  returns `{"success":false,"fileCount":0,"error":"extraction size limit exceeded"}`
  and removes the truncated entry.
- **Default.** `0` = unlimited = byte-identical. **Activate:** `-max-extract-bytes
  <n>` or the `max-extract-bytes` key. The disabled state bypasses `io.LimitReader`
  (`io.Copy(out, tr)`).
- **Why opt-in.** Measured, the reference completes a 629 MB extraction with no cap
  at the pin. That is a frame, not an unbounded wait, so a non-zero default fails an
  extraction the reference completes, and Desktop owns the argv (rule 4).
- **Reopen trigger.** An operator's cap refusing a legitimate extraction, or the
  default letting a size bomb through in normal use.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) and `methods_files.go`. Measurement:
  forensics.

### D4 · Make the `files.read` regular-file guard opt-in { #d4 }

- **Behavior.** With the guard on, `files.read` refuses any non-regular path with
  `-32602 files.read: not a regular file`. The reference refuses none. With the
  guard off, the flag short-circuits the predicate
  (`filesReadRegularOnly && !fi.Mode().IsRegular()`), so the mode check never
  runs.
- **Default.** Off (byte-identical). **Activate:** `-files-read-regular-only` or
  the key.
- **Why a flag and not a narrower predicate.** `/dev/null` and `/dev/zero` are
  indistinguishable by mode, so any predicate that admits the first also admits the
  second.
- **The default has two measured costs** (both are the reference's own behaviour
  too). First, a writerless FIFO parks a request goroutine *and* a descriptor:
  linux reserves the fd number before it blocks, which draws down `RLIMIT_NOFILE`,
  and `accept()` shares that limit. Second, an unbounded device read (`/dev/zero`)
  never reaches EOF. Under a 2 GiB cgroup cap the kernel OOM-killed both binaries.
  `maxBytes` cannot prevent either cost: it keys off the stat size, which is `0`
  for every non-regular kind on linux.
- **Why opt-in.** Across seven non-regular shapes (nine in all, with two
  regular-file controls), claustrum with the guard off matches the reference
  byte-for-byte. The always-on guard cost an honest `/dev/null` read a `-32602`
  that the reference never produces (rule 4).
- **Reopen trigger.** An operator with the flag set reporting a legitimate read
  refused. The opposite direction says the default is wrong: a report of the
  daemon parked or OOMed by a non-regular read in normal use.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `files.read` → Non-regular files.
  Full table, OOM/fd reasoning, unmeasured shapes: forensics.

### D5 · Make the `gitTimeout` deadline opt-in { #d5 }

- **Behavior.** With the deadline on, claustrum bounds every git invocation (shared
  `gitCtx` across `git` / `gitStdoutErr` / `gitDeadline`). On `git.worktree_remove`
  a hit deletes nothing and answers
  `git worktree remove timed out after <dur>; no cleanup was attempted, and git may have partially removed the worktree`.
  On `git.status` / `git.list_branches` a hit surfaces as `-32603 signal: killed`.
  A killed repo-detection call answers `isRepo:false`.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-git-timeout
  <dur>` or the key. The disabled state bypasses `context.WithTimeout`.
- **Never read a timeout as "git refused."** `git.worktree_remove` treats a
  non-locked git failure as permission to delete `worktreePath`, so claustrum keeps the timeout
  reply separate from the failure arm. The cap is also softer than it reads on the
  general git sites. `CombinedOutput`/`Output` waits on git's output pipe. So a git
  that leaves a surviving child stays blocked past the deadline on `git.status`,
  `git.list_branches` and the repo probes. Those paths are unmeasured for a
  descendant-orphan case, so the drain is recorded rather than capped there. The one
  measured exception is `git.worktree_create`'s read-tree checkout, which can run a
  smudge/hook filter that backgrounds a pipe-holding descendant. That path alone
  caps the post-exit drain at a fixed ~5 s from git's own exit. It also SIGKILLs
  the process group (`hardenedGitWorktreeCreate` / `worktreeCreateDrainCap`).
  That reproduces `4534d86`. When the caller `timeoutMs` exceeds that drain, the
  answer is success. Otherwise the answer is
  `errorCode:"timeout"` ("deadline expired after the checkout finished", no
  `signal: killed`) with the worktree rolled back. Measured in
  `scratch/probe/wt-success-lingering-4534d86.md`. This is parity, not a divergence.
- **One opted-in arm loses data silently, on either seeding pass.** `populateWorktree`
  runs two `git ls-files` passes, `copyWorktreeIncludes` (`worktreecopy.go`) and
  `copyClaudeDir` (`worktreeclaude.go`), and both are best-effort: a killed listing
  takes the early return and the error is dropped. Therefore `git.worktree_create`
  still answers `{"success":true}` with files missing. Each pass gets its own deadline
  from `gitCtx`, so a kill loses one pass and not the other: the manifest copy can
  succeed while the `.claude/` seed is lost, or the other way round. The `.claude/`
  pass has no manifest precondition, so unlike the manifest pass it runs on every
  create, which widens where the arm can fire. The loss itself still needs a listing
  slower than the configured deadline. No frame moves. This arm is absent at the
  default.
- **`git.worktree_create` under both deadlines.** The add and the read-tree
  checkout run under the shared deadline. A D5 hit on the add answers `git
  worktree add failed: …` with `errorCode:"worktree_add_failed"`. That is the same
  failure arm as any other git error, and it is distinct from the 4534d86
  caller-`timeoutMs` arm
  (`errorCode:"timeout"`). A D5 hit on the read-tree checkout is discarded like any
  best-effort step (see the loses-data-silently arm above). The error is dropped and
  the create can still answer `{"success":true}` with an incomplete worktree. When a
  caller supplies a `timeoutMs` LONGER than `-git-timeout`, the tighter D5 deadline
  fires first during the add. claustrum still answers `worktree_add_failed`, because
  it attributes the kill to the deadline that actually fired. A caller `timeoutMs`
  earns `errorCode:"timeout"` in one case only. That case is the one where the
  caller `timeoutMs` is the deadline that fired. So the caller's
  longer duration is never quoted for a kill D5 caused. This interaction is
  claustrum-only, because the reference has no `-git-timeout`. It is absent at the
  default. Implemented with
  a distinct context cause (`errCallerTimeoutMs`, checked by `callerTimeoutFired` in
  `methods_git.go`).
- **Why opt-in.** The reference showed no deadline at or below 75 s on
  `worktree_remove`. An honest 61 s git was never measured. The deadline cleared
  clause (a)'s not-a-frame half, but the `-32603 signal: killed` arm is an honest
  caller observing the difference (rule 4).
- **Reopen trigger.** An operator with `-git-timeout` set reporting an honest slow
  git killed by it. The `-32603` arm makes a single report enough.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove` and
  `git.list_branches`. Also `methods_git.go`, `worktreecopy.go` and
  `worktreeclaude.go`.

### D6 · `-cli-version` must name a single path component (always-on) { #d6 }

- **Behavior.** The install's clearing step is
  `os.RemoveAll(filepath.Join(cliDir, cliVersion))`, so a version that reaches
  outside the cli-dir deletes unrelated data. claustrum answers `cli version "…"
  must be a single path component` and touches nothing. It refuses `.`, `..`, and
  both `/` and `\` on every OS, so the accepted set does not change with the
  platform.
- **A single component rather than a lexical containment check.** A lexical check
  accepts `link/1.0.0` (an intermediate symlink under the cli-dir, followed at open
  time), and `EvalSymlinks` only adds a TOCTOU window before the `RemoveAll`. A
  final component that is itself a symlink stays legal, because `os.RemoveAll`
  unlinks it rather than follows it. So the rule is narrower than "no symlinks".
- **Why always-on.** Rule 3 clause (b). Measured, the reference destroys the
  target on both `../victim` and `link/1.0.0`. The real client passes bare version
  strings (`1.0.86`, a commit sha, `latest`, all measured accepted). The evidence is
  an observed value plus a measured accepted-set, not an enumeration.
- **Reopen trigger.** Desktop passing a `-cli-version` that is not a single path
  component.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`, and `install.go`.

### D7 · `-cli-version` must not collide with the install temp sweep (always-on) { #d7 }

- **Behavior.** The orphan sweep claims `.fetch-*` and `*.zst` after *every*
  install. Therefore `-cli-version .fetch-x` or `1.0.zst` installs correctly, and
  the sweep deletes it moments later in the same run. Measured, both binaries
  finish with an empty cli-dir and no `cliError`. They report success although
  they installed nothing. claustrum answers `cli version "…" collides with the
  install temp sweep` instead. The sweep predicate and this check share one
  definition, so they cannot drift apart.
- **Unlike D6 this gives up exact parity** (an error beats a success that installed
  nothing). But that preference is not what earns the entry. Clause (b) earns it.
- **Why always-on.** Rule 3 clause (b), on the same evidence as D6.
- **Reopen trigger.** Desktop passing a `-cli-version` matching `.fetch-*` or
  `*.zst` (one observation reopens D6 too, because both rest on the same evidence).
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`, and `install.go`.

### D8 · claustrum never follows or writes a foreign/symlinked `remote-server.log` (always-on) { #d8 }

- **Behavior.** claustrum rotates the prior log to `remote-server.log.old` and
  creates a fresh own log (`os.Rename` then `O_EXCL` create). That matches
  `4534d86`, which keeps the previous session's log as `.old` on every restart. That part is
  parity, reachable on the ordinary per-restart path. The divergence is how
  claustrum treats a log it does not own. For a planted symlink in a writable
  directory, `os.Rename` moves the link itself (not its target) to
  `remote-server.log.old`. `O_EXCL` then creates a fresh regular log. The link is
  never followed, and the victim file stays untouched. On a sticky directory where
  claustrum cannot rename the existing entry (another user's file or symlink), the
  rename cannot proceed and the exclusive create fails. claustrum then declines the
  log and falls back to inherited stdio. In both cases claustrum never follows the
  link or writes into a file it does not own.
- **The upstream state changed, and this entry was revised to match it.** Measured
  2026-09-06 (scratch/security/disclosures.md): `4534d86` no longer plain-truncates
  a foreign *regular* file. The pre-`4534d86` disclosure (measured 2026-08-06 on
  `5db5e4a`) is fixed upstream, and claustrum now matches the `.old` rotation on the
  common path. But in a root-owned sticky directory the reference still FOLLOWS a
  planted `remote-server.log` symlink. It writes its own log into the victim, or it
  refuses to start. claustrum refuses to follow it. So a narrower hardening
  survives, and it is a D2-style case: "the reference does it too" is not a reason
  to call it safe.
- **Hardening, not a defect claim.** To reach it you need a local user who can
  already plant a file or symlink in that directory. The justification is a design
  position: a daemon must not follow a link it did not plant or write into a file
  it does not own. It does not depend on the reference being wrong.
- **Why always-on.** Rule 3 clause (b). The trigger is unreachable on the
  deployed path (`~/.claude/remote/` is per-user, not world-writable). A flag here
  gates a branch that no honest deployment reaches.
- **Reopen trigger.** A deployment that puts the socket directory somewhere shared
  *and* needs the log file. Or the reference gaining the same refuse-to-follow (then
  this becomes parity, not a divergence).
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Daemon log, and `server.go`
  (`openDaemonLog`).

### D9 · Namespace-wide params binding is stricter than the reference's (always-on) { #d9 }

- **Behavior.** claustrum binds `params` into one struct per namespace
  (`pathParams`, `gitParams`), so a field that is valid for the *namespace* but
  unused by *this* method still participates in decoding. A type-mismatched value
  there answers `-32602` (for example `files.stat {"maxBytes":"{"}`, `git.status
  {"baseRepo":[1,2]}`). The reference binds only the field the method reads and
  ignores the rest. Both binaries ignore a genuinely unknown key.
- **Why always-on.** Rule 3 clause (b): the trigger is a type error in a field the
  method does not read (a client bug). Stated honestly, this is narrower than "a
  real client never sends them". Nobody ever enumerated Desktop's per-method param
  set against this binding, so it is unenumerated, not established.
- **Reopen trigger.** Any real client sending a type-mismatched value in a
  namespace field the target method does not read. That observation is also the
  measurement this entry owes and does not have.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Params presence and typing.

### D10 · Make the `-install` CLI size cap opt-in { #d10 }

- **Behavior.** `maxCLIBytes` governs two reads: the decompressed CLI
  (`decompressing: decompressed CLI exceeds <n> bytes`) and the HTTP download body
  (`download failed: response exceeds <n> bytes`).
- **Default.** `0` = unlimited (byte-identical). **Activate:** `-max-cli-bytes <n>`
  or the key. When disabled, both call sites bypass their `LimitReader`.
- **claustrum streams the blob and never buffers it.** It writes a `.blob-<random>`
  temp (or `$TMPDIR/claustrum-fetch-<random>` on a first install, before the cli-dir
  exists) and hashes it in one pass. Therefore "cap off" does not mean unbounded
  memory (measured 886 MB → 10 MB on a 400 MiB payload). The prefix `.blob-`, not
  `.fetch-`, is the one that matters. `.fetch-*` is the swept namespace. If the
  staging blob used the `.fetch-` prefix, the sweep of a concurrent install deletes
  it, and that defeats the staging retry
  (`errStagingVanished`). The creator, both housekeeping passes, and
  `validateCLIVersion` all read `blobTempPrefix`.
- **Who pays for opting in.** A cap set below free space replaces a disk-full report
  with the cap's own message, which costs the user the free-space hint. But that
  cost turns on the `cliError` driver claim
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)). At the
  default, claustrum preserves the disk-full report and matches the reference.
- **Why opt-in.** Measured, the reference takes a 600 MiB payload to the runnability
  check on both the decompress and download paths. That is a frame, not an unbounded
  wait, so a non-zero default fails an install the reference completes, and Desktop
  owns the argv (rule 4).
- **Reopen trigger.** Desktop ceasing to treat a disk-full message as terminal
  (which removes the cost). One plausible Desktop change fires this trigger and
  D13's at once: Desktop broadening its terminal match to any `decompressing:`
  error.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) and `install.go` (`fetchToFile`,
  `zstdDecompress`). RSS and cap-below-free-space tables: forensics.

### D11 · Make the `-install` runnability probe deadline opt-in { #d11 }

- **Behavior.** `isRunnable` runs `<cli> --version`. There are two probe sites: the
  cache-hit guard, and the probe after extraction. A timeout on the first site is
  indistinguishable from a cache miss, so an opted-in timeout produces one of three
  outcomes, depending on the flags. With no source it answers `cli <v> missing and
  no --cli-url or --cli-zst provided`. With a fast replacement it performs a
  *silent* reinstall, where only `cliWasPresent:false` moves. With the same slow CLI
  supplied it answers `installed cli at <path> is not runnable`. On `-cli-zst`
  claustrum
  consumes the blob whenever decompression succeeds.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-cli-probe-timeout
  <dur>` or the key. The disabled state bypasses `context.WithTimeout`.
- **A bound is not a hang detector.** Measured 2026-08-07: a CLI that answers
  honestly in 20 s makes an opted-in claustrum fail the install, delete the staged
  binary and consume the blob. The reference installs that CLI and returns at 20 s.
  The reference also installed a 90 s CLI, and it waited 91 s. So the reference has
  no deadline at or below 90 s, and any finite bound diverges for some honest input.
  Picking 30 s or 60 s only moves the boundary. Above 90 s is unmeasured on both.
- **Why opt-in.** The deadline cleared clause (a)'s not-a-frame half (the
  reference's wait is apparently unbounded), but an honest-but-slow CLI pays and,
  with Desktop owning the argv, cannot decline (rule 4). We verified the flip
  against the reference. We did not only argue it.
- **Reopen trigger** (a retraction rider, not the flip): Desktop turning out not to
  parse `cliError` after all. See
  [ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance). Whether any
  client reads `cliWasPresent` is unprobed either way.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) and `install.go`. Probe-site table, 20 s /
  90 s table, sweep/prune, zero-parsing edges: forensics.

### D12 · Make the `-install` CLI download bound opt-in { #d12 }

- **Behavior.** The download once ran with `http.Client{Timeout: 5m}`. It now runs
  with `Timeout: cliDownloadTimeout`, which defaults to `0`. The bound covers the
  whole exchange, including the body read. A real body over a link that needs six
  minutes therefore trips it exactly as a black hole does.
- **Default.** `0` = no bound (byte-identical). `http.Client{Timeout: 0}` is the
  stdlib's own "no timeout" sentinel. **Activate:** `-cli-download-timeout <dur>` or
  the key.
- **Zero frees the body read, not every clock.** `fetchToFile` leaves `Transport`
  nil, so `http.DefaultTransport` still applies `net.Dialer{Timeout: 30s}` and
  `TLSHandshakeTimeout: 10s` on `-cli-url`. A SYN-black-holed host therefore fails
  at 30 s with the bound off. Both clocks are always-on stdlib defaults, unnumbered
  and unprobed on the reference.
- **Why opt-in.** `4534d86` bounds a fully STALLED body itself, at a 60 s read-idle
  abort that claustrum reproduces always-on as parity. That is NOT this divergence
  (see [PROTOCOL.md](PROTOCOL.md) → `-install` download). What a non-zero
  `cliDownloadTimeout` adds beyond the read-idle abort is a TOTAL-exchange cap. No
  total cap was observed on the reference within the window measured. VM-measured
  against `4534d86`, a body trickling 1 byte every 30 s was still downloading at
  150 s. Each byte resets the read-idle clock, so the read-idle abort never
  fires. The
  honest slow-but-progressing case a total cap penalizes was measured on `5db5e4a`: a
  valid blob dribbled to completion over ~324 s installed there, while claustrum at the
  retracted 5 m failed it at 300 s. Such a download pays under a non-zero bound, and
  Desktop owns the argv (rule 4). The earlier "no bound at or below 400 s on a stalled
  body" evidence was also `5db5e4a`, before the read-idle abort existed. On `4534d86`
  that same never-sent body aborts at 60 s via the read-idle path, not this deadline.
- **Reopen trigger.** An operator with the bound set reporting an honest slow
  download failed by it.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) and `install.go` (`fetchToFile`). Straddle
  and stall tables, confounders: forensics.

### D13 · `-install` verifies the checksum before decompressing (always-on, UNRESOLVED) { #d13 }

!!! warning "This is the one entry that does not currently clear rule 3."
    We record it rather than explain it away. It stays in the code, labelled,
    rather than justified by a rule bent around it.

- **Behavior.** On the `-cli-url` path the reference decompresses first and aborts
  on the first invalid bytes. claustrum hashes the response as it streams to disk,
  verifies the checksum, and then decompresses. On a blob that is both
  undecompressable and wrong-checksummed the reference says
  `decompressing: unexpected EOF` where claustrum says `checksum mismatch`. A
  *genuine mid-transfer interruption* never reaches the checksum on claustrum,
  because `io.Copy`'s error returns first. That case diverges on the prefix instead:
  `download failed: <err>` against the reference's `decompressing: <err>`.
- **Why it is unresolved.** We wrote clause (c) for this entry and, measured, it
  does not meet it. On both honest-path rows the reference creates an empty
  cli-dir where claustrum creates nothing (when the cli-dir did not already
  exist). The delta is therefore not confined to diagnostic text: an empty directory
  is state a caller keeps, and a caller can distinguish it with a `files.stat`. The
  reopen fixture run 2026-08-08 did not meet its condition. The on-disk delta is
  conditional on the cli-dir being absent, and it does not self-heal.
- **The trigger is reachable** (not "an input no honest caller produces", which was
  measured wrong): a bad mirror, a partial upload, or a stale short proxy object is
  undecompressable *and* checksum-mismatched with no adversary. This is *not* the
  generic "flaky network" case: a genuine interruption is the prefix-divergence row
  above.
- **Why still always-on despite being unresolved.** The delta stays cheap because
  neither string is disk-full-shaped. Therefore Desktop (per the `cliError` driver
  claim) classifies both the same way and retries rather than fails terminally. That
  is a claim about a third binary, and the parity harness cannot settle it
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)). If it is
  ever falsified, the cheapness argument fails and this entry owes an opt-in flip.
- **Reopen trigger.** Any change to how Desktop classifies `cliError`. For example,
  distinguishing `checksum mismatch` from a decompression error, or matching either
  as terminal.
- **Not the same as D1.** D1 is about *whether* claustrum verifies the local
  `-cli-zst` blob at all. D13 is about the *order* of verify and decompress on the
  `-cli-url` download.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Staging and cleanup, and `install.go`.
  Ordering, clause-(c), and reopen-fixture tables: forensics.

### D14 · Make the `-install` libc probe deadline opt-in (linux) { #d14 }

- **Behavior.** Since reference build 3ef9370 `detectLibcWith` runs `ldd --version`
  on every call and lets its output decide (see `classifyLibc`). A "musl" banner
  reports `musl`. Any other output reports `glibc`. When `ldd` produced no output,
  and only then, the loader glob
  (`/lib/ld-musl-*.so.*`) is consulted. The
  deadline bounds that `ldd` run, which is now always in the path. Off linux,
  `libc_other.go` returns `""` without probing, so the bound cannot fire there.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-libc-probe-timeout
  <dur>` or the key. The disabled state bypasses `context.WithTimeout` (`lddCtx`).
  **Not the
  same knob as `-cli-probe-timeout`** (D11), whose name differs only in the
  `cli`/`libc` prefix.
  `-cli-probe-timeout` bounds `<cli> --version`. `-libc-probe-timeout` bounds
  `ldd --version`. `TestInstallArmWiresEachFlagToItsOwnGlobal` exists because a swap
  compiles and passes every isolated test.
- The value delta is narrow but not cosmetic. When the armed deadline kills
  `ldd` before it writes anything, its output is empty, so the loader glob decides
  the fallback: a marker present reports `musl`, else `glibc`. A deadline that
  fires after `ldd` wrote partial output takes that output, not the glob. The
  empty-output case coincides with the un-timed answer, except where the un-timed
  `ldd` output disagrees with the marker. Two host shapes disagree. On a musl host
  the glob misses, the fallback reports `glibc` where the un-timed banner reports
  `musl`. On a mixed host carrying a marker, the fallback reports `musl` where its
  un-timed glibc output reports `glibc`. Per the driver claim that Desktop uses
  `libc` to choose a CLI build
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)), the
  wrong build can be fetched on those host shapes.
- Softer than it reads. The deadline fires in only one of two stall shapes: a
  stalled `ldd` that leaves a surviving child keeps claustrum blocked past the
  deadline (the same softness the general git sites have under D5). This `ldd` probe
  (`runLddVersion`, `CombinedOutput`) does not cap the post-exit drain. Only
  `git.worktree_create`'s checkout does, where the descendant-orphan case is
  measured. The deadline also addresses the stall half only. A hostile `ldd`
  resolved earlier in `PATH` that answers in 1 s is untouched, and `classifyLibc`
  then trusts its `musl` banner verbatim.
- **Why opt-in.** The deadline cleared clause (a)'s not-a-frame half (the reference
  gave no reply at 45 s in the discriminating shape), but the honest-path cost was
  untested in either direction, and there was no escape hatch. An untested
  conjunction is not a justification (rule 4).
- **Reopen trigger.** A host where the armed deadline changes the reported `libc`
  from its un-timed value. The host is a musl host the glob misses, or a mixed
  host, and in either case it has a slow `ldd`. Or any measurement showing the
  reference bounds this probe above 45 s.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`. Also `install.go` (canonical
  stall table), `libc_linux.go` and `libc_other.go`. Full measurement: forensics.

### D15 · Verify a run-dir lock holder is our serve process before signalling it (macOS) { #d15 }

- **Behavior.** On the run-dir lock's eviction path, before a new `-serve` daemon
  signals a live lock holder, it verifies that the holder is one of our own `-serve`
  processes bound to this socket. On Linux both the reference and claustrum verify the
  holder's command line (claustrum reads `/proc/<pid>/cmdline`). On macOS there is no `/proc`, and the reference does not
  verify the holder before signalling: it sends SIGTERM then SIGKILL to the recorded
  pid unverified. Measured on a macOS VM: the reference signals even a lock holder
  that is not a serve process. claustrum instead reads the holder's argument vector
  from `sysctl KERN_PROCARGS2` and refuses to signal a pid whose argv is not our
  `-serve` for this socket.
- **Default.** Always-on, macOS only. On the honest path the live lock holder wrote
  its own pid into the record, so the verification passes and the outcome is
  identical to the reference (the predecessor is evicted).
- **Why always-on (rule 3 clause (a)).** Signalling an unverified pid is
  unrecoverable harm. A crash can leave a stale record whose pid is reused by an
  unrelated process. A foreign process can also hold the flock. The reference then
  SIGKILLs that innocent process. No honest caller benefits from skipping the check.
  The same guard is already always-on on Linux (via `/proc`), so macOS matches
  Linux's safety rather than the reference's macOS gap.
- **Cost.** None on the honest path. In the one case where the reference evicts and
  claustrum does not, the holder is un-inspectable or is not a serve process. There
  claustrum serves
  without run-dir ownership instead of killing the holder, which is the safe outcome.
- **Reopen trigger.** The reference adding the same holder check on macOS (then this
  becomes parity, not a divergence). Or a legitimate macOS holder that
  `KERN_PROCARGS2` cannot read, reported as a failed handover.
- **Not the only identity gate, and the other one is parity.** This entry covers the
  run-dir lock's eviction path alone. The host cleaner's `retireAbandoned` has its own
  gate, re-reading the pid's identity before its SIGTERM, and that one is classified as
  parity: the reference does not signal a pid whose identity no longer matches, in
  `19f30c46` and `90fca6e6` alike. That reading is from those builds, not
  probe-measured, unlike this entry's own macOS measurement. Do not read the two as one
  divergence.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Run-dir lock. Also `daemon_runlock_unix.go`
  (`holderSignalRefusal`), `daemon_runlock_darwin.go` (`realIsServeCmdline`,
  `procArgv`), `daemon_runlock_linux.go`.

### D16 · `git.status` of a linked worktree returns the status on Windows, where the reference errors (Windows) { #d16 }

- **Behavior.** `git.status` takes a `baseRepo` and a `path` that names a linked
  worktree of that repo, and returns the porcelain status of the worktree. Both the
  reference and claustrum build status in an isolated temp gitdir. That gitdir is a
  fresh `GIT_DIR`
  with the worktree's `HEAD` and `index`, `GIT_COMMON_DIR` at the shared repo, and
  `--work-tree` at the worktree. On Linux and macOS the two are byte-identical. On
  Windows the reference returns `-32603 "exit status 128"` for a linked worktree,
  and claustrum returns the status result (`{"isRepo":true,"clean":false,"changes":[…]}`).
- Outcome measured 2026-09-06, and re-verified on `19f30c46` 2026-09-13. The
  mechanism is not yet pinned. The Windows divergence is measured: on a Windows VM the reference
  returns `-32603 "exit status 128"` for a linked-worktree `git.status` across runs,
  and claustrum returns the status result
  (`scratch/osparity/results/win-*-4534d86.json`). The 2026-09-13 re-probe ran the same
  case against the `19f30c46` reference on a Windows VM: it again returns
  `-32603 "exit status 128"` for a linked-worktree `git.status`, while claustrum returns
  `{"isRepo":true,"clean":false,"changes":[" M f.txt"]}`. A main-checkout control
  returned the identical `{"isRepo":false,"clean":false}` on both binaries. So D16 is
  still-needed on `19f30c46`, not moot (`scratch/slice8/prerelease/d16probe.ps1`). The
  exact reason the reference's
  assembly fails on Windows is not yet determined. An earlier hypothesis (a hardcoded
  `/tmp` temp-gitdir path) is contradicted: the reference respects `$TMPDIR`, so its
  temp gitdir is `<os-temp>/claude-ssh-gitdir-<random>` and resolves to `%TEMP%` on
  Windows (captured with `scratch/probe/gitargv` under a set `TMPDIR`). claustrum's own
  temp-gitdir assembly, with an OS-valid temp dir, returns the correct status on Windows
  (measured: a valid temp gitdir returns ` M a.txt`). Pinning the reference's Windows
  failure needs a git-argv capture on Windows (the shim used on Linux is POSIX-only). A
  capture was attempted with a Windows `git.exe` shim on the daemon's PATH. But the
  reference self-daemonizes, and the re-executed child does not inherit the shim, so its git
  calls were not intercepted. A capture needs a foreground daemon mode or an injection the
  daemonized child inherits.
- **This is reachable, not an edge.** `git.status.baseRepo` is advertised on Windows,
  and the reference rebuilt `git.status` around session worktrees, so a Windows client
  that runs status on a session worktree reaches this path by design. D16 is a
  deliberate REACHABLE wire divergence, unlike the unreachable rule 3 clause (b) cases.
- **Why diverge (claustrum is more correct).** claustrum reproduces the reference's
  status assembly and returns the correct status on every OS. To match, claustrum must
  reproduce whatever makes the reference's assembly fail on Windows. That failure is
  an error for a
  valid status of a session worktree, the exact operation the worktree rebuild exists to
  serve. That is the D2 and D8 pattern: the reference doing it is not a reason to
  reproduce a break.
- **Cost.** A Windows client that diffs frames against the reference sees a result
  where the reference sends an error. A client that reads the reference error as "no
  repo or no changes" reads claustrum as reporting changes the reference hides.
- **Reopen trigger.** The reference fixing its Windows `git.status` (then this becomes
  parity). Or a decision to put strict 1:1 above correctness, which replaces this entry
  with reproducing the reference's Windows failure so claustrum errors 128 too.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.status`. Also `methods_git.go`
  (`gitStatus`) and `githarden.go` (`hardenedGitStatus`). Evidence in `scratch/osparity/`.

### D17 · An abandoned `lsof` run reads as busy, not idle (macOS) { #d17 }

- **Behavior.** The macOS host cleaner asks `lsof` whether a daemon has a live client.
  That run is bounded, and the last bound gives up on a run that never returns. A run
  that gave up produces no output, and so does a run that finished and saw nothing.
  The reference reads both as "not busy". claustrum keeps them apart: a run it gave up
  on reads as busy, because it is not evidence about the pid at all. Only the
  abandoned case differs. A completed run that found nothing still reads as not busy,
  which is the parity answer. The reference claims in this entry are read from the
  `90fca6e6` darwin builds, not probe-measured: staging the divergent arm needs an
  `lsof` that hangs on a macOS host.
- **Default.** Always-on, macOS only. The divergent arm is not reachable by a slow
  `lsof`, and that is a property of the bounds rather than an intention. The command
  deadline kills a slow run, and the wait delay bounds its pipe. Such a run therefore
  completes with no output well before the abandon bound. Reaching the abandon arm
  needs a process `SIGKILL` cannot end. So on every honest path the two builds agree,
  and the arm that differs is a wedged mount.
- **Why always-on (rule 3 clause (a)).** The reference's version is unrecoverable
  harm: `retireAbandoned` reads not-busy as permission to continue, and after the
  identity re-check it SIGTERMs the daemon. On a host where `lsof` cannot answer, that
  ends a session that is in fact serving a client. The loss is that session's state,
  which extends rule 3's list rather than sitting inside it, the same extension
  [D15](#d15) made and the same reason. Clause (a)'s other half holds on the bound gap
  in **Default** above: no honest caller reaches the arm at all, so none can observe
  the difference. It is the same shape as
  [D15](#d15), which refuses to act on an identity the daemon cannot verify. It is
  also the same shape as two spares the reference itself already has. It spares a
  daemon whose
  lock state it cannot determine, and an orphan whose descriptors it cannot inspect.
  The busy probe is the one lsof-backed read where the reference does not apply its
  own rule. The lock spare is long-established here. The descriptor spare is read
  from the `90fca6e6` builds rather than probe-measured. It is an argument for
  D17 rather than a premise this entry rests on, because the clause-(a) case stands
  on the harm alone.
- **Cost.** A daemon whose `lsof` keeps failing is never retired by the busy gate, so
  its run dir stays until `lsof` works again. That is the conservative direction, and
  it matches what the cleaner already does for an unreadable lock. It also costs a
  second wedged run. Reading the first as busy keeps the sampler going. Its
  two-sample minimum means claustrum abandons twice on such a host, where the reference
  abandons once. Each abandoned run leaves a process and a goroutine behind, so the
  leak on this path doubles. The reference caps neither, and neither does claustrum.
- **Not covered: the lock read.** `hcLockHeldAt` still reads an abandoned run as
  not-held, matching the reference, although the same argument applies to it. This
  entry was scoped to the busy predicate deliberately. Widening it is a decision, not
  an implementation detail.
- **Reopen trigger.** The reference distinguishing the two empty results (then this
  becomes parity). Or an operator reporting a run dir the cleaner will not tidy
  because `lsof` keeps failing on that host.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Host cleaner. Also `hostclean_darwin.go`
  (`runLsof`, `hcBusy`), `hostclean.go` (`hcSettledBusy`, `retireAbandoned`).

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach { #ct-1 }

- `process.spawn` / `process.reattach` accept an optional `"wantPid":true`. The
  reply then gains `pid` (the child's OS pid) and `startTime`. This is the first
  wire-surface *extension*. D1 by contrast changes an install-path behaviour.
- The default path is byte-identical. When `wantPid` is absent or false,
  `omitempty` omits both fields, and the frame is exactly the old
  `{"success":true}` / `{found,running,firstSeq,lastSeq,stdinApplied}`. The fields live on a
  dedicated `spawnResult` struct, so they can never leak into the `successResult`
  that `process.stdin` / `process.kill` share.
- `startTime` is an opaque daemon token. It is the daemon's epoch-seconds wall
  clock captured at spawn, and the daemon returns it identically on spawn and
  reattach for the same id. Use it to detect PID reuse and orphans. It is not
  OS-comparable: do not equality-check it against psutil `create_time`.
- The extension is tolerant in both directions: an older daemon ignores the param,
  and an older client never sees the fields. A client can therefore send `wantPid`
  unconditionally. The sibling clauster client pins the contract.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`process.spawn` + `process.reattach`),
  and `results.go`.

### CT-2 · Opt-in `-keep-children` serve flag { #ct-2 }

- A `-serve` flag, off the wire: it changes no method, no frame and no capability.
  At the default (off), graceful shutdown kills the whole child tree. When set, it
  leaves spawned children running so they survive a daemon restart/upgrade, and it
  logs one line with the surviving count. The new daemon does not re-adopt them. An
  out-of-band consumer reconciles them through the CT-1 `pid`/`startTime`.
- Caveat: survivors lose their stdio. The daemon-side pipe ends die with the
  daemon: the child sees EOF on stdin, and a later write gets SIGPIPE/EPIPE.
  Therefore only children that tolerate dead stdio genuinely survive.
- **POSIX-only.** On Windows a Job Object confines the children, and the OS
  terminates that Job Object on daemon exit in any case. claustrum therefore ignores
  the flag and prints a startup warning (`honorKeepChildren`).
- **Activate:** `-keep-children` or the `keep-children` key.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`-serve` flags).

### CT-3 · Opt-in `claustrum.conf` config file { #ct-3 }

- A single place to turn deviations on. Absent ⇒ stock. This is an optional
  `key = value` file, read from the directory that holds the binary. If the file is
  missing, unreadable, non-regular, or malformed, claustrum behaves as a stock
  replica. Every key gates an already-opt-in divergence. The file adds zero new
  dependency (stdlib `bufio` + `strings.Cut`, `#` comments). claustrum ignores
  unknown keys and invalid values, which keeps the format forward-compatible and
  fail-safe. Precedence: explicit CLI flag > config > default.
- Keys mirror the flags: `version-override`, `keep-children`, `metrics-addr`,
  `wire-log`, `wire-log-max-string`, `listen-pipe`, `max-extract-bytes` (D3), `max-cli-bytes` (D10), `cli-probe-timeout`
  (D11), `cli-download-timeout` (D12), `libc-probe-timeout` (D14), `git-timeout`
  (D5), `files-read-regular-only` (D4). Durations use `time.ParseDuration`, which
  rejects a bare number, except zero. Zero parses in unboundedly many spellings and
  always means disabled. No accepted oddity can switch a divergence *on*.
- `version-override` makes claustrum a permanent drop-in. The desktop client
  decides whether to re-upload the daemon: it runs `<bin> --version` on the cached
  `~/.claude/remote/srv/<pinned-sha>/server` and matches the output against the SHA
  it pins. Stock claustrum prints its own version, so the client re-SFTPs the
  reference every session. Set the key to that bare commit SHA. A 40-hex git SHA-1
  is accepted, and a 64-hex value is accepted too. Anything else is a no-op.
  claustrum then prints
  `claude-ssh <sha> (via Claustrum …)`, the client hits the cache, and it stops
  overwriting.
- **Off the wire, off by default.** `-version` is CLI stdout, not a JSON-RPC frame.
  `server.capabilities` still reports claustrum's own version (`server.version`
  was removed in `7d193f89`).
- Fail-safe and hardened: regular-file-only via `Lstat`, `io.LimitReader` ≤ 64 KiB,
  per-key validation (`version-override` gated to
  `^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$` and lower-cased). claustrum uses every
  value as data, never as a format string. Any doubt → stock. Startup never fails.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`-version`), and `config.go`.

### CT-4 · Opt-in hardened token persistence — deferred idea { #ct-4 }

- **Context.** The daemon persists its token to `daemon.token` (`0600`) beside the
  socket, so a client can reconnect. That is parity, and it is on by default
  (`tokenpersist.go`). There are two accepted parity caveats. The file survives an
  unclean kill or crash, because cleanup runs only on graceful shutdown. And on
  Windows, `0600` is not an owner-only DACL.
- **Idea (not built).** A `claustrum.conf` key, for example `persist-token = false`,
  or a Windows owner-only DACL on the file, or both. These are for operators who
  prefer a smaller on-disk
  token window over drop-in reconnect. Must stay absent ⇒ stock.
- **Why deferred.** There is no demand. The default matches the reference, and the
  socket directory is already owner-scoped in the real deployment. We record the
  idea so the security trade-off is not lost.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Token persistence, and `tokenpersist.go`.

### CT-5 · Opt-in `-listen-pipe` Windows named-pipe transport { #ct-5 }

- **Shipped.** `-listen-pipe` makes `-serve` *additionally* serve the exact same
  NDJSON JSON-RPC dispatch over a Windows named pipe, concurrently with the
  `AF_UNIX` socket. Off ⇒ stock. The wire contract, the field ordering and the
  framing stay the same whether a request arrives over the socket or over the pipe.
- **Why.** Without the pipe, a Windows client that cannot consume an `AF_UNIX`
  socket cannot attach. Python `asyncio` is the notable example: its Windows
  Proactor loop natively supports named pipes. This was clauster's ask.
- **Discovery + auth.** claustrum picks the pipe name
  (`\\.\pipe\claustrum-<instance-id>`, client-opaque) and publishes it to `rpc.pipe`
  beside the socket (atomic write before accepting, and claustrum removes it on
  graceful shutdown). Same in-band `"auth"` + `daemon.token` handshake. Owner-only
  DACL (SDDL
  `D:P(A;;GA;;;<current-user-SID>)`), local-only, no new authenticated surface
  ([SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
- **Windows-only:** elsewhere claustrum ignores the flag and prints a warning
  (`honorListenPipe`). A setup failure is non-fatal: the socket still serves.
- **Activate:** `-listen-pipe` or the `listen-pipe` key.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`-serve` flags). Also `pipetransport.go`
  and `pipetransport_windows.go`.

## Candidates considered but not taken

In each of these claustrum can be friendlier than the reference, and matching the
reference costs something real. We record them so the code can point
somewhere durable. No decision is implied, and nothing here is shipped or
scheduled.

- **Conditional `-stop` socket unlink.** `-stop` removes the socket path on every
  exit, even the exit where no daemon answered. That matches the reference (measured on
  three arms, two of them attributing: the live-daemon control says nothing, because
  the daemon removes the socket itself). So `-stop` removes a path that it did not
  create and whose owner it cannot identify. `os.Remove` does not distinguish
  shapes, so a regular file or an empty directory at the `-socket` path goes the
  same way. A `stat`-first variant that removes only a socket is strictly
  safer, and it is a divergence.
- **Fail fast on a missing `-serve` token source.** The check runs in the detached
  child, so the launcher reports its ~10 s accept timeout, and the real reason
  reaches only the child's log. That is reference parity (measured 10.02 s vs
  10.07 s). A parent-side check answered in 0.03 s and named the actual problem: a
  better operator experience, and a divergence.
- **The macOS lock read's abandoned run.** `hcLockHeldAt` reads an `lsof` run it gave
  up on as not-held, matching the reference. The argument behind [D17](#d17) reaches
  it too: a held lock that reads stale lets the tidy remove a live daemon's run dir.
  D17 was scoped to the busy predicate, so this is the same shape one step away.
  Taking it is a second divergence rather than an implementation detail.

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods. The sanctioned exception is
  an optional, gracefully-ignored param whose result fields vanish by default.
  That is the D1 / CT-1 pattern. It leaves the default frame byte-identical, and it
  degrades both ways.

Any of these needs a deliberate, documented protocol version bump.
