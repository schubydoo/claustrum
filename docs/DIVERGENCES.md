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
opt-in (D3, D5, D10, D12, and the retired D11 and D14). D4 is the non-threshold sibling.

*Canonical example:* D2 satisfies both halves. The reference's home-wipe is
unrecoverable data loss, and no honest caller has a legitimate *use* for deleting
home. A caller can still reach that path by accident, which is exactly what the
guard is for.

Clause (b): the trigger is unreachable on an honest path. Five always-on
entries use this form (D6, D8, D9, D18, D19). Each entry has its own trigger,
and the glosses are not interchangeable. Three of the five are asserted rather than
enumerated: nobody ever enumerated Desktop's per-method param set against D9's
binding, and D6 and D18 rest on an observed value plus a measured accepted-set. Read
those three as unenumerated, not established (rule 2 puts the burden on the
divergence). D19 holds for worktrees that either daemon creates, because both
creates refuse the junction. A worktree made outside the daemon can still sit behind
it.

*Canonical example:* D6. A `-cli-version` naming a destructive path outside the
cli-dir is not something any correct client emits.

Clause (c): both binaries fail, and the only delta is diagnostic text.
This clause is deliberately narrow, and we wrote it for D13. D13 did not meet it
when measured, so the clause justifies no entry in this file today. A reader must
not take two things on trust. First, an `error.code` is not diagnostic text,
because a client branches on it. Second, on-disk state that a caller can
`files.stat` is not diagnostic text either. D13's honest-path rows differed in
on-disk state when they were measured. The clause keeps its literal wording and
D13 sits [unresolved](#d13). We did not widen the clause to fit its one
candidate.

*Canonical example:* none currently qualifies.

### The "Desktop owns the argv" premise

Every opt-in tag rests on one claim about the driver: Claude Desktop owns the
daemon's argv on both `-serve` and `-install`. Therefore an operator cannot reach
a flag-only knob, and the `claustrum.conf` key (read beside the executable) is the
reachable one. This claim is the premise under D3, D4, D5, D10, D12 and
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
  boundary. Routing the unlimited case through that arithmetic invents a boundary.
- No opt-in bound is a hang detector. Each bound is a threshold, so an
  honest-but-slow or honest-but-large input trips it too. That is precisely why
  they are off by default.
- At the shipped defaults, each `-install` wall-clock bound in this list is a
  bound of the reference. The direct `--version` run is stopped at 30 s on a cache hit
  and at 120 s after an install in the same run. A managed launcher run is
  stopped at 33 s and at 123 s. The download gives up after 60 s without response
  headers, and after 60 s without a body byte. The `ldd` libc probe has a 5 s
  bound on ldd itself and a 2 s drain after ldd exits.
  [PROTOCOL.md](PROTOCOL.md) → `-install` names the measurement behind each one.
  One clock is claustrum's own choice and is not measured: the wait of up to 2 s
  for the output of a stopped launcher run.
  The stdlib transport clocks (`net.Dialer{Timeout: 30s}`,
  `TLSHandshakeTimeout: 10s`) apply on `-cli-url` only. They are always-on,
  unnumbered, and unprobed on the reference.

## Catalog

| ID | What it does | Default | How to activate | Why (rule / clause) | Reopen trigger |
|----|--------------|---------|-----------------|---------------------|----------------|
| [D2](#d2) | Refuse a destructive path that is or contains `$HOME`, on three methods and on `-install` | always-on | always-on | rule 3 clause (a) | an honest caller legitimately targeting a path that is/contains home |
| [D3](#d3) | Cap `files.extract_tar` output size | off (`0` = unlimited) | `-max-extract-bytes` / `max-extract-bytes` | rule 4 (who-pays) | operator's cap refuses a legit extraction, or default lets a bomb through |
| [D4](#d4) | `files.read` refuses non-regular files | off | `-files-read-regular-only` / key | rule 4 | opt-in refuses a legit read, or default parks/OOMs the daemon in normal use |
| [D5](#d5) | Deadline on every `git` invocation | off (`0`) | `-git-timeout` / key | rule 4 | opt-in kills an honest slow git |
| [D6](#d6) | `-cli-version` must be a single path component | always-on | always-on | rule 3 clause (b) | Desktop passing a multi-component `-cli-version` |
| [D8](#d8) | Never follow or write a foreign or symlinked `remote-server.log`. Both halves hold on Linux and macOS only | always-on | always-on | rule 3 clause (b): unreachable on the deployed path | a shared socket dir that also needs the log file, or the reference adding the same refuse-to-follow |
| [D9](#d9) | Namespace-wide params binding (type error in an unread field → `-32602`) | always-on | always-on | rule 3 clause (b) | a real client sending a type-mismatched unread namespace field |
| [D10](#d10) | Cap `-install` CLI size (decompressed + download body) | off (`0`) | `-max-cli-bytes` / key | rule 4 (who-pays) | Desktop ceasing to treat a disk-full message as terminal |
| [D12](#d12) | Bound on the `-install` download exchange | off (`0`) | `-cli-download-timeout` / key | rule 4 | operator with the bound set reporting an honest slow download failed |
| [D13](#d13) | Verify checksum before decompressing (`-cli-url`, and `-cli-zst` with a checksum) | always-on | always-on | **UNRESOLVED**: clause (c) written for it, measured not met | any change to how Desktop classifies `cliError` |
| [D15](#d15) | Verify a run-dir lock holder is our serve process before signalling it, in the serve eviction and in `-stop` (macOS) | always-on | always-on | rule 3 clause (a) | the reference adding the same macOS check, or a macOS holder legitimately un-inspectable via `KERN_PROCARGS2` |
| [D16](#d16) | `git.status` of a linked worktree returns the status on Windows, where the reference errors `exit status 128` (cause: `core.excludesFile=NUL` in its status call, when the user has no global excludes file) | always-on (Windows) | always-on | claustrum-more-correct (D2/D8 pattern). **REACHABLE** | the reference fixing its Windows git.status, a Git for Windows release that accepts `NUL` there, or a decision to reproduce its failure for strict 1:1 |
| [D17](#d17) | An abandoned `lsof` run reads as busy, not idle (macOS) | always-on (macOS) | always-on | rule 3 clause (a) | a measurement that shows the reference distinguishing the two empty results, or an operator reporting a run dir the cleaner will not tidy because `lsof` keeps failing |
| [D18](#d18) | `-cli-version` must not start with `.blob-` | always-on | always-on | rule 3 clause (b) | Desktop passing a `-cli-version` that starts with `.blob-` |
| [D19](#d19) | `git.worktree_remove` refuses a junction at `.claude` or `.claude\worktrees`, where `f6010b97` answers success and deletes only the branch, and `89cb6289` does the same for a branch that another ref reaches (Windows) | always-on (Windows) | always-on | rule 3 clause (b): the create of both daemons refuses that junction. Maintainer decision of 2026-09-27 | the reference refusing the junction or deleting through it, or a Windows client that depends on the success reply |
| [D20](#d20) | Wait 50 ms and read again before the group `SIGKILL` of a child-group leader that reads as gone, at the reap of a `-serve` start (Linux and macOS) | always-on (Linux and macOS) | always-on | rule 3 clause (a) | a measurement that shows the reference waiting before that `SIGKILL`, or a report of a child that outlived a restart because it replaced its program |
| [D21](#d21) | A second daemon on a live socket appends to `remote-server.log`, where `89cb6289` truncates it and loses the earlier lines of the first daemon (Windows) | always-on (Windows) | always-on | Maintainer decision of 2026-10-02. No frame, reply or exit status differs. A reader of the log file sees the kept lines | a measurement that shows the reference keeping those lines, or a reader of the log that needs the file to start with the lines of the second daemon |
| [CT-1](#ct-1) | Opt-in `wantPid` → `pid` + `startTime` on spawn/reattach | off (fields omitted) | caller sends `"wantPid":true` | sanctioned optional-param extension | — (additive, degrades both ways) |
| [CT-2](#ct-2) | `-keep-children` leaves the child tree running on shutdown | off | `-keep-children` / `keep-children` key | off-wire opt-in extension | — |
| [CT-3](#ct-3) | `claustrum.conf` config file | absent ⇒ stock | create the file | the opt-in mechanism itself | — |
| [CT-4](#ct-4) | Hardened token persistence | not built (deferred idea) | — | deferred | — |
| [CT-5](#ct-5) | `-listen-pipe` Windows named-pipe transport | off | `-listen-pipe` / `listen-pipe` key (Windows) | additive opt-in transport | — |

Tags: **opt-in** = operator-declinable (flag + config key). **conditional** =
activated by the caller. No current entry is conditional. **always-on** = no switch. The CT block uses
"opt-in" in the looser sense of "off unless somebody asks for it". CT-1 is
caller-activated and CT-3 *is* the config mechanism, so neither one is
operator-declinable. Only CT-2 and CT-5 carry a flag and a key.

## Entries

### D2 · Refuse a home directory as a destructive path target (always-on) { #d2 }

- **`-install` is guarded too.** The install removes a folder at the CLI path,
  `<cli-dir>/<version>`, as a tree before the new CLI runs. With a cli-dir that
  is the parent of the home folder and a version that is its leaf name, that
  path is the home folder. D6 does not cover that case. claustrum refuses a CLI
  path that is the home folder or contains it:
  `cli path must not be or contain the home directory: "<path>"`. The folder is
  not removed, the blob stays and no CLI runs. The reference removes the home folder
  as a tree and puts the CLI file there. It does so for a folder that contains
  the home folder too. When the new CLI then fails, the folder is gone and
  nothing is left at the path (Linux cell H2, macOS cell HX2L).
  This guard compares folders, not only path texts. The lexical test of the RPC
  paths (`wipesHomeDir`) runs first. Then the guard compares the folder at the
  CLI path with the home folder and with each parent folder of it, by file
  identity. It does that for the home path as given and for its resolved path.
  On the RPC paths `wipesHomeDir` stays lexical.
  Measured with `-cli-zst`. In each cell of the list below claustrum refuses and
  `89cb6289` removes the folder.
  Linux, 39 runs: H1, H2 and H5 once, and three runs each of twelve HS cells.
  HS1, HS2, HS3, HS4 and HS6 have a symlink in the home path or in the cli-dir.
  HS7 has a link that points below the folder. HS8 has a chain of two symlinks,
  and HS9 has a relative symlink. HS10a has a trailing slash on the cli-dir, and
  HS10b has `sub/..` in it. HS11 has a relative `-cli-dir`. In HS12 the home
  path is a bind mount of the real folder. `f6010b97` ran 25 of those 39 runs,
  and it removes the folder in each.
  macOS, 51 runs: HX1, HX2, HX2L, HX5L and HX5 have both paths in one spelling.
  HX1S, HX1R, HX2S and HX2R have the `/tmp` symlink in one of the two paths.
  HXL1 and HXL2 have a home path that is a symlink. HXK1 to HXK5 have another
  letter case, and HXKS and HXKR have it with the `/tmp` symlink. HXB has a link
  that points below the folder. HXTS has a trailing slash on the cli-dir, and
  HXDD has `..` in it.
  Windows, 38 runs: HX1b and HX2, and three runs each of HJ1 to HJ8 and HJ10 to
  HJ13. The HJ cells are a junction in the home path or in the cli-dir, a
  directory symbolic link and another letter case. They are also an 8.3 short
  name, a junction that points below the folder, a chain of two junctions and a
  `subst` drive.
  A folder at the CLI path that is not the home folder and does not contain it
  is replaced, as on the reference. The control cells are H3, HS5 and HS13 on Linux,
  HX3, HXC, HXKC and HXKC0 on macOS, and HX3, E11b, HJ9 and HJ14 on Windows.
  With no source flag neither binary removes anything.
  A second mount of the same folder is measured in one shape, on Linux: the home
  path is the mount point, and the final name is the mount source (HS12). No
  cell covers the reverse shape or a mount of a parent folder. No cell covers a
  home folder that does not exist, a hard link, or Unicode normalization forms.
  No cell covers a case-sensitive volume on macOS or a UNC path. Not measured:
  `-cli-url`.
  This guard and `wipesHomeDir` take the home folder from `os.UserHomeDir`. That
  is the `HOME` variable, or `USERPROFILE` on Windows. With that variable unset
  or empty they refuse nothing. This follows from the code and is not measured.
- **Behavior.** Three methods hand a caller-supplied, `~`-expanded path to
  a recursive delete. `files.extract_tar` wipes `destDir`. `git.worktree_remove`
  deletes `worktreePath` itself, through an `os.Root` on its parent. A locked
  worktree is refused, not deleted. When `git.worktree_create` rolls back a
  worktree, it deletes `worktreePath`. A rollback follows a caller `timeoutMs`
  that expired during a successful add, the checkout or the copy step. It also
  follows a post-checkout drain that exceeded the caller `timeoutMs`, and a failed
  read-tree checkout. That rollback deletes the leaf's entries, then the empty
  leaf. After a failed `git worktree add`, create only removes an empty leaf, with
  an rmdir. On every rollback arm the guard is defense-in-depth behind create's
  own containment. `wipesHomeDir` (`homeguard.go`)
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
  refusal comes before git, the delete, or `wipesHomeDir` is reached. Every `~`-expanded home path is such a path. On that method's
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
  by accident, which is the point. The same holds for `-install`: no honest
  install replaces the home folder with the CLI file, and the delete is not
  recoverable.
- **Not a security boundary.** The socket + token already grant `process.spawn`
  ([SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
  This guard stops the accidental, generated, or mistyped path. On the RPC
  paths it does not resolve symlinks. The `-install` guard compares folders by
  identity.
- **Reopen trigger.** An honest caller legitimately naming a destructive target
  that *is or contains* a home directory.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `files.extract_tar` and `git.worktree_remove` (the `git.worktree_create` guard emits no frame). Also `homeguard.go` and
  `homeguard_test.go` (`wipeDestDir` seams the destructive call, so the suite is
  safe against an unfixed tree). For `-install`: `stageAndInstall` in
  `install.go`, and [PROTOCOL.md](PROTOCOL.md) → `-install` → Staging and
  cleanup. Measurement: forensics.

### D3 · Make the `files.extract_tar` size cap opt-in { #d3 }

- **Behavior.** `maxExtractBytes` caps extraction output. An over-cap extraction
  returns `{"success":false,"fileCount":0,"error":"extraction size limit exceeded"}`
  and removes the truncated entry.
- **Default.** `0` = unlimited = byte-identical. **Activate:** `-max-extract-bytes
  <n>` or the `max-extract-bytes` key. The disabled state bypasses `io.LimitReader`
  (`io.Copy(out, tr)`).
- **Why opt-in.** Measured, the reference completes a 629 MB extraction with no cap
  at `5db5e4a`. That is a frame, not an unbounded wait, so a non-zero default fails an
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
  `gitCtx` across `git` / `gitStdoutErr` and the hardened helpers). That method runs
  no `git worktree remove`. On `git.worktree_remove` no D5 kill leads to a
  delete. A kill only refuses or skips a step. If the worktree directory exists, a
  hit on the config or repository check answers the lock-check refusal. For a gone
  worktree, the hit gives the lock-check text with the hooks refusal, or skips the
  registration step. With `worktreeRoot` a hit answers the work-tree refusal (`cannot
  determine the repository's work tree`), and so does a hit on `rev-parse
  --show-toplevel` or the first `worktree list` call. If the D5 deadline stops the
  second `worktree list` call, the answer is `cannot list the repository's worktrees:
  signal: killed`. If the deadline expires in the listing before that call, the text
  ends with `context deadline exceeded`. That call runs only after the first one
  failed. The second text has no test. A D5 stop of a call of the branch step keeps
  the branch, and the reply adds `"branchKept":true`. In a `git.worktree_create`
  rollback such a stop adds an undo text instead. It is one of the branch-step texts
  in PROTOCOL.md → The branch step.
  On `git.list_branches` a hit surfaces as `-32603 signal: killed`. On `git.status`
  the frame of a hit depends on the call that it stops: a hit in one of the four
  commands surfaces as `-32603 signal: killed`. The frames of a hit in the other
  calls are from the code and not measured.
  In `git.status` one deadline covers the 16 git calls of the answer, or 18 when
  `HEAD` names no commit (from the code). It covered 2 calls before.
  A killed repo-detection call answers `isRepo:false`. A killed
  `branch --show-current` in `git.info` leaves out the `branch` member. A killed
  `defaultBranch` verify there gives `defaultBranch` `""`. On Windows a killed root
  pair of `git.info` falls back to the walk root. A killed first listing of a method
  runs `git version`, and the method answers by that failure class.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-git-timeout
  <dur>` or the key. The disabled state bypasses `context.WithTimeout`.
- **Never read a timeout as "git refused."** On `git.worktree_remove` no D5 kill
  leads to a delete. `git.worktree_create` does: its rollback deletes after a failed or
  killed read-tree checkout, and it removes an empty leaf after a failed add. A caller
  that deletes on a git failure must tell the deadline apart from the verdict of git
  first. The cap is also softer than it reads on the
  general git sites. `CombinedOutput`/`Output` waits on git's output pipe. So a git
  that leaves a surviving child stays blocked past the deadline on `git.status`,
  `git.list_branches` and the repo probes. Those paths are unmeasured for a
  descendant-orphan case, so the drain is recorded rather than capped there. The one
  measured exception is `git.worktree_create`'s read-tree checkout, which can run a
  smudge/hook filter that backgrounds a pipe-holding descendant. That path alone
  caps the post-exit drain at a fixed ~5 s from git's own exit. It also SIGKILLs
  the process group (`hardenedGitCheckout` / `worktreeCreateDrainCap`).
  That reproduces `4534d86`. When the caller `timeoutMs` exceeds that drain, the
  answer is success. Otherwise the answer is
  `errorCode:"timeout"` ("deadline expired after the checkout finished", no
  `signal: killed`) with the worktree rolled back. Measured in
  `scratch/probe/wt-success-lingering-4534d86.md`. This is parity, not a divergence.
- **One opted-in arm loses data silently, on either seeding pass.** `populateWorktree`
  runs two seeding passes, `copyWorktreeIncludes` (`worktreecopy.go`) and
  `copyClaudeDir` (`worktreeclaude.go`), and both are best-effort. A killed call loses what
  that call lists, and the error is dropped. Therefore `git.worktree_create`
  still answers `{"success":true}` with files missing. Each git call gets its own deadline
  from `gitCtx`, so a kill loses one pass and not the other: the manifest copy can
  succeed while the `.claude/` seed is lost, or the other way round. The `.claude/`
  pass has no manifest precondition. It runs git on every create where `.claude/`
  holds a child other than `worktrees`. A killed `git version` selects the full
  scan. No frame moves. This arm is absent at the
  default.
- **`git.worktree_create` under both deadlines.** The add and the read-tree
  checkout run under the shared deadline. A D5 hit on the add answers `git
  worktree add failed: …` with `errorCode:"worktree_add_failed"`. That is the same
  failure arm as any other git error, and it is distinct from the 4534d86
  caller-`timeoutMs` arm
  (`errorCode:"timeout"`). A D5 hit on the read-tree checkout is a failed checkout.
  It answers `git worktree add failed (checkout): …` with
  `errorCode:"worktree_add_failed"`, and the create rolls back. A D5 hit on
  `rev-parse --show-toplevel` or `worktree list` leaves the checkout tests of a
  `worktreeRoot` without that answer. The root-chain step still refuses a root that
  has, or lies below, a `.git` entry. A D5 hit on one of
  the `sourceBranch` steps counts as a failed step. It can change the
  start commit. If both candidate lookups are killed, the HEAD fallback runs. The
  echoed `sourceBranch` then becomes the current branch, or is omitted on a
  detached HEAD. When a
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
  `worktree_remove`, outside the branch step. That step has its own bounds on both
  sides since `89cb6289` (PROTOCOL.md → The branch step). An honest 61 s git was
  never measured. The deadline cleared
  clause (a)'s not-a-frame half, but the `-32603 signal: killed` arm is an honest
  caller observing the difference (rule 4).
- **Reopen trigger.** An operator with `-git-timeout` set reporting an honest slow
  git killed by it. The `-32603` arm makes a single report enough.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove` and
  `git.list_branches`. Also `methods_git.go`, `worktreecopy.go` and
  `worktreeclaude.go`.

### D6 · `-cli-version` must name a single path component (always-on) { #d6 }

- **Behavior.** The install's clearing step is an `os.RemoveAll` of the CLI
  path, `<cli-dir>/<version>` (`<version>.exe` on Windows), so a version that
  reaches outside the cli-dir deletes unrelated data. claustrum answers `cli version "…"
  must be a single path component` and touches nothing. That rule runs on the install path. A regular file that
  is already at the joined path is first run with `--version`, as the cache-hit
  check. A run that passes answers `"cliWasPresent":true` with that path, as
  on the reference. That is measured with `-cli-version ../x` on Linux against
  `f6010b97` and `89cb6289`, and on macOS against `89cb6289`. A run that is
  stopped answers the unresponsive text and sweeps the cli-dir. D6 answers only
  when that run fails, or when no file is there. The reference has no such rule.
  With `../x` and a blob it installs to `<parent>/x`, outside the cli-dir, and
  runs that file. That is cell V1c on Linux, macOS and Windows. It refuses `.`, `..`, and
  both `/` and `\` on every OS, so the accepted set does not change with the
  platform.
- **A single component rather than a lexical containment check.** A lexical check
  accepts `link/1.0.0` (an intermediate symlink under the cli-dir, followed at open
  time). If this path ran an `EvalSymlinks` check, that check adds only a TOCTOU window before the `RemoveAll`. A
  final component that is itself a symlink stays legal, because `os.RemoveAll`
  unlinks it rather than follows it. So the rule is narrower than "no symlinks".
- **Why always-on.** Rule 3 clause (b). Measured, the reference destroys the
  target on `../victim`. The real client passes bare version
  strings (`1.0.86`, a commit sha, `latest`, all measured accepted). The evidence is
  an observed value plus a measured accepted-set, not an enumeration.
- **Reopen trigger.** Desktop passing a `-cli-version` that is not a single path
  component.
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
  log and falls back to inherited stdio. In both cases, on Linux and macOS,
  claustrum never follows the link or writes into a file it does not own.
- **Windows.** After a failed rename and a failed create, the Windows launcher
  opens an existing regular `remote-server.log` for append. That is the path of
  [D21](#d21). A path that is not a regular file is refused. The type test and the
  open are two calls, and the open has no owner test. Both halves of this entry
  therefore hold on Linux and macOS only. This follows from the code and is not
  measured.
- **The upstream state changed, and this entry was revised to match it.** Measured
  2026-09-06: `4534d86` no longer plain-truncates a foreign *regular* file. `5db5e4a`
  did, measured 2026-08-06. claustrum now matches the `.old` rotation on the common
  path. But in a root-owned sticky directory the reference still FOLLOWS a
  planted `remote-server.log` symlink. It writes its own log into the victim, or it
  refuses to start. claustrum refuses to follow it. So a narrower hardening
  survives, and it is a D2-style case: "the reference does it too" is not a reason
  to call it safe.
- **Hardening, not a defect claim.** To reach it you need a local user who can
  already plant a file or symlink in that directory. The justification is a design
  position: a daemon must not follow a link it did not plant or write into a file
  it does not own. It does not depend on the reference being wrong.
- **Why always-on.** Rule 3 clause (b). The trigger is unreachable on the
  deployed path (`~/.claude/remote/` is per-user, not world-writable). If a flag
  existed, it gates a branch that no honest deployment reaches.
- **Reopen trigger.** A deployment that puts the socket directory somewhere shared
  *and* needs the log file. Or the reference gaining the same refuse-to-follow (then
  this becomes parity, not a divergence).
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Daemon log, and `server.go`
  (`openDaemonLog`).

### D9 · Namespace-wide params binding is stricter than the reference's (always-on) { #d9 }

- **Behavior.** claustrum binds `params` into one struct per namespace
  (`pathParams`, `gitParams`), so a field that is valid for the *namespace* but
  unused by *this* method still participates in decoding. A type-mismatched value
  there answers `-32602`, for example `files.stat {"maxBytes":"{"}`. The reference
  answers that request with defaults, as measured on `f6010b97`. Both binaries
  ignore a genuinely unknown key.
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
  temp in the cli-dir (or `$TMPDIR/claustrum-fetch-<random>` if the cli-dir is
  unwritable) and hashes it in one pass. Therefore "cap off" does not mean unbounded
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

### D12 · Make the `-install` CLI download bound opt-in { #d12 }

- **Behavior.** The download once ran with `http.Client{Timeout: 5m}`. It now runs
  with `Timeout: cliDownloadTimeout`, which defaults to `0`. The bound covers the
  whole exchange, including the body read. A real body over a link that needs six
  minutes therefore trips it exactly as a black hole does.
- **Default.** `0` = no bound (byte-identical). `http.Client{Timeout: 0}` is the
  stdlib's own "no timeout" sentinel. **Activate:** `-cli-download-timeout <dur>` or
  the key.
- **Zero frees the body read, not every clock.** `fetchToFile` uses a clone of
  `http.DefaultTransport`, which still applies `net.Dialer{Timeout: 30s}` and
  `TLSHandshakeTimeout: 10s` on `-cli-url`. A SYN-black-holed host therefore fails
  at 30 s with the bound off. Both clocks are always-on stdlib defaults, unnumbered
  and unprobed on the reference. The wait for the response headers has a 60 s
  limit, always on. That limit is parity, not this divergence: `f6010b97` and
  `89cb6289` give up there too (measured on Linux and Windows). A bound set below
  60 s fires first. No row measures that pair.
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
  connection reset in the body is not a D13 row. It answers
  `download interrupted after <got>/<total> bytes: <err>` on both
  binaries. Measured on a Linux VM against `f6010b97` and `89cb6289`. A body that
  ends before its length is not measured.
- **One more measured effect.** The order also shows without a text delta. The
  body was 4096 bytes that are not a zstd archive, with the right checksum for
  those bytes. Both binaries answer
  `decompressing: invalid input: magic number mismatch`. The reference reports
  `"fetch":{"bytes":4,…}` and prints one progress line. claustrum reports
  `"bytes":4096` and prints two progress lines, because it reads and verifies the
  whole body before it decompresses. Measured on a Linux VM against `f6010b97`
  and `89cb6289`.
- **Why it is unresolved.** We wrote clause (c) for this entry and, measured, it
  did not meet it. On both honest-path rows the reference created an empty
  cli-dir where claustrum created nothing (when the cli-dir did not already
  exist). The delta was therefore not confined to diagnostic text: an empty
  directory is state a caller keeps, and a caller can distinguish it with a
  `files.stat`. The reopen fixture run 2026-08-08 did not meet its condition.
- **That on-disk delta is gone.** claustrum now creates the cli-dir before any
  network access, as the reference does (measured on `f6010b97`). The `-cli-url`
  row was measured again on `89cb6289`, on Linux, macOS and Windows. The body was
  the first half of the blob, with the checksum of the full blob. The texts
  differ as above, and both binaries leave an empty cli-dir. The `-cli-zst` row
  was not run again. Whether D13 now meets clause (c) is open, and D13 stays
  unresolved.
- **The trigger is reachable** (not "an input no honest caller produces", which was
  measured wrong): a bad mirror, a partial upload, or a stale short proxy object is
  undecompressable *and* checksum-mismatched with no adversary. This is *not* the
  generic "flaky network" case: a connection reset answers the same text on
  both binaries, as above.
- **Why still always-on despite being unresolved.** The delta stays cheap because
  neither string is disk-full-shaped. Therefore Desktop (per the `cliError` driver
  claim) classifies both the same way and retries rather than fails terminally. That
  is a claim about a third binary, and the parity harness cannot settle it
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)). If it is
  ever falsified, the cheapness argument fails and this entry owes an opt-in flip.
- **Reopen trigger.** Any change to how Desktop classifies `cliError`. For example,
  distinguishing `checksum mismatch` from a decompression error, or matching either
  as terminal.
- **The `-cli-zst` path too.** If the caller supplies a `-cli-checksum`, claustrum
  hashes the `-cli-zst` blob before it decompresses it. The reference reports a
  decompress failure before a checksum mismatch. So a corrupt blob with a wrong checksum answers
  `checksum mismatch` on claustrum and `decompressing: unexpected EOF` on the
  reference. Both binaries leave an empty cli-dir there. The reference half is
  measured on a Linux VM against `7d193f89` through `f6010b97`.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Staging and cleanup, and `install.go`.
  Ordering, clause-(c), and reopen-fixture tables: forensics.

### D15 · Verify a run-dir lock holder is our serve process before signalling it (macOS) { #d15 }

- **Behavior.** On the run-dir lock's eviction path, before a new `-serve` daemon
  signals a live lock holder, it verifies that the holder is one of our own `-serve`
  processes bound to this socket. On Linux both the reference and claustrum verify the
  holder's command line (claustrum reads `/proc/<pid>/cmdline`). On macOS there is no `/proc`, and the reference does not
  verify the holder before signalling: it sends SIGTERM to the recorded
  pid unverified. Measured on a macOS VM: the reference signals even a lock holder
  that is not a serve process. claustrum instead reads the holder's argument vector
  from `sysctl KERN_PROCARGS2` and refuses to signal a pid whose argv is not our
  `-serve` for this socket.
- **`-stop` too.** `-stop` runs the same holder check before it signals a lock
  holder. On macOS the reference `-stop` signals any live holder whose lock record
  says role `serve` on this host's node. It does not check the holder's command
  line. claustrum does not signal such a holder, and prints `survivor`. Measured
  on a macOS VM against `f6010b97` and `90fca6e6`. Both references sent `SIGTERM`
  to a python holder with a valid `serve` record, and `SIGKILL` 4 s later if it
  ignored `SIGTERM`. claustrum left it alone. On Linux the references also refuse that holder, so Linux is
  parity.
- **Default.** Always-on, macOS only. On the honest path the live lock holder wrote
  its own pid into the record, so the verification passes and the outcome is
  identical to the reference (the predecessor is evicted).
- **Why always-on (rule 3 clause (a)).** Signalling an unverified pid is
  unrecoverable harm. A crash can leave a stale record whose pid is reused by an
  unrelated process. A foreign process can also hold the flock. The reference then
  SIGTERMs that innocent process. No honest caller benefits from skipping the check.
  The same guard is already always-on on Linux (via `/proc`), so macOS matches
  Linux's safety rather than the reference's macOS gap.
- **Cost.** None on the honest path. In the one case where the reference evicts and
  claustrum does not, the holder is un-inspectable or is not a serve process. There
  claustrum serves
  without run-dir ownership instead of killing the holder, which is the safe outcome.
- **Reopen trigger.** The reference adding the same holder check on macOS (then this
  becomes parity, not a divergence). Or a legitimate macOS holder that
  `KERN_PROCARGS2` cannot read, reported as a failed handover.
- **Not the only identity gate.** This entry covers the run-dir lock's eviction path
  and `-stop` alone. claustrum's host cleaner has its own gate in `retireAbandoned`, which
  re-reads the pid's identity before its SIGTERM. That gate is not a numbered
  divergence. On a Linux VM, `f6010b97` and `89cb6289` refused a live listener that is
  not their daemon binary (row HC04). They also refused one that serves another socket
  (row ST06). On that Linux VM claustrum refused both with the same texts. On a
  macOS VM the references refused the listener of row HC04 too. Row ST06 is not
  measured on macOS. In row GSg2 on a macOS VM, claustrum and `89cb6289` refused
  the same listener with the `not our daemon binary` text. Do not read the two as
  one divergence.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Run-dir lock and `-stop`. Also
  `daemon_runlock_unix.go` (`holderSignalRefusal`, `stopRunDirHolder`),
  `daemon_runlock_darwin.go` (`realIsServeCmdline`, `procArgv`),
  `daemon_runlock_linux.go`.

### D16 · `git.status` of a linked worktree returns the status on Windows, where the reference errors (Windows) { #d16 }

- **Behavior.** `git.status` takes a `baseRepo` and a `path` that names a linked
  worktree of that repo, and returns the porcelain status of the worktree. Both the
  reference and claustrum build status in an isolated temp gitdir. That gitdir is a
  fresh `GIT_DIR`
  with the worktree's `HEAD` and `index`, `GIT_COMMON_DIR` at the shared repo, and
  `--work-tree` at the worktree. On Linux and macOS the two are byte-identical. On
  Windows, when the user has no global excludes file, the reference returns
  `-32603 "exit status 128"` for a linked worktree. claustrum returns the status result (`{"isRepo":true,"clean":false,"changes":[…]}`).
- Outcome measured 2026-09-06, and re-verified on `19f30c46` 2026-09-13. On a
  Windows VM the reference returns `-32603 "exit status 128"` for a linked-worktree
  `git.status`, across runs, and claustrum returns the status result. A
  main-checkout control returns `{"isRepo":false,"clean":false}` on both binaries.
  On 2026-09-26 the `f6010b97` reference again answered 128 on a Windows VM. The
  claustrum side of that date is its Windows unit suite, which gets the status.
- **Mechanism, measured 2026-09-26.** A logging git wrapper on a Windows 11 VM
  recorded the cwd, argv and env of every git call of the reference (`f6010b97`).
  Its status call passes `-c core.excludesFile=NUL`, the Windows null device. The
  reference passes that value when the user has no global excludes file. That
  status call was then replayed with Git for Windows 2.55.0, one element changed at
  a time. With `NUL`, `git status` exits 128 with
  `fatal: cannot use NUL as an exclude file`. With `/dev/null`, it prints the
  status. Five other elements did not change the result. They are the working
  directory, the argv order, the slash direction of `GIT_COMMON_DIR` and of
  `commondir`, the temp name, and the listing before the call. In the same replay,
  `git ls-files --others --ignored --exclude-standard` and `git check-ignore`
  accepted `NUL`. The replay is in `scratch/f6010b97/d16-nul-excludes-raw/`.
- **With a user excludes file, measured.** A Windows user with a global excludes
  file gets that file in the reference's status call, not `NUL`. The reference's
  status then works, and D16 does not arise. `89cb6289` shows that on a Windows VM:
  in pass X of the `git.status` rows, row K1 has 22 calls with the user file as
  `core.excludesFile`.
- **The one element that differs on purpose.** claustrum's `git status` call has the
  cwd, argv and env of the reference, apart from the temp dir name and the env order
  on Windows. One value differs on purpose. On Windows claustrum passes
  `-c core.excludesFile=/dev/null` where the reference passes `NUL`
  (`statusExcludesFile` in `githarden.go`). The `ls-files` and `diff-index` calls of
  `git.status` get the same value as its status call. Every other call that passes
  `-c core.excludesFile` passes `NUL` there, as the reference does. On Linux and
  macOS the null device is `/dev/null`, so nothing differs there. A user with a
  global excludes file gets that file on every OS.
- **This is reachable, not an edge.** `git.status.baseRepo` is advertised on Windows,
  and the reference rebuilt `git.status` around session worktrees, so a Windows client
  that runs status on a session worktree reaches this path by design. D16 is a
  deliberate REACHABLE wire divergence, unlike the unreachable rule 3 clause (b) cases.
- **Why diverge (claustrum is more correct).** claustrum reproduces the reference's
  status assembly apart from the excludes value, and returns the correct status on
  every OS. To match, claustrum must pass `NUL` to its `status`, `ls-files` and `diff-index` calls as well. The
  reference's failure is an error for a valid status of a session worktree, the exact operation the worktree rebuild exists to
  serve. That is the D2 and D8 pattern: the reference doing it is not a reason to
  reproduce a break.
- **Cost.** A Windows client that diffs frames against the reference sees a result
  where the reference sends an error. A client that reads the reference error as "no
  repo or no changes" reads claustrum as reporting changes the reference hides.
- **Reopen trigger.** The reference fixing its Windows `git.status` (then this becomes
  parity). A Git for Windows release that accepts `NUL` as an exclude file in
  `git status` (then this becomes parity too). Or a decision to put strict 1:1 above correctness, which replaces this entry
  with reproducing the reference's Windows failure so claustrum errors 128 too. That
  change is one value: `NUL` in the `core.excludesFile` of the `status`, `ls-files`
  and `diff-index` calls. The reference stops at its `status` call with no excludes
  file, so what it passes to the other two there is not measured.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.status`. Also `methods_git.go`
  (`gitStatus`), `gitstatus.go` (`statusChanges`) and `githarden.go` (`statusExcludesFile`).
  Evidence in `scratch/osparity/` and `scratch/f6010b97/d16-nul-excludes-raw/`.

### D17 · An abandoned `lsof` run reads as busy, not idle (macOS) { #d17 }

- **Behavior.** The macOS host cleaner asks `lsof` whether a daemon has a live client.
  That run is bounded, and the last bound gives up on a run that never returns. A run
  that gave up produces no output, and so does a run that finished and saw nothing.
  claustrum keeps them apart. A run it gave up on reads as busy, because it is not
  evidence about the pid at all. A completed run that found nothing reads as not
  busy. The reference side of this entry is not probe-measured. Staging the
  abandoned arm needs an `lsof` that hangs on a macOS host.
- **Default.** Always-on, macOS only. The command
  deadline kills a slow run, and the wait delay bounds its pipe. Such a run therefore
  completes with no output well before the abandon bound. Reaching the abandon arm
  needs a process `SIGKILL` cannot end.
- **Why always-on (rule 3 clause (a)).** The harm it refuses is unrecoverable. If an
  abandoned run reads as not busy, `retireAbandoned` takes that as permission to
  continue. After the identity re-check it SIGTERMs the daemon. On a host where `lsof` cannot answer, that
  ends a session that is in fact serving a client. The loss is that session's state,
  which extends rule 3's list rather than sitting inside it, the same extension
  [D15](#d15) made and the same reason. The other half: a slow `lsof` completes
  before the abandon bound (see **Default**), so no honest caller reaches this arm.
  It is the same shape as
  [D15](#d15), which refuses to act on an identity the daemon cannot verify. It is
  also the same shape as two spares claustrum already has. It spares a daemon whose
  open files it cannot inspect, and an orphan whose descriptors it cannot inspect.
  That is an argument for D17 rather than a premise this entry rests on, because the clause-(a) case stands
  on the harm alone.
- **Cost.** A daemon whose `lsof` keeps failing is never retired by the busy gate, so
  its run dir stays until `lsof` works again. That is the conservative direction, and
  it matches what the cleaner already does for an unreadable lock. It also costs a
  second wedged run. Reading the first as busy keeps the sampler going. Its
  two-sample minimum means claustrum abandons twice on such a host. Each abandoned
  run leaves a process and a goroutine behind. claustrum caps neither.
- **Not covered: the lock read.** `hcLockHeldAt` still reads an abandoned run as
  not-held, although the same argument applies to it. This
  entry was scoped to the busy predicate deliberately. Widening it is a decision, not
  an implementation detail.
- **Linux is outside this entry.** On Linux the busy read comes from `/proc`, not
  `lsof`. If `/proc` cannot read a daemon's descriptors or its `net/unix` table, the
  retire refuses that daemon. The reference's
  retire refusal, captured on the Linux run for a busy daemon, uses one text for both
  cases: "a connection is attached to it right now, or that could not be read". The Linux
  run did not stage an unreadable `/proc`, so neither arm is measured. Neither arm is a numbered divergence, because neither is known to
  differ from the reference.
- **Reopen trigger.** A measurement that shows the reference distinguishing the two
  empty results (then this becomes parity). Or an operator reporting a run dir the cleaner will not tidy
  because `lsof` keeps failing on that host.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Host cleaner. Also `hostclean_darwin.go`
  (`runLsof`, `hcBusy`), `hostclean.go` (`hcSettledBusy`, `retireAbandoned`).

### D18 · `-cli-version` must not start with `.blob-` (always-on) { #d18 }

- **Behavior.** claustrum downloads a `-cli-url` blob to `<cli-dir>/.blob-<random>`.
  The sweep and the `-cli-keep` prune both skip that prefix, so an in-flight blob
  is never deleted or counted. A CLI installed under such a name is never pruned
  or swept either. claustrum therefore answers `cli version "…" collides with the
  install download blob` and installs nothing.
- **Reference side.** The prefix is claustrum's own name. Whether the reference
  installs such a version is not measured.
- **Why always-on.** Rule 3 clause (b). The real client passes bare versions
  (`1.0.86`, a commit sha, `latest`). It is on the same evidence as [D6](#d6).
- **Why not part of D6.** D6 guards a destructive path that leaves the cli-dir.
  D18 guards a name that claustrum itself reserves. The reasons and the reopen
  triggers differ. The number D7 is retired and is not reused.
- **Reopen trigger.** Desktop passing a `-cli-version` that starts with `.blob-`.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`, and `install.go`
  (`isDownloadBlobName`, `validateCLIVersion`).

### D19 · Refuse a junction at `.claude` or `.claude\worktrees` on remove (Windows, always-on) { #d19 }

- **Behavior.** On Windows, `git.worktree_remove` refuses a request whose `.claude` or
  `.claude\worktrees` is a junction. The reply is `{"success":false,"error":"failed
  to remove worktree: openat .claude\\worktrees: path escapes from parent"}`. That
  text is claustrum's own, the error of `os.Root`. Nothing is deleted, and the branch
  stays.
- **Reference side, measured.** Measured against `f6010b97` on a Windows VM (rows
  J03, J04 and J05 for `.claude\worktrees`, row JC for `.claude`). The reference answers `{"success":true}` and deletes nothing.
  It still deletes the branch. When the junction leads to a live worktree, that
  worktree is left on a deleted branch. With no worktree behind the junction, it
  answers `{"success":true}` too (rows JCR1 and JCR2, after the refused create).
  Neither daemon deleted anything outside the fixture. The whole-build check of
  issue 442 measured `89cb6289` the same (Windows rows JC, J03 to J05, JCR1 and
  JCR2), for a branch that another ref reaches.
- **Default.** Always-on, Windows only. **Activate:** always-on. There is no flag
  and no key.
- **Why always-on.** Rule 3 clause (b), by the maintainer's decision of 2026-09-27.
  The `git.worktree_create` of both daemons refuses a junctioned `.claude` or
  `.claude\worktrees` with `mkdir_failed` and creates nothing (reference measured on a
  Windows VM, rows JCR1 and JCR2). So neither daemon creates a worktree there. Only a
  worktree made outside the daemon sits there. For it, claustrum keeps a branch that
  the reference deletes, which on `89cb6289` is a branch that another ref reaches.
- **Cost.** A Windows client that diffs frames against the reference sees
  `success:false` where the reference sends `success:true`. A branch that the
  reference deletes stays with claustrum. A client that relies on the remove to
  delete the branch there must delete it itself.
- **Reopen trigger.** The reference refusing the junction with the same text (then
  this becomes parity), or the reference deleting the tree through the junction. Or a Windows
  client that depends on the success reply for a junctioned `.claude` or
  `.claude\worktrees`.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove`. Also
  `worktreeremove.go` (`openRemoveParent`). Evidence in
  `scratch/i429/remove-val-windows-6bed4ee.md`,
  `scratch/i429/remove-val-windows-8755717.md` and
  `scratch/i429/remove-val-windows-8755717-jcr.md`.

### D20 · Wait for a settle before the group `SIGKILL` of a leader that reads as gone (Linux and macOS) { #d20 }

- **Behavior.** At a `-serve` start the daemon ends the child groups that a dead
  predecessor left: `SIGTERM` to the group, then a wait of 2 s. If the leader of a
  group reads as gone in that wait and the group still answers, the daemon sends
  `SIGKILL` to the group. claustrum first waits 50 ms and reads the leader again. A
  leader that is alive again takes the record test. A leader that fails that test
  gets no more signals. This is off-wire. No frame changes.
- **Reference side, measured.** `89cb6289` sends that `SIGKILL` with no wait.
  - Row RP05a (the leader exits on `SIGTERM`, two members ignore it), Linux, the
    20 runs of the last three run sets: the `SIGKILL` comes 0.3 to 4.9 ms after the
    `SIGTERM`.
  - Row PG05a (a child that replaces its program on `SIGTERM`), Linux, six run
    sets of 10, 10, 10, 30, 10 and 10 runs: `89cb6289` ended the child in 4, 2, 3,
    5, 1 and 5 runs. That is 20 of 80 runs. In the other 60 runs the child stayed
    alive.
  - Row PG05a, macOS, three run sets of 21, 20 and 10 runs: `89cb6289` ended the
    child in no run.
  - Rows where the child ends on `SIGTERM`, Linux: `89cb6289` also sends a group
    `SIGKILL` right after the `SIGTERM`, in 15 of 42 runs of one run set. claustrum
    sends none there and reads the group again after the wait.
- **claustrum side, measured.** With the wait, claustrum ended the child of row
  PG05a in none of 50 Linux runs (the last three run sets) and in none of 51 macOS
  runs. In the Linux set of the build right before the wait, it ended the child
  in 2 of 10 runs.
- **Default.** Always-on, Linux and macOS. **Activate:** always-on. There is no
  flag and no key. The 50 ms is claustrum's own value.
- **Why always-on (rule 3 clause (a)).** The harm it refuses is a `SIGKILL` to a
  live process that is no longer the recorded child. That loss cannot be taken
  back. It is the same shape as [D15](#d15), which refuses to signal an identity
  that the daemon cannot verify. On the honest path the outcome stays inside what
  the rows of `89cb6289` show: in row PG05a the child stays alive, as in 60 of its
  80 Linux runs, and in row RP05a the group still gets its `SIGKILL`. The only
  difference on the honest path is the time of one signal at a daemon start, off
  the wire.
- **Cost.** In row RP05a the group `SIGKILL` comes about 50 ms later. Measured on
  Linux in the same 20 runs: 51 to 55 ms after the `SIGTERM` in 19 runs, and 152 ms
  in one. Measured on macOS in 13 runs of row RP05q (three run sets): the members end
  51 to 80 ms after their `SIGTERM` in 12 runs and 100 ms after it in one, against
  25 ms at most on `89cb6289`. A reap
  whose groups all end on the `SIGTERM` returns one wait later.
- **The wait is a threshold.** A program replacement that takes longer than 50 ms
  still gets the `SIGKILL`. No measured run shows that.
- **Reopen trigger.** A measurement that shows the reference waiting before that
  `SIGKILL` (then this becomes parity). Or a report of a child that outlived a
  daemon restart because it replaced its program.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → process.spawn. Also `childreap.go`
  (`settleGone`, `reapGoneLeader`). Evidence in
  `scratch/89cb6289/e-linux/REPORT.md` and `REPORT-val1.md` to `REPORT-val5.md`
  there, and in `scratch/89cb6289/e-macos/REPORT-val1.md` to `REPORT-val3.md`.

### D21 · A second daemon on a live socket appends to `remote-server.log` (Windows) { #d21 }

- **Behavior.** On Windows a second `-serve` can start on a live socket, and the
  first daemon stays alive. The first daemon holds `remote-server.log` open, so the
  launcher of the second one cannot rotate it. claustrum then opens that file for
  append. If both daemons are this build, each one writes to the end of the file,
  and no line of either one is lost. A first daemon of an earlier build writes at
  its own offset. That follows from the code and is not measured. This is
  off-wire. No frame changes.
- **Reference side, measured.** On a Windows VM (rows WN04 and WJ04), `89cb6289`
  writes to the same file and truncates it at the second start.
  - The 16 lines that its first daemon wrote before that start were lost.
  - The later lines of the first daemon were kept, behind a block of NUL bytes.
  - After both daemons ended, the file held 12 lines.
- **claustrum side, measured.** In the same rows the file kept the 16 lines of the
  first daemon. The later lines of both daemons followed in time order. After both
  daemons ended, the file held 28 lines.
- **Equal in those rows.** The launcher of the second daemon returned at once with
  empty stdout and stderr. Both daemons and the three children stayed alive.
  `-stop` ended the second daemon only.
- **Default.** Always-on, Windows only. **Activate:** always-on. There is no flag
  and no key. On Linux a second daemon evicts the first, and the log rotates to
  `.old` on both sides (rows LOCK-0, LOCK-2, ST03).
- **Why always-on.** The maintainer's decision of 2026-10-02: claustrum keeps the
  lines. No frame, no reply and no exit status differs from `89cb6289` in the
  measured rows. The difference is in the log file only. No clause of rule 3
  covers this entry. It stands on that decision, and the reopen trigger below
  takes it back.
- **Cost.** A reader of the log file sees the difference: it gets the kept lines
  from claustrum. The file holds the lines of two daemons with no mark of which
  daemon wrote a line. It does not start with the start lines of the second
  daemon, as the file of `89cb6289` does.
- **Not measured.** A first daemon of another build beside a second daemon of this
  one. An earlier claustrum build does not write in append mode.
- **Reopen trigger.** A measurement that shows the reference keeping the earlier
  lines of the first daemon (then this becomes parity). Or a reader of the log
  that needs the file to start with the lines of the second daemon.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Daemon log. Also `server.go`
  (`openDaemonLog`) and `detach_windows.go` (`openHeldDaemonLog`). Evidence in
  `scratch/89cb6289/e-windows/REPORT-val4.md`, section 3.3.

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach { #ct-1 }

- `process.spawn` / `process.reattach` accept an optional `"wantPid":true`. The
  reply then gains `pid` (the child's OS pid) and `startTime`. This is the first
  wire-surface *extension*.
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
  At the default (off), graceful shutdown kills the children: the whole tree on
  Linux and macOS, the direct child on Windows. When set, it
  leaves spawned children running so they survive a daemon restart/upgrade, and it
  logs one line with the surviving count. The new daemon does not re-adopt them. An
  out-of-band consumer reconciles them through the CT-1 `pid`/`startTime`.
- Caveat: survivors lose their stdio. The daemon-side pipe ends die with the
  daemon: the child sees EOF on stdin, and a later write gets SIGPIPE/EPIPE.
  Therefore only children that tolerate dead stdio genuinely survive. Not measured
  on Windows.
- **The records of the kept children.** On Linux and macOS a child has a record in
  `<runDir>/children`. The reap of the next daemon start ends each recorded
  child of a daemon that is gone. With the flag, the graceful shutdown removes the
  records of the children that it leaves alive. The next daemon then finds no
  record of them and sends them no signal. This rule is claustrum's own, and the
  reference has no such flag. A Linux VM ran the flag on claustrum (row P14, two
  children, one of them ignores `SIGTERM`). `-stop` sent no kill and removed both
  records. Both children were alive after it, with parent pid 1. A new daemon
  then sent no signal and logged no reap line, with the flag and without it.
- Two limits of that rule. It covers the processes of the process table only. A
  process whose `id` a later spawn took keeps its record, so the next start ends
  it. The host cleaner does not read the records. By its own rule it ends an
  orphaned Claude Code group under the install root, a kept one too. That is not
  measured with the flag.
- **Windows.** The flag works there too. A child is in no Job Object, so the exit
  of the daemon does not end it. On a Windows VM
  (rows WN03, WJ02, WJ05, WJ06), the children of `f6010b97` and `89cb6289` survive a
  killed daemon. With the flag, `-stop` and `server.shutdown` leave the direct child
  alive too. On that VM the whole tree of claustrum was alive at 1 s, 5 s and 15 s
  after both. The reference has no such flag: `89cb6289` exits with code 2 for it.
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
  `wire-log`, `wire-log-max-string`, `listen-pipe`, `max-extract-bytes` (D3), `max-cli-bytes` (D10),
  `cli-download-timeout` (D12), `git-timeout`
  (D5), `files-read-regular-only` (D4). Two more keys are deprecated and set
  nothing: `cli-probe-timeout` (retired D11) and `libc-probe-timeout` (retired
  D14). Durations use `time.ParseDuration`, which
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

## Retired entries

A retired entry is a divergence that a later reference build, or a later
measurement of the reference, made moot. Its number is not reused, and its old
link points here.

### D1 · Verify the `-cli-zst` blob against a supplied checksum (retired) { #d1 }

- Since `7d193f89` the reference checks a supplied `-cli-checksum` against the
  `-cli-zst` blob. `5db5e4a` ignored it. The compare is case-sensitive, so the
  right digest in upper case fails.
- On a mismatch the reference answers `checksum mismatch` and keeps the blob.
  It creates the cli-dir before it opens the blob. So a failed attempt past the
  version check leaves the cli-dir, empty if it was new. The cli-dir half is
  measured on a Linux VM from `5db5e4a`, the mismatch from `7d193f89`. claustrum
  now does all of this, unit-tested.
- The `-cli-url` path follows the same rules on `f6010b97`. The compare is
  case-sensitive, and the cli-dir comes before any network access.
- One delta stays, and [D13](#d13) owns it: claustrum hashes before it
  decompresses.
- Desktop's one captured `-cli-zst` call (2026-08-10) supplied no
  `-cli-checksum`, so no check ran on that path. It is one instance of one
  failure shape.

### D7 · `-cli-version` must not collide with the install temp sweep (retired) { #d7 }

- D7 refused a `-cli-version` such as `.fetch-x` or `1.0.zst`. Its premise was
  that the sweep deletes such a version in the run that installed it. That held
  up to `7d193f89`.
- Since `4534d86` the sweep removes an entry only when its mtime is about 10
  minutes old or more. The reference installs such a version and keeps it. A later
  install removes it once it is that old. A cache hit does not.
- claustrum now does the same. Measured on a Linux VM against `4534d86` through
  `f6010b97`. [D6](#d6) still applies to every version.

### D11 · Deadline on the `<cli> --version` run of `-install` (retired) { #d11 }

- D11 made the deadline on the direct `<cli> --version` run opt-in and off by
  default. Its premise was that the reference has no deadline at or below 90 s.
  That premise is false.
- The reference stops a present CLI that has not answered after 30 s. It stops
  a CLI that it installed in the same run after 120 s. Measured on `f6010b97` and
  `89cb6289`, on Linux, macOS and Windows. A present CLI that answers at 28 s
  passes, and a new CLI that answers at 118 s passes. On Linux and Windows a
  present CLI that answers at 31 s is stopped, and a new CLI that answers at
  121 s is stopped. Each stop came at 30.0 s or at 120.0 s.
- The two older numbers of this entry fit those bounds. A CLI that answers at
  20 s installs, and a CLI that answers at 90 s installs after 91 s. Both are
  under 120 s, the bound of a new CLI.
- The outcome at the bound is not one of the three that this entry named. The
  reference answers a `cli unresponsive: …` `cliError` with
  `"cliUnresponsive":true`. On a cache hit it installs nothing. After an install
  it keeps the new CLI. [PROTOCOL.md](PROTOCOL.md) → `-install` holds the rows.
- claustrum now does the same, always. The bounds are unit-tested with shrunk
  values.
- The `-cli-probe-timeout` flag and its `claustrum.conf` key are deprecated.
  Both are still accepted, both set nothing, and either one logs one warning on
  `-install`.
- Not measured: which reference build first had the two bounds. `f6010b97` has
  them.
- No knob stays to change the bounds. A different bound is a new divergence, and
  rule 2 puts the burden of proof on it.

### D14 · Deadline on the `ldd --version` libc probe (retired) { #d14 }

- Since `19f30c46` the reference bounds the `ldd` process at 5 s. If `ldd` still
  runs then, it kills the whole process group of `ldd` and drops any output. The
  loader glob then decides. No log line is written. The dropped-output row is
  measured on `f6010b97` and `90fca6e6`.
- If `ldd` exits in time, the reference waits up to about 2 s for its output
  pipe, then uses the output. Measured on `f6010b97` for exits up to 4.5 s.
- claustrum now does the same, always. The late-exit case is unit-tested.
- The `-libc-probe-timeout` flag and its `claustrum.conf` key are deprecated.
  Both are still accepted, both set nothing, and either one logs one warning on
  `-install`.
- No knob stays to change the bound. A different bound is a new divergence, and
  rule 2 puts the burden of proof on it. No measurement gives that proof today.

## Candidates considered but not taken

In each of these claustrum can be friendlier than the reference, and matching the
reference costs something real. We record them so the code can point
somewhere durable. No decision is implied, and nothing here is shipped or
scheduled.

- **Conditional `-stop` socket unlink.** After a failed connect, `-stop` removes
  the socket path, unless it prints `survivor`. That matches the reference (measured against `f6010b97` and
  `90fca6e6` on a Linux VM). `os.Remove` does not check the shape of the path. A
  regular file or an empty directory at the `-socket` path is removed too. A
  `stat`-first variant that removes only a socket keeps those two shapes, and it
  is a divergence. It does not change the socket case.
- **Fail fast on a missing `-serve` token source.** The check runs in the detached
  child, so the launcher reports its accept timeout, and the real reason
  reaches only the child's log. That is reference parity (measured 10.02 s vs
  10.07 s on `5db5e4a`). With a zero-byte `-token-file`, the launcher of
  `89cb6289` exited after 12.06 s (Linux row P7, also measured on macOS and Windows), and the bound of claustrum is
  12 s now. A parent-side check answered in 0.03 s and named the actual problem: a
  better operator experience, and a divergence.
- **The macOS lock read's abandoned run.** `hcLockHeldAt` reads an `lsof` run it gave
  up on as not-held. The argument behind [D17](#d17) reaches
  it too: a held lock that reads stale lets the tidy remove a live daemon's run dir.
  D17 was scoped to the busy predicate, so this is the same shape one step away.
  Taking it is a second divergence rather than an implementation detail.

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods. The sanctioned exception is
  an optional, gracefully-ignored param whose result fields vanish by default.
  That is the CT-1 pattern. It leaves the default frame byte-identical, and it
  degrades both ways.

Any of these needs a deliberate, documented protocol version bump.
