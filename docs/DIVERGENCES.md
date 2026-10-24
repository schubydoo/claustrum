# claustrum — deliberate divergences

Almost everything claustrum does is byte-identical to the reference daemon. This
file is the catalog of the exceptions: the behaviours that *knowingly* change a
frame or an action. It also gives the rules that gate each one.

The [catalog table](#catalog) has one row per divergence. The
[rules](#how-we-decide-the-rules) come first, because they decide which shape an
entry can take: always-on or opt-in.

Per-method wire facts (params, result field order, error texts) live in
[PROTOCOL.md](PROTOCOL.md) and on the pages under `protocol/`. The history of the
reference builds is in [REFERENCE-BUILDS.md](REFERENCE-BUILDS.md).

claustrum follows the reference build `5fd08069`. An entry names the build of a
measurement where the build matters. A measurement of the reference that names no build was made
on `5fd08069` or on an earlier build. A fact that comes from
reading claustrum's code is marked "from the code".

## The one hard rule

Byte-identical JSON-RPC frames are the product. A divergence needs a reason.
Matching does not. "Off by default" means byte-identical, not almost
byte-identical. An opt-in divergence that is not active leaves the wire exactly as
the reference leaves it. The
[out-of-scope list](#explicitly-out-of-scope-would-break-compatibility) at the end
gives the changes we will never make.

## How we decide (the rules)

The standard, in priority order:

> 1. Claude Desktop must keep working. Claustrum is a drop-in. If Desktop can
>    reach a behaviour, and matching the reference keeps Desktop working, we
>    match. How ugly the reference's behaviour is does not matter.
> 2. Match by default. A wart Desktop tolerates is the contract, not a bug. The
>    burden of proof is always on the divergence.
> 3. A divergence earns ALWAYS-ON only if one of three conditions holds, or by the
>    exception in rule 4.
>    **(a)** Two things hold together. The reference's behaviour on that path
>    is not a frame at all: an unbounded wait, unbounded memory, or
>    unrecoverable data loss. And no honest caller can observe the
>    difference. **(b)** The trigger itself is unreachable on an honest path.
>    **(c)** The trigger is reachable, but both binaries fail the operation and
>    the only delta is diagnostic text.
> 4. Anything else that changes an honest-path frame is OPT-IN, default off, or
>    it does not ship. The one exception is a maintainer decision that an
>    entry is always-on. That entry gives the date of the decision and a reopen
>    trigger.

Corollaries:

- Keeping a wart does not mean hiding it. We match the wart *and* we document
  it, so a user can see the edge before it causes damage.
- "The reference does it too" is a reason to match, never a reason to call it
  safe. D2 is the standing counter-example: the reference wipes a home directory,
  and we still refuse to do it.
- Every always-on divergence owes a reopen trigger: the observation that makes us
  take it back out. Without one it is a preference, not a decision.

### Reading the clauses

Clause (a) is an AND. The second half is where thresholds fail. A bound is a
threshold, and a threshold cannot tell a hostile input from one that is only slow
or large. So an honest input trips it too. The test:

> When the guard fires on an honest input, who pays, and can they decline?

Take an honest input that is reachable, and a guard that the caller cannot turn
off. Then always-on is not justified, however the reference behaves on the
hostile path. That is why each divergence that is a timeout or a size cap is
opt-in (D3, D10, D12). D2 passes both
halves: the home wipe is unrecoverable data loss, and no honest caller has a use
for it. A caller can still reach that path by accident, which is what the guard
is for.

Clause (b) is used by D6, D8 and D19, and each has its own trigger. D6 rests on an
observed value plus a measured accepted set. So read it as unenumerated, not
established. Rule 2 puts the burden on the divergence. D19 holds for worktrees
that either daemon creates, because both creates refuse the junction. A worktree
made outside the daemon can still sit behind it.

Clause (c) is narrow, and no entry uses it today. An `error.code` is not
diagnostic text, because a client branches on it. On-disk state that a caller can
`files.stat` is not diagnostic text either.

### The "Desktop owns the argv" premise

Each opt-in entry rests on one driver claim: Claude Desktop owns the daemon's argv
on `-serve` and `-install`, so the `claustrum.conf` key is the knob an operator
can reach. [ARCHITECTURE.md → Driver claims and their
provenance](ARCHITECTURE.md#driver-claims-and-their-provenance) has the evidence
and the reopen trigger. A route to the argv would make a flag-only opt-in enough
for Desktop-driven hosts. It would not moot the config key, which serves every
other driver.

## Conventions for opt-in divergences

These hold for each opt-in entry:

- A flag and a matching `claustrum.conf` key. The key is the knob an operator can
  reach (see the argv premise above). For each key that has a flag, the
  precedence is an explicit CLI flag, then the config file, then the default.
- The default is off, which is the zero value. A disabled bound bypasses its guard
  entirely: a cap of `0` skips its `io.LimitReader`. A download bound of `0` is
  `http.Client{Timeout: 0}`, the stdlib's own "no timeout". Never use a huge-but-finite value instead. The
  `cap+1` arithmetic defines the boundary, and a huge value
  invents a new one.
- No opt-in bound is a hang detector. Each one is a threshold that an honest slow
  or large input trips too. That is why they are off by default.
- At the shipped defaults, the bounds of the `--version` run, of a managed
  launcher run, of the download and of the `ldd` libc probe are bounds of the
  reference, not divergences.
  [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli) has each
  value. Two clocks are claustrum's own: the wait of up to 2 s for the output of a
  stopped launcher run, and the 0.2 s bound of the Windows rename retry. The
  stdlib transport clocks (`net.Dialer{Timeout: 30s}`, `TLSHandshakeTimeout:
  10s`) apply on `-cli-url` only. They are always-on, unnumbered, and not
  probed on the reference.

## Catalog

| ID | What it does | Default | How to activate | Why (rule / clause) | Reopen trigger |
|----|--------------|---------|-----------------|---------------------|----------------|
| [D2](#d2) | Refuse a destructive path that is or holds `$HOME` | always-on | always-on | rule 3 (a). The `files.extract_tar` wipe: decision of 2026-10-10 | an honest caller that needs such a path (more in the entry) |
| [D3](#d3) | Cap `files.extract_tar` output size | off (`0`) | `-max-extract-bytes` / key | rule 4 | the cap refuses an honest extraction (more in the entry) |
| [D6](#d6) | `-cli-version` is one path component | always-on | always-on | rule 3 (b) | Desktop sends a multi-component version |
| [D8](#d8) | Refuse a FIFO, a link (not an own link to an own file on Linux and macOS) or (Linux, macOS) another user's `remote-server.log`. The `git-shadow` sweep writes nothing through a link | always-on | always-on | rule 3 (b), decision of 2026-10-19 | a shared socket folder that needs the log (more in the entry) |
| [D10](#d10) | Cap the `-install` CLI size | off (`0`) | `-max-cli-bytes` / key | rule 4 | Desktop stops treating disk-full as terminal |
| [D12](#d12) | Bound the `-install` download | off (`0`) | `-cli-download-timeout` / key | rule 4 | an honest slow download fails under the bound |
| [D15](#d15) | Verify a lock holder before a signal (macOS) | always-on (macOS) | always-on | rule 3 (a) | the reference verifies the holder too (more in the entry) |
| [D16](#d16) | `git.status` of a linked worktree works (Windows, no global excludes file) | always-on (Windows) | always-on | decision of 2026-10-13 | the reference or Git for Windows fixes the error (more in the entry) |
| [D19](#d19) | Refuse a junction between `baseRepo` and the leaf on remove (Windows) | always-on (Windows) | always-on | rule 3 (b), decisions of 2026-09-27 and 2026-10-07 | a client that needs the success reply (more in the entry) |
| [D20](#d20) | Wait 50 ms and read again before the group `SIGKILL` of a leader that reads as gone, at a `-serve` start (Linux and macOS) | always-on (Linux and macOS) | always-on | rule 3 (a) | the reference waits there too (more in the entry) |
| [D22](#d22) | Refuse a worktree locked in the `.git` folder of `baseRepo` on remove, where the reference answers success in four states | always-on | always-on | decision of 2026-10-03 | a caller that needs that removal (more in the entry) |
| [D23](#d23) | A child's environment block is in name order (Windows) | always-on (Windows) | always-on | decision of 2026-10-13 | the reference moves to Go 1.26 or newer |
| [D24](#d24) | In a create rollback, keep a folder that replaced the registration. On Windows, run no git call after a `.git` file swap | always-on | always-on | decisions of 2026-10-07 and 2026-10-19 | the reference keeps such a folder (more in the entry) |
| [D27](#d27) | Keep a stale entry whose record names a path that exists (Windows) | always-on (Windows) | always-on | decision of 2026-10-18 | a measurement of the reference with the same test |
| [CT-1](#ct-1) | `wantPid` adds `pid` and `startTime` | off | the caller sends `"wantPid":true` | optional-param extension | none |
| [CT-2](#ct-2) | `-keep-children` keeps the child tree at shutdown | off | `-keep-children` / key | off-wire extension | none |
| [CT-3](#ct-3) | The `claustrum.conf` config file | absent | create the file | the opt-in mechanism itself | none |
| [CT-4](#ct-4) | Hardened token persistence | not built | none | deferred idea | none |
| [CT-5](#ct-5) | `-listen-pipe` named-pipe transport (Windows) | off | `-listen-pipe` / key | additive transport | none |

Tags: **opt-in** means an operator switch, a flag and a config key. **always-on**
means no switch. The CT rows are off until someone asks: CT-1 by the caller,
CT-3 by the file, CT-2 and CT-5 by a flag and a key. CT-4 is not built. In the
CT headings "opt-in" means "off unless somebody asks for it": CT-1 and CT-3 have
no flag and no key.

## Entries

### D2 · Refuse a home directory as a destructive path target (always-on) { #d2 }

- **Behavior.** `wipesHomeDir` (`homeguard.go`) refuses a target that is the
  home directory or contains it. It resolves a relative path with
  `filepath.Abs` and then compares the text. It does not resolve symbolic links.
  On Windows `filepath.Abs` drops a dot or a space after a name, so home with a
  dot or a space after it is refused too.
  A path under home stays allowed, because `~/.claude/…` is the install path of
  the daemon. Home comes from `os.UserHomeDir`: `HOME`, or `USERPROFILE` on
  Windows. With that variable unset or empty, nothing is refused (from the
  code). The guard runs before these deletes:
  - `files.extract_tar`, before the wipe of `destDir`. The reply starts with
    `destDir must not be or contain the home directory:`. A folder at the last
    name of `destDir` that is home or a parent folder of it is refused by
    identity too. On Linux and macOS that covers a path through a link above the
    last name. On Windows it covers the `\\?\` prefix and the 8.3 name of home.
    The maintainer approved this delete path on 2026-10-10.
    [files.extract_tar](protocol/files-extract-tar.md) has the two other tests
    of claustrum's own and their texts.
  - `git.worktree_remove`. Since `7d193f89` the reference first refuses a
    `worktreePath` that is not strictly inside `baseRepo`. So without
    `worktreeRoot` the guard fires only for a repository that is an ancestor of
    home. With `worktreeRoot` it is the active home guard.
  - Each rollback delete of `git.worktree_create`, and from the code its stale
    step before the add. It is defense in depth and emits no frame. The
    exception is the registration delete of a create that holds no git
    directory and accepted no registration. That delete tests the `gitdir` record and the registrations
    directory instead. On Windows the guard also runs before the cleanup of old
    `.claude-new-worktree-*` entries and before the two staging removes. A
    refused entry stays.
  - The deletes inside the folder `git-shadow` of the daemon.
  - `-install`, before the tree delete of a folder at the CLI path. The reply is
    `cli path must not be or contain the home directory: "<path>"`. The folder
    test is by identity too (`folderHoldsHome`). The sweeps and the `-cli-keep`
    prune skip such an entry (`cliEntryHoldsHome`). `89cb6289` removed an empty
    home folder there.
- **Reference.** It wipes home for `"destDir":"~"` and answers
  `{"success":true}` (measured on `7d193f89`). `-install` removes home as a tree
  and puts the CLI file there (measured with `-cli-zst` on Linux, macOS and
  Windows). Not measured: `-cli-url`, a mount of a parent folder of home, a home
  folder that does not exist, a hard link, Unicode normalization forms, a
  case-sensitive volume on macOS and a UNC path. For the last name `...`, claustrum
  and `5fd08069` answer `clean destDir: RemoveAll ...: invalid argument` on
  Windows, which is parity. No measured staging entry of the reference held home, so that guard
  is claustrum's own.
- **Systems and switch.** Linux, macOS and Windows, with the staging removes
  on Windows only. Always on, no switch.
- **Cost.** A caller that names home, or a folder that holds it, gets a refusal.
  On Windows claustrum refuses a `destDir` with the `\\?\` prefix and a dot or a
  space after its last name. There `5fd08069` extracts into the folder with no
  dot or space.
- **Rule.** Rule 3 clause (a). The home wipe is unrecoverable data loss. No
  honest caller has a use for it, but a caller can reach it by accident. On
  2026-08-02 an in-repo fuzzer sent `"destDir":"~"` to a live daemon and
  destroyed the maintainer's home directory. The old gate
  `IsAbs && !isFilesystemRoot` passed it. Before `7d193f89`,
  `"worktreePath":".."` from a daemon in home reached `os.RemoveAll` on the
  parent of home. That is why the guard resolves a relative path. It is not a
  security boundary: the socket and the token already grant `process.spawn`
  ([SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
  It stops the accidental, generated or mistyped path.
- **Reopen trigger.** An honest caller that names a destructive target that is
  or contains a home directory. Or a client that needs a Windows `destDir` with
  the `\\?\` prefix and a dot or a space after its last name.
- **Pointers.** [files.extract_tar](protocol/files-extract-tar.md),
  [git.worktree_remove](protocol/git-worktree-remove.md#where-the-paths-can-be),
  [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli),
  `homeguard_test.go` (the `wipeDestDir` seam keeps the suite safe against an
  unfixed tree) and `install.go` (`stageAndInstall`).

### D3 · Make the `files.extract_tar` size cap opt-in { #d3 }

- **Behavior.** `maxExtractBytes` caps the extraction output. An over-cap
  extraction returns
  `{"success":false,"fileCount":0,"error":"extraction size limit exceeded"}`
  and removes the truncated entry.
- **Reference.** No cap. It completed a 629 MB extraction (measured on
  `5db5e4a`).
- **Systems and switch.** Linux, macOS and Windows. Default `0`, off. Turn it on
  with `-max-extract-bytes <n>` or the `max-extract-bytes` key. Off bypasses
  `io.LimitReader`.
- **Cost.** A cap fails an honest extraction above it.
- **Rule.** Rule 4. The reference answers a frame, not an unbounded wait, and
  Desktop owns the argv.
- **Reopen trigger.** An operator's cap that refuses a legitimate extraction, or
  the default that lets a size bomb through in normal use.
- **Pointers.** [files.extract_tar](protocol/files-extract-tar.md#the-size-limit)
  and `methods_files.go`.

### D6 · `-cli-version` must name a single path component (always-on) { #d6 }

- **Behavior.** The install clears the CLI path, `<cli-dir>/<version>`, with an
  `os.RemoveAll`. claustrum refuses a version that is `.` or `..`, or that holds
  `/` or `\`, with `cli version "…" must be a single path component`. Nothing
  is written or removed at a path built from the version, and the blob stays.
  A last component that is a symbolic link stays legal, because `os.RemoveAll`
  unlinks it. [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli)
  has the cache hit that comes first and the sweeps that are not behind this
  rule.
- **Reference.** No such rule. With `-cli-version ../x` and a blob it installs
  to `<parent>/x`, outside the cli-dir (Linux, macOS and Windows). With
  `../victim` it destroys the target.
- **Systems and switch.** Linux, macOS and Windows. Always on, no switch.
- **Cost.** None for a bare version: `1.0.86`, a commit sha and `latest` are
  measured as accepted.
- **Rule.** Rule 3 clause (b). The real client passes bare version strings. The
  evidence is an observed value plus a measured accepted set, not an
  enumeration. A lexical containment test was not taken, because it accepts
  `link/1.0.0` through an intermediate symlink. An `EvalSymlinks` test only adds
  a TOCTOU window.
- **Reopen trigger.** Desktop passes a `-cli-version` that is not a single path
  component.
- **Pointers.** `install.go`.

### D8 · claustrum refuses a link, a FIFO or another user's `remote-server.log`, and the `git-shadow` sweep writes nothing through a link (always-on) { #d8 }

- **Behavior.** If the fresh create of the log fails, the launcher judges the
  entry at the log path by `Lstat`.
  [PROTOCOL.md → Daemon log](PROTOCOL.md#daemon-log-remote-serverlog) has the
  steps, which match `5fd08069` apart from the entries below.
  - Linux and macOS: an own regular file, or the own regular file behind an own
    link, is truncated. Every other entry fails the start with
    `claustrum: open log: open <path>: permission denied` and exit 1, and stays
    unchanged. Examples are a link to another user's file, a FIFO and another
    user's file. Three rules are claustrum's own, not measured. The target of an
    own link must be an own regular file with one link. An own file with a
    second hard link is refused. An own file that the open cannot write fails
    the start with the error of that open.
  - Windows: a regular file is truncated with no owner test. A file symbolic
    link is refused.
  - The `git-shadow` sweep: a symbolic link with a folder name of this host goes
    with one plain remove, and on Windows a junction too. Nothing is written in
    the target.
- **Reference.** On Linux it refuses the same entries in a sticky
  world-writable folder with the `fs.protected` sysctls on. On macOS, and on
  Linux with those sysctls off, it writes through a link or into another user's
  file, and it blocks on a FIFO. On Windows it writes through a file symbolic link. Its sweep makes
  an empty lock file in the target of a link (Linux, macOS and Windows).
- **Systems and switch.** Linux, macOS and Windows, as split above. Always on,
  no switch.
- **Cost.** None in the deployed socket folder.
- **Rule.** The maintainer's decision of 2026-10-19, after `5fd08069` truncated
  a file of another user and the target of a planted link. Rule 3 clause (b):
  the deployed socket folder (`~/.claude/remote/`) is per-user and not
  world-writable. So no honest start reaches the refusal, and no honest sweep
  meets a planted link.
- **Reopen trigger.** A deployment that puts the socket folder somewhere shared,
  the reference refusing these entries on every host, or the reference making
  no lock file through a link.
- **Pointers.** `server.go` (`openDaemonLog`), `detach_unix.go`
  (`openHeldDaemonLog`) and `gitshadowsweep.go` (`sweepLink`).

### D10 · Make the `-install` CLI size cap opt-in { #d10 }

- **Behavior.** `maxCLIBytes` caps the decompressed CLI
  (`decompressing: decompressed CLI exceeds <n> bytes`) and the download body
  (`download failed: response exceeds <n> bytes`).
- **Reference.** It takes a 600 MiB payload as far as the run test of the new
  CLI, on both paths.
- **Systems and switch.** Linux, macOS and Windows. Default `0`, off. Turn it on
  with `-max-cli-bytes <n>` or the `max-cli-bytes` key. Off bypasses both
  `LimitReader`s. claustrum streams the download into the staging file and
  hashes it in the same pass, so "cap off" does not mean unbounded memory. The
  memory of the streamed decode is not measured.
- **Cost.** A cap below the free space replaces a disk-full report with the
  cap's own message, so the user loses the free-space hint. That cost turns on
  the `cliError` driver claim
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)).
- **Rule.** Rule 4. The reference answers a frame, not an unbounded wait, and
  Desktop owns the argv.
- **Reopen trigger.** Desktop stops treating a disk-full message as terminal,
  which removes the cost.
- **Pointers.** [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli)
  and `install.go` (`fetchCLI`, `zstdDecompress`).

### D12 · Make the `-install` CLI download bound opt-in { #d12 }

- **Behavior.** `http.Client{Timeout: cliDownloadTimeout}` bounds the whole
  download, the body read too. So a real body that needs six minutes trips it,
  as a black hole does. With the bound off, the transport clocks of the
  [conventions](#conventions-for-opt-in-divergences) still apply, so a
  SYN-black-holed host fails at 30 s.
- **Reference.** No bound on the total exchange in the measured window. A valid
  blob that took about 324 s to arrive installed (measured on `5db5e4a`). Two
  60 s limits are parity and always on: the wait for the response headers
  (Linux and Windows), and a body that sends no byte for 60 s. A bound below
  60 s fires first, and no measurement covers that pair.
- **Systems and switch.** Linux, macOS and Windows. Default `0`, off:
  `http.Client{Timeout: 0}` is the stdlib's own "no timeout". Turn it on with
  `-cli-download-timeout <dur>` or the `cli-download-timeout` key.
- **Cost.** An honest slow download fails under the bound.
- **Rule.** Rule 4. Desktop owns the argv.
- **Reopen trigger.** An operator with the bound set reports an honest slow
  download that it failed.
- **Pointers.** [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli)
  and `install.go` (`fetchCLI`).

### D15 · Verify a run-dir lock holder is our serve process before signalling it (macOS) { #d15 }

- **Behavior.** Before a new `-serve` daemon or `-stop` signals a live holder of
  the run-dir lock, it verifies that the holder is one of our own `-serve`
  processes for this socket. On macOS claustrum reads the argument vector with
  `sysctl KERN_PROCARGS2`. It signals no other pid. The daemon then serves
  without run-dir ownership, and `-stop` prints `survivor`.
- **Reference.** On Linux it verifies the command line of the holder too, so
  Linux is parity. On macOS it sends `SIGTERM` to the recorded pid unverified,
  also to a holder that is not a serve process (measured on macOS). On macOS its
  `-stop` signals any live holder whose record says role `serve` on the node of
  this host: `SIGTERM`, then `SIGKILL` 4 s later to a holder that ignores it.
- **Systems and switch.** macOS. Always on, no switch.
- **Cost.** None on the honest path: the live holder wrote its own pid into the
  record, so the check passes and the predecessor is evicted. A holder that
  cannot be inspected, or is not a serve process, stays alive where the
  reference signals it.
- **Rule.** Rule 3 clause (a). A signal to an unverified pid is unrecoverable
  harm. A crash can leave a stale record whose pid an unrelated process reuses,
  and a foreign process can hold the flock. No honest caller gains from skipping
  the check.
- **Reopen trigger.** The reference adds the same holder check on macOS (then
  this is parity). Or a legitimate macOS holder that `KERN_PROCARGS2` cannot
  read, reported as a failed handover.
- **Pointers.** [PROTOCOL.md → Run-dir lock](PROTOCOL.md#run-dir-lock-daemonlock).
  The host cleaner has its own gate, which is parity
  ([Signals of the cleaner](PROTOCOL.md#signals-of-the-cleaner),
  `retireAbandoned`). Code:
  `daemon_runlock_unix.go` (`holderSignalRefusal`, `stopRunDirHolder`),
  `daemon_runlock_darwin.go` (`realIsServeCmdline`, `procArgv`) and
  `daemon_runlock_linux.go`.

### D16 · `git.status` of a linked worktree returns the status on Windows, where the reference errors (Windows) { #d16 }

- **Behavior.** On Windows, with no global excludes file of the user,
  `git.status` of a `path` that names a linked worktree returns the status
  result (`{"isRepo":true,"clean":false,"changes":[…]}`). On Windows claustrum
  passes `-c core.excludesFile=/dev/null` to the `status`, `ls-files` and
  `diff-index` calls of `git.status` (`statusExcludesFile`). Every other call
  with that item passes `NUL` there, as the reference does.
- **Reference.** On Windows it answers `-32603 "exit status 128"` there
  (measured on Windows). It passes `NUL`, and Git for Windows 2.55.0 refuses it
  with `fatal: cannot use NUL as an exclude file`. With a user excludes file its
  status works. Linux and macOS are byte-identical.
- **Systems and switch.** Windows. Always on, no switch.
- **Cost.** A Windows client that diffs frames against the reference sees a
  result where the reference sends an error.
- **Rule.** Rule 4, by the maintainer's decision of 2026-10-13. A Windows client
  that runs status on a session worktree reaches this path by design.
- **Reopen trigger.** The reference fixes its Windows `git.status`. Or a Git for
  Windows release accepts `NUL` there. Or a decision to answer 128 as the
  reference does: that change is the value `NUL` in those three calls. The
  reference stops at its `status` call, so its value for the other two is not
  measured.
- **Pointers.** [git.status](protocol/git-status.md#differences-by-system),
  `gitstatus.go` (`statusChanges`) and `githarden.go`.

### D19 · Refuse a junction between `baseRepo` and the leaf on remove (Windows, always-on) { #d19 }

- **Behavior.** On Windows, `git.worktree_remove` refuses a junction at any
  directory between `baseRepo` and the leaf:
  `{"success":false,"error":"failed to remove worktree: openat <path from
  baseRepo>: path escapes from parent"}`. Here `<path from baseRepo>` is the
  folder that holds the leaf. For a junction at `.claude` or
  `.claude\worktrees` the frame has `.claude\\worktrees`. That text is claustrum's own,
  the error of `os.Root`. Nothing is deleted, and the branch stays. For a
  junction above `.claude` no git call runs.
- **Reference.** On Windows, for a junction at `.claude` or `.claude\worktrees`,
  it answers `{"success":true}` and deletes only the branch. So a live worktree
  behind the junction is left on a deleted branch. With no worktree behind it,
  the answer is `{"success":true}` too. This is measured on `f6010b97`, and on
  `89cb6289` for a branch that another ref reaches. A junction above `.claude`
  with a leaf and branch that do not exist gets `{"success":true}` from
  `89cb6289`. Nothing changes on disk (measured on Windows).
  Not measured: a live
  worktree behind such a junction, and the branch step there.
- **Systems and switch.** Windows. Always on, no switch.
- **Cost.** A Windows client sees `success:false` where the reference sends
  `success:true`. A branch that the reference deletes stays. A client that
  relies on the remove to delete the branch there must delete it itself.
- **Rule.** Rule 3 clause (b), by the maintainer's decision of 2026-09-27,
  widened on 2026-10-07 to any directory between `baseRepo` and the leaf. The
  `git.worktree_create` of both daemons refuses a junction there with
  `mkdir_failed`. Above `.claude` both answer
  `failed to create parent directory: <P>\J is not a directory`, with equal
  frames. So only a worktree made
  outside the daemon sits behind it.
- **Reopen trigger.** The reference refuses the junction with the same text (then
  this is parity), or deletes the tree through the junction. Or a Windows client
  depends on the success reply there.
- **Pointers.** [git.worktree_remove](protocol/git-worktree-remove.md#where-the-paths-can-be)
  and `worktreeremove.go` (`openRemoveParent`).

### D20 · Wait for a settle before the group `SIGKILL` of a leader that reads as gone (Linux and macOS) { #d20 }

- **Behavior.** At a `-serve` start the daemon ends the child groups of a dead
  predecessor: `SIGTERM` to the group, then a wait of 2 s. A leader can read as
  gone in that wait while its group still answers. claustrum then waits 50 ms
  and reads the leader again before the group `SIGKILL`. A leader that is alive
  again takes the record test. A leader that fails that test gets no more
  signals. No frame changes. With the wait, claustrum did not end the child
  that replaces its program (measured on Linux and macOS).
- **Reference.** `89cb6289` sends that `SIGKILL` within 5 ms of the `SIGTERM`
  (measured on Linux). So a child that replaces its program on `SIGTERM` is
  sometimes ended on Linux, and was not ended on macOS (measured on Linux and
  macOS). It also sends a group `SIGKILL` in some runs where the child ends on
  `SIGTERM` (measured on Linux). claustrum sends none there.
- **Systems and switch.** Linux and macOS. Always on, no switch. The 50 ms is
  claustrum's own value.
- **Cost.** The group `SIGKILL` comes about 50 ms later. A reap whose groups all
  end on the `SIGTERM` returns one wait later. A program replacement that takes
  longer than 50 ms still gets the `SIGKILL`. No measured run shows that.
- **Rule.** Rule 3 clause (a). A `SIGKILL` to a live process that is no longer
  the recorded child cannot be taken back, the same shape as [D15](#d15). On the
  honest path the outcome stays inside what the reference shows. The child that
  replaces its program stays alive, as in most runs of the reference. A group
  whose leader exits still gets its `SIGKILL`. Only the time of one signal moves,
  off the wire.
- **Reopen trigger.** A measurement shows the reference waiting before that
  `SIGKILL` (then this is parity). Or a child outlived a daemon restart because
  it replaced its program.
- **Pointers.** [PROTOCOL.md → Child records and the reap](PROTOCOL.md#child-records-and-the-reap-linux-and-macos)
  and `childreap.go` (`settleGone`, `reapGoneLeader`).

### D22 · Refuse a worktree locked in the `.git` folder of `baseRepo` on remove (always-on) { #d22 }

- **Behavior.** `git.worktree_remove` refuses a worktree that is locked in the
  `.git` folder of `baseRepo`, and deletes nothing. The removal can read its
  entries from another git directory. Then claustrum also reads
  `<baseRepo>/.git/worktrees`, with no git call, with and without
  `worktreeRoot`. A folder there that it cannot read gives the lock-check
  refusal, and nothing is deleted. A missing folder counts as not locked. On
  Linux and macOS a gone worktree whose locked entry sits behind a linked
  `<git dir>/worktrees` gets success, and its branch goes. That follows
  `5fd08069` (measured on Linux), by the maintainer's decision of 2026-10-14.
- **Reference.** In the plain state it refuses with the same text. `89cb6289`
  answers success in four states. S is a worktree locked in T, the `.git` folder
  of `baseRepo`, and X is another repository:
  - a daemon `GIT_DIR` and `GIT_COMMON_DIR` of X: it deletes the folder of S,
    and the entry in T with its `locked` file stays (Linux, macOS and Windows)
  - a daemon `GIT_DIR` of X alone: it deletes the folder of S (Linux, macOS and
    Windows)
  - `baseRepo` `<T>/missing/..` with no folder `missing`: it deletes the folder
    of S with `"branchKept":true` (Linux and macOS). This state is not measured
    on Windows: both sides refuse that `baseRepo` there
  - the `GIT_DIR` and `GIT_COMMON_DIR` state with the folder of S gone: it
    deletes nothing (Linux, macOS and Windows). claustrum answers the "is gone
    but its registration is locked" refusal

  On Linux, with `worktreeRoot` in the `GIT_DIR` states, `89cb6289` answers the
  `could not verify` text, or success with the folder gone. It deletes nothing,
  and claustrum answers the locked refusals (measured on Linux). Not measured: a daemon `GIT_COMMON_DIR` of a third
  repository, and a `worktrees` folder that cannot be read.
- **Systems and switch.** Linux, macOS and Windows. Always on, no switch.
- **Cost.** In these states claustrum sends `success:false` with a locked text,
  and a folder stays that the reference deletes. A caller must unlock the
  worktree first. Each state needs a daemon `GIT_DIR`, a `baseRepo` spelled
  through a missing name, or a folder removed by hand. No measurement shows a
  client that reaches one of them.
- **Rule.** Rule 4, by the maintainer's decision of 2026-10-03. A lock is the
  user's own instruction not to delete, and a deleted folder cannot be taken
  back. The same decision keeps one gap: a `baseRepo` with no `.git` folder of
  its own, such as a subfolder of T, a linked worktree or a submodule. With a
  daemon `GIT_DIR` of another repository, claustrum does not read the lock there
  and deletes the locked worktree. The reference is not measured there.
- **Reopen trigger.** A caller that needs the removal of a locked worktree in
  these states. Or a measurement that shows the reference refusing there (then
  this is parity).
- **Pointers.** [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks)
  and `worktreeremove.go` (`lockedInBaseRepo`).

### D23 · The environment block of a child is in name order (Windows) { #d23 }

- **Behavior.** On Windows claustrum gives a child its environment block in name
  order: `process.spawn`, the git children and `-install`.
- **Reference.** It does not sort the block. Go sorts it since 1.26, and the
  reference builds use go1.25.14. A claustrum build with go1.25.14 matched the
  order of `5fd08069` (measured on Windows).
- **Systems and switch.** Windows. Always on, no switch.
- **Cost.** A child sees its variables in another order.
- **Rule.** Rule 4, by the maintainer's decision of 2026-10-13: claustrum stays
  on Go 1.26. Go 1.26.9 fixed standard library advisories of go1.25.14, one in
  the folder handles of the Windows file guards (GO-2026-6604).
- **Reopen trigger.** The reference moves to Go 1.26 or newer.
- **Pointers.** `process.go`.

### D24 · In the create rollback, keep a folder that replaced the new registration and run no git call after a `.git` file swap { #d24 }

- **Behavior.** The tests after the add can accept the registration of the new
  worktree. Then `git.worktree_create` takes the identity of that folder and
  holds it open until it answers. While that handle is open, a new folder cannot
  get that identity. If the open fails, the identity comes from a stat of the
  path (not measured). On Windows the identity is that of the staging folder,
  held open from its mkdir to the end of the move.
  - A rollback deletes the registration in one case only: the folder at the path
    still has that identity. The test runs through the root that the delete
    opened. It adds no text, and the branch step runs.
  - After a failed `rev-parse --show-toplevel`, the rollback deletes the one new
    entry of the listing after the add, by the identity from that listing. A
    sibling registration moved to that name stays. `5fd08069` deletes it
    (measured on Linux).
  - The placement of the index has the same test. On a mismatch it places
    nothing and answers a failed checkout with a text of its own
    ([git.worktree_create](protocol/git-worktree-create.md#differences-from-the-reference)).
  - If git gave no answer to `rev-parse --absolute-git-dir`, the guard does not
    run.

  On Windows, after a swap of `.git` to a file that names a second repository,
  claustrum runs no git call and changes nothing there. Its undo text says that
  the branch still exists.
- **Reference.** A process renames the new registration and puts another folder
  at its name before the rollback. On Linux and macOS `89cb6289` removes that
  folder, which can be the registration of a live sibling worktree. On Windows
  `5fd08069` deletes it too. The frames are equal, and on Linux and macOS the
  leaf and the branch go on both sides. On Windows, after the `.git`
  swap, `5fd08069` runs the branch step in the second repository. It deletes a
  branch of the same name there that `main` reaches, or keeps it with
  `branchKept`. The frames differ.
- **Systems and switch.** Linux, macOS and Windows, with the `.git` swap on
  Windows only. Always on, no switch.
- **Cost.** A replaced folder stays that the reference removes, and no frame
  differs. The registrations folder must change between the add and the
  rollback to reach the guard. After a `.git` swap the frame differs, and a branch can stay that
  `5fd08069` deletes. Not measured: the guard at the index placement, and a
  replaced folder with a daemon `GIT_DIR` of another repository.
- **Rule.** Rule 4, by the maintainer's decision of 2026-10-07: the guard
  refuses the loss of the registration of another worktree. The decision of
  2026-10-19 adds the `.git` swap, where the harm is a branch deleted in the
  repository that the file names.
- **Reopen trigger.** A measurement that shows the reference keeping such a
  folder. Or a decision to match its disk for a replaced folder. Or a decision to
  run the branch step in the repository that the `.git` file names.
- **Pointers.** [git.worktree_create](protocol/git-worktree-create.md#after-a-failure).

### D27 · Keep a stale entry whose record names a path that exists (Windows) { #d27 }

- **Behavior.** On Windows the stale step of `git.worktree_create` compares the
  `gitdir` record of each entry with the new leaf without regard to letter case.
  Before it deletes a matched entry, it tests the path that the record names, as
  written, with `Lstat`. If that path exists, the entry stays. Both forms of the
  step have the test: through the held git directory and by the path. Linux and
  macOS compare exactly, so the missing leaf already proves the entry stale. In
  the state below claustrum keeps the entry, and `W1` stays on its branch.
- **Reference.** On Windows a folder `.claude\worktrees` with case sensitivity
  on holds a live worktree `W1`, and a client creates `w1`. `5fd08069` deletes
  `.git\worktrees\W1` before the add and answers success. `W1` then reads the
  records of `w1`, and its own branch is no longer checked out (measured on
  Windows).
- **Systems and switch.** Windows. On-wire, always on, no switch.
- **Cost.** In that state the create answers
  `{"success":false,"error":"git worktree add failed: Preparing worktree (new branch 'b1') fatal: '<repo>/.claude/worktrees/w1' is a missing but already registered worktree; use 'add -f' to override, or 'prune' or 'remove' to clear","errorCode":"worktree_add_failed"}`.
  The branch `b1` stays, because git makes it before it refuses. No cost where
  the folder of a stale entry is gone (from the code). A `Lstat` that fails with
  another error than "does not exist" keeps a stale entry too, and then the add
  fails.
- **Rule.** Rule 4, by the maintainer's decision of 2026-10-18. The reference
  destroys the records of a live worktree in a state that a client can reach.
- **Reopen trigger.** A measurement that shows `5fd08069` with the same test.
- **Pointers.** [git.worktree_create](protocol/git-worktree-create.md#differences-by-system).

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach { #ct-1 }

- **Behavior.** `process.spawn` and `process.reattach` accept an optional
  `"wantPid":true`. The result of a spawn, and of a reattach that finds the
  process, then also has `pid` and `startTime`. Without `wantPid` the frame is
  byte-identical to the reference.
- **Why.** A client can detect a reused pid or an orphan. An older daemon ignores
  the parameter, and an older client never sees the members. So a client can
  send `wantPid` on every request.
- **Activate.** The caller sends `"wantPid":true`. There is no flag and no key.
- **Pointers.** [process.spawn](protocol/process-spawn.md#response) has what
  `startTime` means. Also [process.reattach](protocol/process-reattach.md) and
  `results.go`.

### CT-2 · Opt-in `-keep-children` serve flag { #ct-2 }

- **Behavior.** Off the wire. Without the flag, a graceful shutdown ends the
  children. A graceful shutdown is `-stop`, `server.shutdown`, `SIGTERM`,
  `SIGINT` or the self-exit. With the flag it leaves the spawned children running and log one line with their count. The new daemon does not re-adopt
  them. A client can find them again through the CT-1 `pid` and `startTime`.
- **Limits.** The survivors lose their stdio, so only a child that tolerates a
  dead stdin and stdout lives on (not measured on Windows). On Linux and macOS the shutdown removes the
  records of the kept children (claustrum's own). So the reap of the next start
  sends them no signal. That covers the processes of the process table only.
  A process whose `id` a later spawn took keeps its record, and the next start
  ends it. The host cleaner does not read the records. It ends an orphaned
  Claude Code group under the install root, a kept one too (not measured with
  the flag). A child whose start returns after the shutdown began is ended also
  with the flag (from the code). Its client got no reply, so no client has its
  `pid`.
- **Reference.** `89cb6289` has no such flag and exits with code 2 on it.
- **Activate.** `-keep-children` or the `keep-children` key.
- **Pointers.** [PROTOCOL.md → Flags of claustrum
  alone](PROTOCOL.md#flags-of-claustrum-alone) has the log line, the stdio of the
  survivors and Windows.

### CT-3 · Opt-in `claustrum.conf` config file { #ct-3 }

- **Behavior.** `claustrum.conf` is an optional `key = value` file with `#`
  comments, in the folder that holds the binary. Its keys mirror the flags. The
  [flags table](PROTOCOL.md#flags-and-config-keys) lists each key, the
  deprecated ones too. Each key that sets something turns on, or tunes, a behavior
  that is off by default. The deprecated keys set nothing and log one
  warning.
- **Fail-safe.** A missing file, a file that cannot be read and a path that is no
  regular file, a link too, give stock behavior. claustrum reads at most the
  first 64 KiB. It ignores an unknown key and an invalid value. A problem of the
  file never fails the start. A `wire-log` path that cannot be opened does fail
  it. A duration uses `time.ParseDuration`, which
  refuses a bare number other than zero. A zero in any spelling means off. So no
  accepted value can switch a divergence on by mistake.
- **`version-override`.** Set it to the commit SHA that the desktop client pins,
  and the client stops uploading the reference each session. The value is 40 or
  64 hex digits, claustrum prints it in lower case, and any other value does
  nothing. [PROTOCOL.md → -version](PROTOCOL.md#-version) has the output and
  how the client reads it.
- **Activate.** Create the file.
- **Pointers.** `config.go`.

### CT-4 · Opt-in hardened token persistence — deferred idea { #ct-4 }

- **Idea (not built).** A `claustrum.conf` key such as `persist-token = false`,
  or an owner-only DACL on `daemon.token` on Windows, or both. It is for an
  operator who prefers a smaller on-disk token window over reconnect. Without
  the key the daemon stays stock.
- **Why deferred.** There is no demand. The default matches the reference, and
  the socket folder is owner-only in the real deployment.
- **Pointers.** [PROTOCOL.md → Token
  persistence](PROTOCOL.md#token-persistence-daemontoken) has the file and its
  two caveats, and `tokenpersist.go`.

### CT-5 · Opt-in `-listen-pipe` Windows named-pipe transport { #ct-5 }

- **Behavior.** On Windows, `-serve` also serves the same JSON-RPC dispatch over a
  named pipe, beside the `AF_UNIX` socket, with the same frames and the same
  `"auth"`. Other systems ignore the
  flag and log a warning. A setup failure is not fatal, and the socket still
  serves.
- **Why.** A Windows client that cannot use an `AF_UNIX` socket can attach.
  Python `asyncio` is the example: its Windows loop supports named pipes.
- **Activate.** `-listen-pipe` or the `listen-pipe` key.
- **Pointers.** [PROTOCOL.md → Named-pipe
  transport](PROTOCOL.md#named-pipe-transport-windows-opt-in) has the name, the
  file rules and the DACL. Also `pipetransport.go` and
  `pipetransport_windows.go`.

## Retired entries

A retired entry is a divergence that the reference made moot, or one that the
maintainer retired to follow the reference. Its number is not reused. A retired
flag and its `claustrum.conf` key are still accepted and set nothing. Each logs
one warning in the mode of the flag, `-serve` or `-install`. A duration flag
whose value does not parse fails the flag parse with exit 2 (from the code).

### D1 · Verify the `-cli-zst` blob against a supplied checksum (retired) { #d1 }

claustrum checks a supplied `-cli-checksum` against the `-cli-zst` blob, as the
reference does: see [PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli).

### D4 · `files.read` refuses a file that is not regular (retired) { #d4 }

`files.read` refuses a path that is not a regular file with no switch, as the
reference does: see [files.read](protocol/files-read.md).

### D5 · Deadline on the `git` calls (retired) { #d5 }

The git calls have the time limits of the reference, always: see
[PROTOCOL.md → Time limits of git calls](PROTOCOL.md#time-limits-of-git-calls).

### D7 · `-cli-version` must not collide with the install temp sweep (retired) { #d7 }

A version with a sweep name, such as `.fetch-1`, installs and stays in that run,
as on the reference. [D6](#d6) still applies to every version.

### D9 · Namespace-wide params binding (retired) { #d9 }

A method ignores a member that another method of its namespace reads, as the
reference does: see [PROTOCOL.md → Params presence and
typing](PROTOCOL.md#params-presence-and-typing).

### D11 · Deadline on the `<cli> --version` run of `-install` (retired) { #d11 }

D11 made a deadline on that run opt-in, and claustrum now stops it at the 30 s and
120 s of the reference, always: see
[PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli).

### D13 · `-install` verified the checksum before decompressing (retired) { #d13 }

claustrum decompresses into the staging file first, as the reference does, so a
body that is no zstd archive answers the decoder text. The checksum test comes
before the chmod, the clear and the rename (claustrum's own): see
[PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli).

### D14 · Deadline on the `ldd --version` libc probe (retired) { #d14 }

The `ldd` probe has the 5 s bound of the reference, always: see
[PROTOCOL.md → -install](PROTOCOL.md#-install-ensure-the-agent-cli).

### D17 · An abandoned `lsof` run reads as busy, not idle (retired) { #d17 }

On macOS an idle daemon whose `lsof` run `SIGKILL` cannot end gets no SIGTERM
and keeps its run dir, as on the reference. Only the time of that pass differs:
see [PROTOCOL.md → The `lsof` reads](PROTOCOL.md#the-lsof-reads-macos).

### D18 · `-cli-version` must not start with `.blob-` (retired) { #d18 }

claustrum writes no `.blob-` file and accepts a version that starts with
`.blob-`, and the prune counts version files only, as on the reference.

### D21 · A second daemon on a live socket appends to `remote-server.log` (retired) { #d21 }

On Windows the launcher truncates a held log, and each daemon writes at its own
offset, as on the reference: see [PROTOCOL.md → Daemon
log](PROTOCOL.md#daemon-log-remote-serverlog).

### D25 · Answer `"unavailable"` for the peer check and serve every caller (retired) { #d25 }

On Linux claustrum checks the namespaces of each caller with the frames of the
reference: see [server.capabilities](protocol/server.md#the-peer-check).

## Candidates considered but not taken

In each of these claustrum can be friendlier than the reference, and matching the
reference costs something real. We record them so the code can point
somewhere durable. No decision is implied, and nothing here is shipped or
scheduled.

- **Conditional `-stop` socket unlink.** After a failed connect, `-stop` removes
  the socket path, unless it prints `survivor`, as the reference does. That
  `os.Remove` does not check the kind of the path, so a regular file or an empty
  directory at the `-socket` path goes too. A `stat`-first variant that removes
  only a socket keeps those two kinds, and it is a divergence.
- **Fail fast on a missing `-serve` token source.** The check runs in the detached
  child, so the launcher reports its accept timeout after about 12 s. The real
  reason reaches only the log of the child. That is reference parity. A check in
  the parent answers at once and names the actual problem: a better operator
  experience, and a divergence.

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding required new params to existing methods. The sanctioned exception is
  an optional, gracefully-ignored param whose result fields vanish by default.
  That is the CT-1 pattern. It leaves the default frame byte-identical, and it
  degrades both ways.

Any of these needs a deliberate, documented protocol version bump.
