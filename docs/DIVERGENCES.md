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
opt-in (D3, D10, D12, and the retired D5, D11 and D14). The retired D4 was the
non-threshold sibling.

*Canonical example:* D2 satisfies both halves. The reference's home-wipe is
unrecoverable data loss, and no honest caller has a legitimate *use* for deleting
home. A caller can still reach that path by accident, which is exactly what the
guard is for.

Clause (b): the trigger is unreachable on an honest path. Three always-on
entries use this form (D6, D8, D19). Each entry has its own trigger,
and the glosses are not interchangeable. D6 is asserted rather than
enumerated: it rests on an observed value plus a measured accepted-set. Read
it as unenumerated, not established (rule 2 puts the burden on the
divergence). D19 holds for worktrees that either daemon creates, because both
creates refuse the junction. A worktree made outside the daemon can still sit behind
it.

*Canonical example:* D6. A `-cli-version` naming a destructive path outside the
cli-dir is not something any correct client emits.

Clause (c): both binaries fail, and the only delta is diagnostic text.
This clause is deliberately narrow, and we wrote it for the retired
[D13](#d13), which did not meet it. The clause justifies no entry in this file
today. A reader must not take two things on trust. First, an `error.code` is not
diagnostic text, because a client branches on it. Second, on-disk state that a
caller can `files.stat` is not diagnostic text either.

*Canonical example:* none currently qualifies.

### The "Desktop owns the argv" premise

Every opt-in tag rests on one claim about the driver: Claude Desktop owns the
daemon's argv on both `-serve` and `-install`. Therefore an operator cannot reach
a flag-only knob, and the `claustrum.conf` key (read beside the executable) is the
reachable one. This claim is the premise under D3, D10, D12 and
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
| [D2](#d2) | Refuse a destructive path that is or contains `$HOME`, on three methods and on `-install` | always-on | always-on | rule 3 clause (a). The `files.extract_tar` wipe through the parent handle and its two tests: maintainer decision of 2026-10-10 | an honest caller legitimately targeting a path that is/contains home, or a Windows `destDir` with the `\\?\` prefix and a dot or a space after its last name |
| [D3](#d3) | Cap `files.extract_tar` output size | off (`0` = unlimited) | `-max-extract-bytes` / `max-extract-bytes` | rule 4 (who-pays) | operator's cap refuses a legit extraction, or default lets a bomb through |
| [D6](#d6) | `-cli-version` must be a single path component | always-on | always-on | rule 3 clause (b) | Desktop passing a multi-component `-cli-version` |
| [D8](#d8) | Refuse a link, a FIFO, and on Linux and macOS another user's `remote-server.log`, at the start. `5fd08069` writes through a link or another user's file and blocks on a FIFO, on macOS and on Linux with the `fs.protected` sysctls off. On Windows it writes through a link. An own link to an own file passes on Linux and macOS. The `git-shadow` sweep makes no lock file in the target of a link | always-on | always-on | rule 3 clause (b), and the maintainer's decision of 2026-10-19 | a shared socket folder that needs the log, the reference refusing these entries on every host, or the reference making no lock file through a link |
| [D10](#d10) | Cap `-install` CLI size (decompressed + download body) | off (`0`) | `-max-cli-bytes` / key | rule 4 (who-pays) | Desktop ceasing to treat a disk-full message as terminal |
| [D12](#d12) | Bound on the `-install` download exchange | off (`0`) | `-cli-download-timeout` / key | rule 4 | operator with the bound set reporting an honest slow download failed |
| [D15](#d15) | Verify a run-dir lock holder is our serve process before signalling it, in the serve eviction and in `-stop` (macOS) | always-on | always-on | rule 3 clause (a) | the reference adding the same macOS check, or a macOS holder legitimately un-inspectable via `KERN_PROCARGS2` |
| [D16](#d16) | `git.status` of a linked worktree returns the status on Windows, where the reference errors `exit status 128` (cause: `core.excludesFile=NUL` in its status call, when the user has no global excludes file) | always-on (Windows) | always-on | claustrum-more-correct (D2/D8 pattern). **REACHABLE** | the reference fixing its Windows git.status, a Git for Windows release that accepts `NUL` there, or a decision to reproduce its failure for strict 1:1 |
| [D17](#d17) | An abandoned `lsof` run reads as busy, not idle (macOS) | always-on (macOS) | always-on | rule 3 clause (a) | a measurement of the reference with an `lsof` run that cannot be killed, or an operator reporting a run dir the cleaner will not tidy on such a host |
| [D19](#d19) | `git.worktree_remove` refuses a junction at any directory between `baseRepo` and the leaf. At `.claude` or `.claude\worktrees`, `f6010b97` answers success and deletes only the branch, and `89cb6289` does the same for a branch that another ref reaches. At a level above `.claude`, `89cb6289` answers success with no leaf and no branch there (Windows) | always-on (Windows) | always-on | rule 3 clause (b): the create of both daemons refuses that junction. Maintainer decisions of 2026-09-27 and, for the levels above `.claude`, of 2026-10-07 | the reference refusing the junction or deleting through it, or a Windows client that depends on the success reply |
| [D20](#d20) | Wait 50 ms and read again before the group `SIGKILL` of a child-group leader that reads as gone, at the reap of a `-serve` start (Linux and macOS) | always-on (Linux and macOS) | always-on | rule 3 clause (a) | a measurement that shows the reference waiting before that `SIGKILL`, or a report of a child that outlived a restart because it replaced its program |
| [D22](#d22) | `git.worktree_remove` refuses a worktree locked in the `.git` folder of `baseRepo`, in four states where `89cb6289` answers success. Two have a daemon `GIT_DIR` and `GIT_COMMON_DIR` of another repository, with the folder present or gone. One has a daemon `GIT_DIR` alone. One has a `baseRepo` that does not exist as sent. With `worktreeRoot`, rows q2 to q4 (Linux VM) differ in the frame too | always-on | always-on | Maintainer decision of 2026-10-03. The frame differs from `89cb6289` in those four states and in rows q2 to q4 | a caller that needs the removal of a locked worktree there, or a measurement that shows the reference refusing there |
| [D23](#d23) | The environment block of a child is in name order (Windows). The reference does not sort it | always-on (Windows) | always-on | Maintainer decision of 2026-10-13: claustrum stays on Go 1.26, which sorts the block | the reference moves to Go 1.26 or newer |
| [D24](#d24) | The rollback of `git.worktree_create` keeps a folder that replaced the new registration. `89cb6289` removes it in cells B6 and B6b (Linux and macOS VMs), and in B6b it holds the registration of a live sibling worktree. On Windows a rollback after `.git` became a file runs no git call (cells X1, X1b and X1c of round `task077-03`, X1 and X1b of round `task077-04`) | always-on | always-on | Maintainer decisions of 2026-10-07 and, for cells X1, X1b and X1c, of 2026-10-19. The frames are equal in cells B6 and B6b (Linux and macOS VMs) and B6w of round `task087-03` (Windows VM), and the disk differs. They differ in cells X1, X1b and X1c. The Windows placement is not measured | a measurement that shows the reference keeping such a folder, a decision to match the disk of cells B6 and B6b, or a decision to run the branch step in the repository that the `.git` file names (cells X1, X1b and X1c) |
| [D25](#d25) | With `CLAUDE_SSH_PEER_CHECK=1`, `server.capabilities` answers `"peerCheck":"unavailable"` and the daemon serves every caller. `5fd08069` answers `"peerCheck":"on","peerCheckBy":["network","pidfd"]` there (Linux VM) | always-on (Linux) | always-on | Maintainer decision of 2026-10-10. The frame differs from `5fd08069` in one of the listed values: `1` on Linux | a client that needs the `"on"` answer on Linux, or a decision to answer as `5fd08069` does there |
| [D27](#d27) | On Windows the stale step of `git.worktree_create` keeps an entry whose `gitdir` record names a path that exists. `5fd08069` deletes the entry of a live worktree `W1` for a create of `w1` in a folder with case sensitivity on (cell W-CS of round `task084-02`, Windows VM) | always-on (Windows) | always-on | The maintainer's decision of 2026-10-18 | a measurement of `5fd08069` with the same test |
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

- **`files.extract_tar` tests the folder that it opened.** `wipesHomeDir` judges
  the `destDir` path as text. The entries and the remove of the `.synced` entry
  act through a handle of `destDir`, which claustrum opens after the wipe and
  the mkdir of the new folder. Another process can replace `destDir` with a link
  or a junction in that time. So
  claustrum tests after the open that `destDir` is still a real folder and is the
  folder of the handle. If not, it answers
  `open destDir: "<name>" changed while it was opened`. It writes no entry and
  removes nothing. claustrum opens that handle through a handle of the parent
  folder. A link that leaves the parent folder fails that open first, with
  `open destDir: openat <name>: path escapes from parent`. This test is part of this entry: it keeps the delete on the
  folder that the guard judged. A request alone cannot stage the swap, and no
  measurement says what the reference does there.
  A `destDir` that is a link or a junction before the request is not refused:
  the wipe removes the link first, as on the reference. The wipe removes the
  last name of `destDir` through a handle of the parent folder, so it follows
  no link and no junction. The maintainer approved this delete path on
  2026-10-10.
- **`files.extract_tar` tests the entry that the wipe acts on.** Before the wipe
  claustrum reads the last name of `destDir` inside the handle of the parent
  folder. Two tests follow, and they run again after the mkdir of the new
  folder. If the views differ there, the entry at that name goes with a plain
  remove, never as a tree.
  First, a folder at the last name that is the home folder, or a parent folder
  of it, gets the home refusal by identity. That refusal comes after the archive
  open, so the archive is gone. On Linux and macOS that covers a path through a
  link above the last name. On Windows the text compare refuses a dot or a
  space after home or after a folder above it, because the path is resolved
  first. It lets a path with the `\\?\` prefix and the short 8.3 name of home
  pass, and the test by identity refuses those. These Windows rows are runs of
  the test binary on a Windows VM, with a temp home and a stubbed wipe. A
  Windows VM also ran them on a daemon, one run each. A link or a
  junction at the name itself is not followed: the wipe removes the link, and
  its target stays. Second, the path as the system
  resolves it and the name inside the handle must name one entry. If not,
  claustrum answers
  `clean destDir: "<name>" is not the entry that the path names`, or the same
  text after `mkdir destDir:`, and deletes nothing. On Windows the two views
  differ for a path with the `\\?\` prefix and a dot or a space after its last
  name. Six cells are measured: each of the two names with `dest` alone, with a
  folder of the exact name too, and with neither. `89cb6289` extracts into the
  folder with the exact name, which it makes if there is none. `5fd08069`
  extracts into the folder with no dot or space, which it makes if there is
  none (Windows VM). A seventh cell is the last name `...` behind the prefix.
  `89cb6289` makes a folder with that exact name and extracts there.
  `5fd08069` answers `clean destDir: RemoveAll ...: invalid argument`.
  claustrum answers the text of the two views, and the tree is equal with
  `5fd08069` (Windows VM). claustrum refuses, because the home guard compares
  the text of the path. These tests are for
  this method. They do not cover the other callers of `wipesHomeDir`.
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
  The sweeps and the `-cli-keep` prune of `-install` keep the same guard
  (`cliEntryHoldsHome`) as defense in depth. On `5fd08069` they remove regular
  files only (cells L4, L8, M1, M3a, W3 and W5), so a home folder stays there
  on both binaries (from those cells, no home cell on `5fd08069`). `89cb6289` removed an empty home folder there (cells H1 and
  H3).
  This guard and `wipesHomeDir` take the home folder from `os.UserHomeDir`. That
  is the `HOME` variable, or `USERPROFILE` on Windows. With that variable unset
  or empty they refuse nothing. This follows from the code and is not measured.
- **Behavior.** Three methods hand a caller-supplied, `~`-expanded path to
  a recursive delete. `files.extract_tar` wipes `destDir`. `git.worktree_remove`
  deletes `worktreePath` itself, through an `os.Root` on its parent. A worktree
  locked in the `.git` folder of `baseRepo` is refused, not deleted. When `git.worktree_create` rolls back a
  worktree, it deletes `worktreePath`. A rollback follows a caller `timeoutMs`
  that expired during a successful add, the checkout or the copy step. It also
  follows a post-checkout drain that exceeded the caller `timeoutMs`, and a failed
  read-tree checkout. It also follows a failed placement of the
  index of the new worktree. That rollback deletes the leaf's entries, then the empty
  leaf. After a failed `git worktree add`, create only removes an empty leaf, with
  an rmdir. On every rollback arm the guard is defense-in-depth behind create's
  own containment. `wipesHomeDir` (`homeguard.go`)
  refuses any target that is or contains the home directory. Descendants stay
  allowed, because extracting into `~/.claude/…` is the daemon's own install path.
  On Windows a create also removes old `.claude-new-worktree-*` entries of the git
  directory of `baseRepo`. `5fd08069` does so in cell 48 of round `s3p2b-edges3`
  (Windows VM). `wipesHomeDir` runs on each such entry before its removal, and a
  refused entry stays. That guard is claustrum's own there: no cell of `5fd08069`
  has such an entry that holds the home folder. The two staging removes of a
  create, of its staging folder and of a moved registration, run the guard too.
  The deletes inside the folder
  `git-shadow` of the daemon run the same guard on the full path.
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
- **Pointers.** The page [files.extract_tar](protocol/files-extract-tar.md), and [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove` (the `git.worktree_create` guard emits no frame). Also `homeguard.go` and
  `homeguard_test.go` (`wipeDestDir` seams the destructive call, so the suite is
  safe against an unfixed tree). For `-install`: `stageAndInstall` and
  `cliEntryHoldsHome` in `install.go`, and [PROTOCOL.md](PROTOCOL.md) → `-install` → Staging and
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
- **Pointers.** The page [files.extract_tar](protocol/files-extract-tar.md) and
  `methods_files.go`. Measurement: forensics.

### D6 · `-cli-version` must name a single path component (always-on) { #d6 }

- **Behavior.** The install's clearing step is an `os.RemoveAll` of the CLI
  path, `<cli-dir>/<version>` (`<version>.exe` on Windows), so a version that
  reaches outside the cli-dir deletes unrelated data. claustrum answers `cli version "…"
  must be a single path component`. It answers before
  the install writes or removes anything at a path built from the version. The
  two sweeps of the cli-dir name only the cli-dir, and they are not behind this
  rule. From the code: the `<version>.zst.part` sweep runs before the rule
  answers, and the 10 minute sweep runs after the refusal. claustrum answers the refusal, writes nothing and keeps the blob. `89cb6289`
  installs a file at `<parent>/x`. That rule runs on the install path. A regular file that
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

### D8 · claustrum refuses a link, a FIFO or another user's `remote-server.log`, and the `git-shadow` sweep writes nothing through a link (always-on) { #d8 }

- **Behavior.** If the rename to `remote-server.log.old` fails, claustrum's launcher
  judges the entry at the log path by `Lstat`. It truncates an own regular file and the
  own regular file behind an own link. A folder fails the start with `is a directory`,
  and a failed create with nothing at the path fails it with the create error. These
  follow `5fd08069` in rounds `task015-03` and `task015-04`: cells E1 and E3 on the
  Linux and macOS VMs, E5 and E6 on the Linux VM.
- **What differs.** Every other entry fails the start with `claustrum: open log: open
  <path>: permission denied` and exit 1, and it stays unchanged. Round `task015-04`
  measured that refusal in cells S1 to S4 (Linux VM) and S1, S3 and S4 (macOS VM).
  `5fd08069` refuses the same in a sticky world-writable folder with the `fs.protected`
  sysctls on. On macOS, and on Linux with those sysctls off, it writes through a link
  or another user's file and blocks on a FIFO.
- **Why.** The maintainer's decision of 2026-10-19. In round `task015-03` `5fd08069`
  truncated a file of another user and the target of a planted link.
- **Not measured.** The target of an own link must be an own regular file with one
  link. An own file with a second hard link is refused. Both are claustrum's own. An
  own file that the open cannot write fails the start with the error of that open.
- **Windows.** The launcher truncates a regular file with no owner test, as `5fd08069`
  does for a held log (cells W1 and W3 of round `task015-01`). It refuses a file
  symbolic link, where `5fd08069` writes through it (cell W2 of round `task015-04`,
  Windows VM). The rename moves a folder onto a regular `.old` on both (cell W1).
- **git-shadow sweep.** A symbolic link in `git-shadow` with a folder name of this host
  goes with one plain remove. On Windows a junction goes the same way. Nothing is
  written in its target. `5fd08069` makes an empty lock file in the target. Cells G10
  of rounds `task075-01` and `task075-02` show it on the Linux and macOS VMs. Cells
  G10 and G10j of the same rounds show it on the Windows VM.
- **Why always-on.** Rule 3 clause (b). The deployed socket folder
  (`~/.claude/remote/`) is per-user and not world-writable. So no honest start
  reaches the refusal, and no honest sweep meets a planted link.
- **Reopen trigger.** A deployment that puts the socket folder somewhere shared, the
  reference refusing these entries on every host, or the reference making no lock
  file through a link.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Daemon log, `server.go`
  (`openDaemonLog`), `detach_unix.go` (`openHeldDaemonLog`) and `gitshadowsweep.go`
  (`sweepLink`).

### D10 · Make the `-install` CLI size cap opt-in { #d10 }

- **Behavior.** `maxCLIBytes` governs two reads: the decompressed CLI
  (`decompressing: decompressed CLI exceeds <n> bytes`) and the HTTP download body
  (`download failed: response exceeds <n> bytes`).
- **Default.** `0` = unlimited (byte-identical). **Activate:** `-max-cli-bytes <n>`
  or the key. When disabled, both call sites bypass their `LimitReader`.
- **claustrum streams the body and never buffers it.** It decompresses the
  download into the staging file and hashes it in the same pass.
  Therefore "cap off" does not mean unbounded memory. The figure of 886 MB → 10 MB
  on a 400 MiB payload was the old two-step path. The streamed decode is not
  measured.
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
  (which removes the cost).
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) and `install.go` (`fetchCLI`,
  `zstdDecompress`). RSS and cap-below-free-space tables: forensics.

### D12 · Make the `-install` CLI download bound opt-in { #d12 }

- **Behavior.** The download once ran with `http.Client{Timeout: 5m}`. It now runs
  with `Timeout: cliDownloadTimeout`, which defaults to `0`. The bound covers the
  whole exchange, including the body read. A real body over a link that needs six
  minutes therefore trips it exactly as a black hole does.
- **Default.** `0` = no bound (byte-identical). `http.Client{Timeout: 0}` is the
  stdlib's own "no timeout" sentinel. **Activate:** `-cli-download-timeout <dur>` or
  the key.
- **Zero frees the body read, not every clock.** `fetchCLI` uses a clone of
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
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) and `install.go` (`fetchCLI`). Straddle
  and stall tables, confounders: forensics.

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

- **Behavior.** `git.status` with a `baseRepo` and a `path` that names a linked
  worktree returns the porcelain status of that worktree. On Linux and macOS the
  reference and claustrum are byte-identical. On Windows, when the user has no
  global excludes file, the reference returns `-32603 "exit status 128"`.
  claustrum returns the status result (`{"isRepo":true,"clean":false,"changes":[…]}`).
- **Measured.** Windows VMs gave that error on `19f30c46` and on `f6010b97`. On
  `5fd08069`, with no user excludes file, 18 of 18 requests
  answer `exit status 128` (Windows VM).
- **Mechanism, measured on `f6010b97`.** The status call of the reference passes
  `-c core.excludesFile=NUL`, the Windows null device. With that value, `git status`
  of Git for Windows 2.55.0 exits 128 with `fatal: cannot use NUL as an exclude file`.
  With `/dev/null` it prints the status.
- **With a user excludes file.** The reference passes that file, not `NUL`. Its
  status then works, and D16 does not arise (`89cb6289`, Windows VM, row K1).
- **The one value that differs.** On Windows claustrum passes
  `-c core.excludesFile=/dev/null` to the `status`, `ls-files` and `diff-index`
  calls of `git.status` (`statusExcludesFile` in `githarden.go`). Every other call
  that passes `-c core.excludesFile` passes `NUL` there, as the reference does.
- **Why diverge.** The path is reachable: a Windows client that runs status on a
  session worktree gets it by design. The reference answers an error for a valid
  status there. That is the D2 and D8 pattern. D16 stays by the maintainer's
  decision of 2026-10-13.
- **Cost.** A Windows client that diffs frames against the reference sees a result
  where the reference sends an error.
- **Reopen trigger.** The reference fixing its Windows `git.status`. A Git for
  Windows release that accepts `NUL` there. Or a decision to answer 128 as the
  reference does: that change is the value `NUL` in those three calls. The
  reference stops at its `status` call, so its value for the other two is not
  measured there.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.status`. Also `gitstatus.go`
  (`statusChanges`) and `githarden.go` (`statusExcludesFile`).

### D17 · An abandoned `lsof` run reads as busy, not idle (macOS) { #d17 }

- **Behavior.** The macOS host cleaner asks `lsof` whether a daemon has a live client.
  That run is bounded, and the last bound gives up on a run that never returns. A run
  that gave up produces no output, and so does a run that finished and saw nothing.
  claustrum keeps them apart. A run it gave up on reads as busy, because it is not
  evidence about the pid at all. The reference side of this entry is not probe-measured.
- **Default.** Always-on, macOS only. The command
  deadline kills a slow run, and the wait delay bounds its pipe. Such a run therefore
  completes with no output well before the abandon bound. Reaching the abandon arm
  needs a process `SIGKILL` cannot end.
- **Why always-on (rule 3 clause (a)).** The harm it refuses is unrecoverable. If an
  abandoned run reads as not busy, `retireAbandoned` takes that as permission to
  continue. After the identity re-check it SIGTERMs the daemon. Where an `lsof` run cannot be killed, that
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
- **Cost.** A daemon whose `lsof` cannot be killed is never retired by the busy gate, so
  its run dir stays until `lsof` works again. That is the conservative direction. It also costs
  three wedged runs on such a host: one for the pass line and two in the sampler. Each abandoned
  run leaves a process and a goroutine behind. claustrum caps neither.
- **A run that the 15 s deadline ends is outside this entry.** The lock read then
  keeps the run dir, and the busy read does not retire the daemon. `5fd08069` does
  both under an `lsof` that hangs (cells L1, L2, B1 and B2, macOS VM). No staging
  reached the 17 s arm.
- **Only the abandoned run is this entry.** Two other `lsof` outcomes are parity
  with `89cb6289` on a macOS VM, 3 of 3 runs each: a command that does not start,
  and a run that writes to stderr and exits 1. In both, the daemon gets no SIGTERM,
  and a run dir with a lock file stays.
- **Linux is outside this entry.** On Linux the busy read comes from `/proc`, not
  `lsof`. If `/proc` cannot read a daemon's descriptors or its `net/unix` table, the
  retire refuses that daemon. The reference's
  retire refusal, captured on the Linux run for a busy daemon, uses one text for both
  cases: "a connection is attached to it right now, or that could not be read". The Linux
  run did not stage an unreadable `/proc`, so neither arm is measured. Neither arm is a numbered divergence, because neither is known to
  differ from the reference.
- **Reopen trigger.** A measurement of the reference with an `lsof` run that cannot
  be killed. Or an operator reporting a run dir the cleaner will not tidy
  on such a host.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Host cleaner. Also `hostclean_darwin.go`
  (`runLsof`, `hcBusy`), `hostclean.go` (`hcSettledBusy`, `retireAbandoned`).

### D19 · Refuse a junction between `baseRepo` and the leaf on remove (Windows, always-on) { #d19 }

- **Behavior.** On Windows, `git.worktree_remove` refuses a request with a junction
  at any directory between `baseRepo` and the leaf. The reply is
  `{"success":false,"error":"failed to remove worktree: openat <path from
  baseRepo>: path escapes from parent"}`. `<path from baseRepo>` is the folder that
  holds the leaf, relative to `baseRepo`. It is `.claude\\worktrees` in the frame
  for a junction at `.claude` or at `.claude\worktrees`. That text is claustrum's
  own, the error of `os.Root`. From the code: claustrum opens that folder in one
  `os.Root` step. Nothing is deleted, and the branch stays.
- **Reference side, measured.** Measured against `f6010b97` on a Windows VM (rows
  J03, J04 and J05 for `.claude\worktrees`, row JC for `.claude`). The reference answers `{"success":true}` and deletes nothing.
  It still deletes the branch. When the junction leads to a live worktree, that
  worktree is left on a deleted branch. With no worktree behind the junction, it
  answers `{"success":true}` too (rows JCR1 and JCR2, after the refused create).
  Neither daemon deleted anything outside the fixture. The whole-build check of
  issue 442 measured `89cb6289` the same (Windows rows JC, J03 to J05, JCR1 and
  JCR2), for a branch that another ref reaches.
- **Reference side, a level above `.claude`.** Two cells of `89cb6289` on a
  Windows VM have a `baseRepo` P with no junction and a junction `<P>\J`. The
  `worktreePath` is `<P>\J\sub\.claude\worktrees\w1` in cell J-b-wt-pj and
  `<P>\J\.claude\worktrees\w1` in cell J-b-wt-pJ. The leaf did not exist, and no
  branch existed. `89cb6289` answers `{"success":true}` after 2 git calls.
  claustrum answers `openat J\\sub\\.claude\\worktrees: path escapes from parent`
  and `openat J\\.claude\\worktrees: path escapes from parent` in the frame, with
  no git call. Nothing changes on disk on either side. Not measured: a live
  worktree behind such a junction, and the branch step there.
- **Default.** Always-on, Windows only. **Activate:** always-on. There is no flag
  and no key.
- **Why always-on.** Rule 3 clause (b), by the maintainer's decision of 2026-09-27.
  The `git.worktree_create` of both daemons refuses a junctioned `.claude` or
  `.claude\worktrees` with `mkdir_failed` and creates nothing (reference measured on a
  Windows VM, rows JCR1 and JCR2). So neither daemon creates a worktree there. Only a
  worktree made outside the daemon sits there. For it, claustrum keeps a branch that
  the reference deletes, which on `89cb6289` is a branch that another ref reaches.
  The maintainer widened the entry on 2026-10-07 to a junction at any directory
  between `baseRepo` and the leaf. The create of both daemons refuses there too.
  Both answer `failed to create parent directory: <P>\J is not a directory` with
  `mkdir_failed` in cells J-b-wt-pj and J-b-wt-pJ, with equal frames.
- **Cost.** A Windows client that diffs frames against the reference sees
  `success:false` where the reference sends `success:true`. A branch that the
  reference deletes stays with claustrum. A client that relies on the remove to
  delete the branch there must delete it itself.
- **Reopen trigger.** The reference refusing the junction with the same text (then
  this becomes parity), or the reference deleting the tree through the junction. Or a Windows
  client that depends on the success reply for a junction between `baseRepo` and
  the leaf.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove`. Also
  `worktreeremove.go` (`openRemoveParent`). Evidence in
  `scratch/i429/remove-val-windows-6bed4ee.md`,
  `scratch/i429/remove-val-windows-8755717.md`,
  `scratch/i429/remove-val-windows-8755717-jcr.md` and
  `scratch/i429/s33-windows-junction/raw/table.md`.

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

### D22 · Refuse a worktree locked in the `.git` folder of `baseRepo` on remove (always-on) { #d22 }

- **Behavior.** `git.worktree_remove` refuses a worktree that is locked in the
  `.git` folder of `baseRepo`, and deletes nothing. Since `7d193f89` the reference refuses it
  too, with the same text. In four states `89cb6289` answers success for a
  worktree S that is locked in T, the `.git` folder of `baseRepo`. claustrum
  refuses in each of them. On Linux and macOS a gone worktree whose locked entry sits behind a linked `<git dir>/worktrees` gets success, and its branch goes. That follows `5fd08069` (cell L1 of round `tier0-26`, Linux VM), by the maintainer's decision of 2026-10-14.
- **Reference side, measured.** 3 of 3 runs on each system named:
  - Row p6 (Linux, macOS, Windows). The daemon has `GIT_DIR` and `GIT_COMMON_DIR`
    of another repository X. `89cb6289` answers `{"success":true}` and deletes the
    folder of S. The entry of T with its `locked` file stays.
  - Row p6e (Linux, macOS, Windows). The daemon has `GIT_DIR` of X alone.
    `89cb6289` answers `{"success":true}` and deletes the folder of S.
  - Row p6d (Linux, macOS). `baseRepo` is `<T>/missing/..` with no folder
    `missing`, with git on `PATH`. `89cb6289` answers
    `{"success":true,"branchKept":true}` and deletes the folder of S.
  - Row p6f (Linux, macOS, Windows). The environment of row p6, and the folder of
    S is gone. `89cb6289` answers `{"success":true}` and deletes nothing.
- **claustrum side, measured.** In rows p6, p6e and p6d claustrum answers the
  locked refusal of [PROTOCOL.md](PROTOCOL.md) and deletes nothing. In row p6f it
  answers the "is gone but its registration is locked" refusal. Those are the
  rows of build `87f2326` on the same VMs. claustrum at
  `2dc9d85` refused in rows p6, p6d and p6f too, and deleted the folder in row p6e.
- **With `worktreeRoot`, measured.** On a Linux VM, 3 runs each, a worktree made
  by the daemon beneath a root and locked in T. Row q2 has a daemon `GIT_DIR` of X
  alone, and row q3 has `GIT_DIR` and `GIT_COMMON_DIR` of X. In both `89cb6289`
  answers the transient `could not verify` text of [PROTOCOL.md](PROTOCOL.md) and
  deletes nothing. Row q4 is row q2 with the folder gone: `89cb6289` answers
  `{"success":true}` and deletes nothing. claustrum build `afd4ff0` answers the
  locked refusals in rows q2, q3 and q4 and deletes nothing. The reference deletes
  nothing there either. The end state is the same on both sides, and the frame
  and the git calls differ.
- **Equal in the rows beside them.** Row p6b puts the lock on an entry of X whose
  record names S: both sides refuse. Rows p6c (Linux, macOS and Windows VMs) and
  p6g (Linux VM) have no daemon environment: both sides refuse. Rows q1, q5 and q5b
  (Linux VM) have no daemon environment either. Row q1 is equal: a lock with a
  `worktreeRoot`. Rows q5 and q5b are equal in frame and end state: a
  `T/.git/worktrees` folder of mode 000, with and without `worktreeRoot`.
- **How.** If the removal reads its entries from another git directory,
  claustrum also reads the entries under `<baseRepo>/.git/worktrees`, with no git
  call, with and without `worktreeRoot`. If that folder exists and cannot be read,
  claustrum answers the lock-check refusal and deletes nothing. A folder that does
  not exist counts as not locked.
- **Default.** Always-on, on every system. **Activate:** always-on. There is no
  flag and no key.
- **Why always-on.** The maintainer's decision of 2026-10-03: claustrum refuses a
  worktree that is locked in the `.git` folder of `baseRepo`. A lock is the user's own instruction not to delete,
  and a deleted folder cannot be taken back. No clause of rule 3 covers this
  entry. Rule 4 says that a change to a frame on an honest path is opt-in or does
  not ship. The maintainer decided on 2026-10-03 to accept this frame difference.
  Each of the four states needs a state that the request does not create: a
  `GIT_DIR` in the daemon's environment, a `baseRepo` spelled through a missing
  name, or a folder removed by hand. No measurement shows a client that reaches
  one of them. The entry stands on that decision, and the reopen trigger below
  takes it back.
- **Cost.** In the four states, and in rows q2 to q4, the frame differs from
  `89cb6289`: claustrum sends `success:false` with a locked text. In rows p6, p6e
  and p6d the folder stays. A caller there must unlock the worktree first.
- **Not measured.** The reference on Windows in row p6d: Windows refuses that
  `baseRepo` on both sides. The reference with `GIT_DIR` of X, `GIT_COMMON_DIR` of
  a third repository Y and a lock in T. The reference with a daemon environment and
  a `<baseRepo>/.git/worktrees` folder that cannot be read: claustrum answers the
  lock-check refusal there.
- **Not covered.** A `baseRepo` with no `.git` folder of its own: a subfolder of T,
  a linked worktree or a submodule. With a daemon `GIT_DIR` of another repository,
  claustrum does not read the lock in the real repository there. It deletes the
  locked worktree, as the reference does in row p6. The maintainer decided on
  2026-10-03 to keep that, and the reference is not measured there.
- **Reopen trigger.** A caller that needs the removal of a locked worktree in
  these states. Or a measurement that shows the reference refusing there (then
  this becomes parity).
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove`. Also
  `worktreeremove.go` (`lockedInBaseRepo`) and `methods_git.go`. Evidence in
  `scratch/i429/s2-linux/REPORT-rev1.md`, and in `REPORT-val3.md` under
  `scratch/i429/s2-linux`, `s2-macos` and `s2-windows`, and in
  `scratch/i429/s2-linux/REPORT-val4.md`.

### D23 · The environment block of a child is in name order (Windows) { #d23 }

On claustrum the Windows environment block of a child is in name order. The
reference does not sort it. Go sorts the block since 1.26. The reference uses
go1.25.14.

Windows VM, 2026-10-13: a claustrum build with go1.25.14 matched `5fd08069` in
the order. Counts: 18 of 18 `process.spawn` blocks with no `env`, 487 of 487
git children in the order of names, 18 of 18 at `-install`. The Go 1.26.9
build sorted every cell.

claustrum stays on Go 1.26 (maintainer's decision, 2026-10-13). Go 1.26.9 fixed
standard library advisories of go1.25.14, one in the folder handles of the
Windows file guards (GO-2026-6604).

Reopen trigger: the reference moves to Go 1.26 or newer.

### D24 · In the create rollback, keep a folder that replaced the new registration and run no git call after a `.git` file swap { #d24 }

- **Behavior.** `git.worktree_create` tests the registration
  of the new worktree right after the add. When the tests accept it, claustrum
  takes the identity of the registration folder. A rollback deletes the folder at
  that path only if it still has that identity. After a failed `rev-parse
  --show-toplevel` the rollback deletes the one new entry of the listing after
  the add, with its identity from that listing. In cell P2 of round `tier0-13`
  (Linux VM) `5fd08069` deletes a sibling registration that was moved to that
  name during the call, and claustrum keeps it. The test runs on the
  registrations directory that the delete opened, so the test and the delete
  use one registrations directory.
  claustrum holds the accepted folder open until the create answers.
  While that handle is open, a new folder does not get that identity.
  If the open fails, the identity comes from a stat of the path (not measured).
  The rollback deletes nothing if the folder at the path is not the accepted
  one at that test. It adds no text to the frame, and the
  branch step runs. The placement of the index has the same test. claustrum
  places nothing if the folder at the path is not the accepted one at that
  test. It then answers a
  failed checkout with its own text, `the registration <entry> is not the folder
  that was tested after the add`.
  On Windows the same tests guard the registration that the staging step moved,
  with the identity of the staging folder held open from its mkdir to the
  return of the move. The text there is `the registration <entry> is not the
  folder that was moved into place`. In cell B6w of round `task087-03`
  (Windows VM) `5fd08069` deletes the folder that took the name, claustrum
  keeps it, and the frames are equal. The placement there is not measured.
- **Reference side, measured.** A Linux VM with git 2.43 ran `89cb6289` on
  2026-10-07, 2 runs per cell. A macOS VM with git 2.50 ran it too, 2 runs per
  cell, with the same disk. A git wrapper changes the registrations folder of
  `baseRepo` and then fails the read-tree.
  - Cell B6. The wrapper renames the new registration `w1` to `w1x` and makes a
    new empty folder `w1`. `89cb6289` removes the empty `w1` and keeps `w1x`.
  - Cell B6b. The wrapper renames `w1` to `w1x`, and the registration `w9` of a
    live sibling worktree to `w1`. `89cb6289` removes that `w1` with the files
    of the sibling, and keeps `w1x`.
- **claustrum side, measured.** claustrum removes no folder of the
  registrations directory in both cells (Linux and macOS VMs, 2 runs per cell).
- **Equal in those cells (Linux and macOS VMs).** The frame is the plain failed checkout on both
  sides. The leaf and branch `w1` are gone on both sides. In cell B6b the folder
  of the sibling worktree and its branch stay on both sides.
- **A `.git` file after the add (Windows), measured.** Cells X1, X1b and X1c
  of round `task077-03` ran on a Windows VM, 2 runs each. Round `task077-04`
  ran X1 and X1b again with the same bytes. There `.git` becomes a file
  `gitdir: <the git directory of a second repository>` right after the add.
  `5fd08069` runs the branch step in that repository. In X1 that repository has
  no branch `w1`, and `5fd08069` answers with no undo text. In X1b it
  deletes its branch `w1`, which `main` reaches. In X1c it keeps it with
  `branchKept`. claustrum runs no git call after the swap and changes nothing
  in that repository. Its undo text says that branch `w1` still exists. The
  frames differ in all three cells. The maintainer decided on 2026-10-19 to
  keep this.
- **Default.** Always-on. **Activate:** always-on. There is no
  flag and no key.
- **Why always-on.** The maintainer's decision of 2026-10-07: the guard stays.
  The harm that it refuses is the loss of the registration of another worktree,
  as in cell B6b. The decision of 2026-10-19 adds cells X1, X1b and X1c. There
  the harm is a branch deleted in the repository that a `.git` file names, as
  in cell X1b. No clause of rule 3 covers this entry. It stands on that
  decision, and the reopen trigger below takes it back.
- **Cost.** In cells B6 and B6b a folder stays that `89cb6289` removes: the
  empty `w1` of cell B6, and the files of the sibling under the name `w1` in
  cell B6b. No frame differs there. In cells B6 and B6b a create reaches the
  guard only if the registrations folder changes between the add and the
  rollback. In cells X1, X1b and X1c the frame differs, and in X1b a branch
  stays that `5fd08069` deletes.
- **Not measured.** The guard at the placement of the index: no cell has a
  replaced folder with a read-tree that exits 0. A replaced folder with a
  daemon `GIT_DIR` of another repository is not measured either. Cells B9 and
  B9b have that environment with no replaced folder: the registration that git
  made in that repository is gone after the rollback on `89cb6289` (Linux and
  macOS VMs). If git gave no answer
  to `rev-parse --absolute-git-dir`, the guard does not run.
- **Reopen trigger.** A measurement that shows the reference keeping such a
  folder. Or a decision to match the disk of cells B6 and B6b. Or a decision
  to run the branch step in the repository that the `.git` file names (cells
  X1, X1b and X1c).
- **Pointers.** Evidence in
  `scratch/i429/s37-linux-rollback-probe/REPORT.md` and in `raw/cells/B6.md` and
  `raw/cells/B6b.md` beside it. The macOS round is in
  `scratch/i429/s40-macos-rollback-val/REPORT.md`. The rounds of the final build are in
  `scratch/i429/s44-linux-rollback-last/REPORT.md` and
  `scratch/i429/s45-macos-rollback-last/REPORT.md`. Cells X1, X1b and X1c are in
  `scratch/tier1/task077/round03-windows/REPORT.txt`.

### D25 · Answer `"unavailable"` for the peer check and serve every caller (Linux) { #d25 }

- **Behavior.** claustrum has no peer check. A daemon that starts with
  `CLAUDE_SSH_PEER_CHECK=1` in its environment answers `"peerCheck":"unavailable"`
  in `server.capabilities`, with no `peerCheckBy` member. It writes one line
  before the listening line, at WARN level, and it serves every caller. That
  holds on Linux, macOS and Windows. The divergence is on Linux only.
- **Reference side, measured.** A Linux VM ran `5fd08069` with
  `CLAUDE_SSH_PEER_CHECK=1` in the daemon environment. `server.capabilities`
  answers `"peerCheck":"on","peerCheckBy":["network","pidfd"]`. Before the
  listening line the log holds one more line:
  `[Server] serving only callers in "user:[4026531837] net:1 pidfd"` (the number
  is the one of that VM). A client of the same user on that VM got its answers.
- **claustrum side, measured.** On the Linux VM the frame ends with
  `"peerCheck":"unavailable"`, and the log holds the line
  `[Server] CLAUDE_SSH_PEER_CHECK is set: this build has no peer check on Linux, serving all callers`
  after the level tag.
- **Equal.** With no variable and with the value `0`, `5fd08069` answers
  `"peerCheck":"off"` (Linux, macOS and Windows VMs). claustrum answers the same
  on those VMs. With the value `1`, `5fd08069` answers
  `"peerCheck":"unavailable"` on the macOS VM and on the Windows VM, and
  claustrum answers the same there. The text of its log line after the level
  tag equals the line of `5fd08069` there. `89cb6289` has no `peerCheck` member.
  The other measured values are in [PROTOCOL.md](PROTOCOL.md) → server.*.
- **Default.** Always-on, Linux only, and only with `CLAUDE_SSH_PEER_CHECK=1` in
  the daemon environment. **Activate:** always-on. There is no flag and no key.
- **Why always-on.** The maintainer's decision of 2026-10-10: the difference
  stays. No clause of rule 3 covers this entry. Rule 4 says that a change to a
  frame on an honest path is opt-in or does not ship. The maintainer decided on
  2026-10-10 to accept this frame difference. The entry stands on that decision,
  and the reopen trigger below takes it back.
- **Cost.** A client that reads `"unavailable"` on Linux learns that no check
  runs. An operator who sets the variable on Linux gets a daemon that serves
  every caller, where `5fd08069` answers `"on"`.
- **Not measured.** Whether `5fd08069` serves each caller on Linux with the
  value `1`. Each measured caller ran as the same user on the same VM. Which
  client sets the variable is not measured either.
- **Reopen trigger.** A client that needs the `"on"` answer on Linux. Or a
  decision to answer as `5fd08069` does there.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → server.* and Daemon log.

### D27 · Keep a stale entry whose record names a path that exists (Windows) { #d27 }

- **Behavior.** On Windows the stale step of `git.worktree_create` compares the
  `gitdir` record of each entry with the new leaf without regard to letter case.
  Before it deletes an entry that matched, it tests the path that the record
  names, as written, with `Lstat`. If that path exists, the entry stays. Both
  forms of the step have the test: through the held git directory and by the
  path. Linux and macOS have an exact compare, so the missing leaf already
  proves that the entry is stale there.
- **claustrum side, measured.** In cell W-CS of round `task084-03` (Windows VM,
  3 of 3 runs, build `3fbc96a`) `.git\worktrees\W1` stays, and `W1` stays on
  its branch. The create of `w1` answers
  `{"success":false,"error":"git worktree add failed: Preparing worktree (new branch 'b1') fatal: '<repo>/.claude/worktrees/w1' is a missing but already registered worktree; use 'add -f' to override, or 'prune' or 'remove' to clear","errorCode":"worktree_add_failed"}`.
  The branch `b1` stays, because git makes it before it refuses. `5fd08069`
  answers success there.
- **Reference side, measured.** In a folder `.claude\worktrees` with case
  sensitivity on, a live worktree `W1` and a create of `w1`: `5fd08069`
  deletes `.git\worktrees\W1` before the add. `W1` then reads the records of
  `w1`, and its own branch is no longer checked out (cell W-CS of round
  `task084-02`, Windows VM, 3 of 3 runs). claustrum `e7bb806` did the same.
- **Default.** On-wire, always-on, Windows only. **Activate:** always-on.
- **Why always-on.** The reference destroys the records of a live worktree in a
  state that a client can reach. The maintainer's decision of 2026-10-18.
- **Cost.** The frame of cell W-CS above. None where the folder of a stale
  entry is gone (from the code). A `Lstat` that fails with another error than
  "does not exist" keeps a stale entry too, and then the add fails.
- **Reopen trigger.** A measurement that shows `5fd08069` with the same test.
- **Pointers.** [git.worktree_create](protocol/git-worktree-create.md#differences-by-system).

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach { #ct-1 }

- `process.spawn` / `process.reattach` accept an optional `"wantPid":true`. The
  reply then gains `pid` (the child's OS pid) and `startTime`. This is the first
  wire-surface *extension*.
- The default path is byte-identical. When `wantPid` is absent or not `true`,
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
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`process.spawn`), the page
  [process.reattach](protocol/process-reattach.md), and `results.go`.

### CT-2 · Opt-in `-keep-children` serve flag { #ct-2 }

- A `-serve` flag, off the wire: it changes no method, no frame and no capability.
  At the default (off), graceful shutdown kills the children: the whole tree on
  Linux and macOS, the direct child on Windows. When set, it
  leaves spawned children running so they survive a daemon restart/upgrade, and it
  logs one line with the surviving count. The new daemon does not re-adopt them. An
  out-of-band consumer reconciles them through the CT-1 `pid`/`startTime`.
- A child whose start returns after the shutdown began is ended also with the flag
  (from the code, `TestLateStartEndedWithKeepChildren`). Its client got no reply,
  so no client has its `pid`.
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
  `cli-download-timeout` (D12). Four more keys are deprecated and set
  nothing: `files-read-regular-only` (retired D4), `git-timeout` (retired D5),
  `cli-probe-timeout` (retired D11) and `libc-probe-timeout` (retired D14). Durations use `time.ParseDuration`, which
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
measurement of the reference, made moot. The maintainer also retires an entry to
follow the reference. Its number is not reused, and its old
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
- The order delta of [D13](#d13) is gone: claustrum decompresses first.
- Desktop's one captured `-cli-zst` call (2026-08-10) supplied no
  `-cli-checksum`, so no check ran on that path. It is one instance of one
  failure shape.

### D4 · `files.read` refuses a file that is not regular (retired) { #d4 }

- D4 made the refusal of a path that is not a regular file opt-in and off by
  default. Its premise was that the reference does not refuse such a path.
  `89cb6289` does not refuse one.
- `5fd08069` refuses such a path with `-32602 files.read: not a regular file`.
  Its answers put the open before the kind test: a directory that it cannot
  open gets the `open` text, and a writer that waits in its open of a FIFO
  returns at the request. On Linux and macOS it reads the null device itself as empty content. Measured on
  Linux, macOS and Windows VMs.
- claustrum now does the same, with no switch. The FIFO rows and the null
  device rows are tested on Linux and macOS. `NUL` and a named pipe are tested
  on Windows.
- The two costs of the old default are gone for the measured kinds. A FIFO with
  no writer gets the refusal at once. `/dev/zero` gets the refusal, so the
  daemon does not grow.
- The `-files-read-regular-only` flag and its `claustrum.conf` key are
  deprecated. Both are still accepted, both set nothing, and either one logs
  one warning on `-serve`.
- No knob stays to read such a path. A read of one is a new divergence, and
  rule 2 puts the burden of proof on it.

### D5 · Deadline on the `git` calls (retired) { #d5 }

- D5 made a deadline on every git call opt-in and off by default. Its premise
  was that the reference has no deadline on its git calls. The old probes
  reached 75 s only.
- `5fd08069` stops a git call of four git methods at 2 minutes, and some steps
  at 30 seconds or 10 minutes (rounds `task010-01` and `task010-02`).
  claustrum now has the measured limits, always. See
  [PROTOCOL.md → Time limits of git calls](PROTOCOL.md#time-limits-of-git-calls).
- The `-git-timeout` flag and its `claustrum.conf` key are deprecated. Both are
  still accepted, both set nothing, and either one logs one warning on `-serve`.
- No knob stays to change the limits. A different limit is a new divergence, and
  rule 2 puts the burden of proof on it.

### D7 · `-cli-version` must not collide with the install temp sweep (retired) { #d7 }

- D7 refused a `-cli-version` such as `.fetch-1` or `1.0.0.zst`. Its premise was
  that the sweep deletes such a version in the run that installed it. That held
  up to `7d193f89`.
- Since `4534d86` the sweep removes an entry only when its mtime is about 10
  minutes old or more. The reference installs such a version and keeps it. A later
  install removes it once it is that old. A cache hit does not.
- claustrum installs such a version too. Its sweep now claims only
  `.fetch-<digits>` and `<version>.zst`. Measured on a Linux VM against
  `4534d86` through `f6010b97`. [D6](#d6) still applies to every version.

### D9 · Namespace-wide params binding (retired) { #d9 }

- A method now ignores a member that another method of its namespace reads, as `5fd08069` does (round `task015-01`, Linux, macOS and Windows VMs).

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

### D13 · `-install` verified the checksum before decompressing (retired) { #d13 }

- claustrum hashed and verified a source before it decompressed it. A body that
  is no zstd archive with a wrong checksum then answered `checksum mismatch`,
  and `fetch.bytes` counted the whole body.
- `5fd08069` answers the decoder text with a right and with a wrong checksum.
  `fetch.bytes` is the count that the decoder read: 4 for a body of 4096 bytes
  (cells L15a and L15b, Linux VM). `7d193f89` through `f6010b97` answered
  the decoder text on `-cli-zst` too (Linux VM).
- claustrum now decompresses into the staging file first and verifies before
  the chmod, the clear and the rename.

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

### D18 · `-cli-version` must not start with `.blob-` (retired) { #d18 }

- claustrum refused a version with its own download prefix `.blob-`, and its
  prune did not count a `.blob-` name.
- `5fd08069` counts and removes no `.blob-planted` file (cells L4, M1 and W3,
  Linux, macOS and Windows VMs). The prune counts version files only.
- claustrum writes no `.blob-` file now, and it accepts such a version.

### D21 · A second daemon on a live socket appends to `remote-server.log` (retired) { #d21 }

- On Windows the launcher now truncates a held log, and each daemon writes at its own offset, as `5fd08069` does (round `task015-01`, Windows VM).

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

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods. The sanctioned exception is
  an optional, gracefully-ignored param whose result fields vanish by default.
  That is the CT-1 pattern. It leaves the default frame byte-identical, and it
  degrades both ways.

Any of these needs a deliberate, documented protocol version bump.
