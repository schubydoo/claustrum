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
    prune skip such an entry (`cliEntryHoldsHome`).
- **Reference.** It wipes home for `"destDir":"~"` and answers
  `{"success":true}` (measured on `7d193f89`). `-install` removes home as a tree
  and puts the CLI file there (Linux, macOS and Windows). No measured staging
  entry of the reference held home, so that guard is claustrum's own.
- **Systems and switch.** Linux, macOS and Windows, with the staging removes
  on Windows only. Always on, no switch.
- **Cost.** A caller that names home, or a folder that holds it, gets a refusal.
  On Windows claustrum refuses a `destDir` with the `\\?\` prefix and a dot or a
  space after its last name. There `5fd08069` extracts into the folder with no
  dot or space. For the last name `...` both answer
  `clean destDir: RemoveAll ...: invalid argument`.
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
  to `<parent>/x`, outside the cli-dir. With `../victim` it destroys the target
  (Linux, macOS and Windows).
- **Systems and switch.** Linux, macOS and Windows. Always on, no switch.
- **Cost.** None for a bare version: `1.0.86`, a commit sha and `latest` are
  measured as accepted. A lexical containment test was not taken, because it
  accepts `link/1.0.0` through an intermediate symlink. An `EvalSymlinks` test
  only adds a TOCTOU window.
- **Rule.** Rule 3 clause (b). The real client passes bare version strings. The
  evidence is an observed value plus a measured accepted set, not an
  enumeration.
- **Reopen trigger.** Desktop passes a `-cli-version` that is not a single path
  component.
- **Pointers.** `install.go`.

### D8 · claustrum refuses a link, a FIFO or another user's `remote-server.log`, and the `git-shadow` sweep writes nothing through a link (always-on) { #d8 }

- **Behavior.** After a failed rename to `remote-server.log.old`, the launcher
  judges the entry at the log path by `Lstat`.
  [PROTOCOL.md → Daemon log](PROTOCOL.md#daemon-log-remote-serverlog) has the
  steps that match the reference.
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
- **Reference.** On Linux it refuses the same entries only in a sticky
  world-writable folder with the `fs.protected` sysctls on. Elsewhere on Linux
  and macOS it writes through a link or into another user's file, and it blocks
  on a FIFO. On Windows it writes through a file symbolic link. Its sweep makes
  an empty lock file in the target of a link.
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

- **Behavior.** Before a new `-serve` daemon signals a live holder of the run-dir
  lock, it verifies that the holder is one of our own `-serve` processes bound to
  this socket. On Linux the reference and claustrum both verify the command line
  of the holder (claustrum reads `/proc/<pid>/cmdline`). On macOS the reference
  sends SIGTERM to the recorded pid unverified. It does so also to a holder that
  is not a serve process (measured on a macOS VM). claustrum reads the argument vector
  from `sysctl KERN_PROCARGS2` and refuses to signal a pid whose argv is not our
  `-serve` for this socket.
- **`-stop` too.** `-stop` runs the same holder check before it signals. On macOS
  the reference `-stop` signals any live holder whose lock record says role
  `serve` on this host's node. It sends `SIGTERM`. To a holder that ignores it,
  it sends `SIGKILL` 4 s later. claustrum does not signal such a holder, and prints
  `survivor`. On Linux the reference also refuses that holder, so Linux is
  parity.
- **Default.** Always-on, macOS only. On the honest path the live lock holder wrote
  its own pid into the record, so the verification passes and the outcome is
  identical to the reference (the predecessor is evicted).
- **Why always-on (rule 3 clause (a)).** Signalling an unverified pid is
  unrecoverable harm. A crash can leave a stale record whose pid an unrelated
  process reuses. A foreign process can also hold the flock. The reference then
  SIGTERMs that innocent process. No honest caller benefits from skipping the
  check. The same guard is always-on on Linux, so macOS matches the safety of
  Linux rather than the macOS gap of the reference.
- **Cost.** None on the honest path. Where the reference evicts and claustrum
  does not, the holder is un-inspectable or is not a serve process. There
  claustrum serves without run-dir ownership instead of killing the holder.
- **Reopen trigger.** The reference adding the same holder check on macOS (then this
  becomes parity, not a divergence). Or a legitimate macOS holder that
  `KERN_PROCARGS2` cannot read, reported as a failed handover.
- **Not the only identity gate.** The host cleaner has its own gate in
  `retireAbandoned`, before its SIGTERM. That gate is parity, not this entry.
  On Linux both daemons refuse a listener that is not their daemon binary, and
  one that serves another socket. On macOS the first case is measured equal,
  and the second is not measured.
- **Pointers.** [PROTOCOL.md → Run-dir lock](PROTOCOL.md#run-dir-lock-daemonlock)
  and `-stop`. Also `daemon_runlock_unix.go` (`holderSignalRefusal`,
  `stopRunDirHolder`), `daemon_runlock_darwin.go` (`realIsServeCmdline`,
  `procArgv`) and `daemon_runlock_linux.go`.

### D16 · `git.status` of a linked worktree returns the status on Windows, where the reference errors (Windows) { #d16 }

- **Behavior.** `git.status` with a `baseRepo` and a `path` that names a linked
  worktree returns the porcelain status of that worktree. On Linux and macOS the
  reference and claustrum are byte-identical. On Windows, with no global excludes
  file of the user, `5fd08069` returns `-32603 "exit status 128"` (Windows VM). claustrum returns the status result
  (`{"isRepo":true,"clean":false,"changes":[…]}`).
- **The cause.** The status call of the reference passes
  `-c core.excludesFile=NUL`, the Windows null device. With that value,
  `git status` of Git for Windows 2.55.0 exits 128 with
  `fatal: cannot use NUL as an exclude file`. With `/dev/null` it prints the
  status. With a user excludes file the reference passes that file, its status
  works, and D16 does not arise.
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
- **Pointers.** [git.status](protocol/git-status.md#differences-by-system). Also `gitstatus.go`
  (`statusChanges`) and `githarden.go` (`statusExcludesFile`).

### D19 · Refuse a junction between `baseRepo` and the leaf on remove (Windows, always-on) { #d19 }

- **Behavior.** On Windows, `git.worktree_remove` refuses a request with a junction
  at any directory between `baseRepo` and the leaf. The reply is
  `{"success":false,"error":"failed to remove worktree: openat <path from
  baseRepo>: path escapes from parent"}`. `<path from baseRepo>` is the folder that
  holds the leaf, relative to `baseRepo`. It is `.claude\\worktrees` in the frame
  for a junction at `.claude` or at `.claude\worktrees`. That text is claustrum's
  own, the error of `os.Root`. Nothing is deleted, and the branch stays.
- **The reference at `.claude` or `.claude\worktrees`.** It answers
  `{"success":true}` and deletes nothing but the branch. When the junction leads
  to a live worktree, that worktree is left on a deleted branch. With no worktree
  behind the junction it answers `{"success":true}` too. Measured on `f6010b97`,
  and on `89cb6289` for a branch that another ref reaches (Windows VM).
- **The reference at a junction above `.claude`.** For a leaf and a branch that
  do not exist, `89cb6289` answers `{"success":true}`. claustrum answers its
  refusal there with no git call. Nothing changes on disk on either side
  (Windows VM). Not measured: a live worktree behind such a
  junction, and the branch step there.
- **Default.** Always-on, Windows only. **Activate:** always-on. There is no flag
  and no key.
- **Why always-on.** Rule 3 clause (b), by the maintainer's decision of
  2026-09-27. The `git.worktree_create` of both daemons refuses a junction there
  with `mkdir_failed` and creates nothing. Above `.claude` both answer
  `failed to create parent directory: <P>\J is not a directory`, with equal
  frames. So neither daemon creates a worktree there. Only a worktree made
  outside the daemon sits there. The maintainer widened the entry on 2026-10-07
  to a junction at any directory between `baseRepo` and the leaf.
- **Cost.** A Windows client that diffs frames against the reference sees
  `success:false` where the reference sends `success:true`. A branch that the
  reference deletes stays with claustrum. A client that relies on the remove to
  delete the branch there must delete it itself.
- **Reopen trigger.** The reference refusing the junction with the same text (then
  this becomes parity), or the reference deleting the tree through the junction. Or a Windows
  client that depends on the success reply for a junction between `baseRepo` and
  the leaf.
- **Pointers.** [git.worktree_remove](protocol/git-worktree-remove.md#where-the-paths-can-be). Also
  `worktreeremove.go` (`openRemoveParent`).

### D20 · Wait for a settle before the group `SIGKILL` of a leader that reads as gone (Linux and macOS) { #d20 }

- **Behavior.** At a `-serve` start the daemon ends the child groups that a dead
  predecessor left: `SIGTERM` to the group, then a wait of 2 s. If the leader of a
  group reads as gone in that wait and the group still answers, the daemon sends
  `SIGKILL` to the group. claustrum first waits 50 ms and reads the leader again. A
  leader that is alive again takes the record test. A leader that fails that test
  gets no more signals. This is off-wire. No frame changes.
- **The reference.** `89cb6289` sends that `SIGKILL` with no wait, within 5 ms
  of the `SIGTERM` (Linux). A child that replaces its program on `SIGTERM` is
  then ended in about one run of four on Linux, and in no run on macOS. The
  reference also sends a group `SIGKILL` right after the `SIGTERM` in some runs
  where the child ends on `SIGTERM` (Linux). claustrum sends none there.
- **claustrum, measured.** With the wait, claustrum ended the child that
  replaces its program in no run, on Linux and on macOS.
- **Default.** Always-on, Linux and macOS. **Activate:** always-on. There is no
  flag and no key. The 50 ms is claustrum's own value.
- **Why always-on (rule 3 clause (a)).** The harm it refuses is a `SIGKILL` to a
  live process that is no longer the recorded child. That loss cannot be taken
  back. It is the same shape as [D15](#d15), which refuses to signal an identity
  that the daemon cannot verify. On the honest path the outcome stays inside what
  the reference shows. The child that replaces its program stays alive, as in
  most runs of the reference. A group whose leader exits still gets its
  `SIGKILL`. The only honest-path difference is the time of one signal at a
  daemon start, off the wire.
- **Cost.** The group `SIGKILL` comes about 50 ms later, on Linux and macOS. A
  reap whose groups all end on the `SIGTERM` returns one wait later.
- **The wait is a threshold.** A program replacement that takes longer than 50 ms
  still gets the `SIGKILL`. No measured run shows that.
- **Reopen trigger.** A measurement that shows the reference waiting before that
  `SIGKILL` (then this becomes parity). Or a report of a child that outlived a
  daemon restart because it replaced its program.
- **Pointers.** [PROTOCOL.md → Child records and the reap](PROTOCOL.md#child-records-and-the-reap-linux-and-macos). Also `childreap.go`
  (`settleGone`, `reapGoneLeader`).

### D22 · Refuse a worktree locked in the `.git` folder of `baseRepo` on remove (always-on) { #d22 }

- **Behavior.** `git.worktree_remove` refuses a worktree that is locked in the
  `.git` folder of `baseRepo`, and deletes nothing. In the plain state the
  reference refuses it too, with the same text, since `7d193f89`.
- **The four states that differ.** S is a worktree locked in T, the `.git`
  folder of `baseRepo`, and X is another repository. `89cb6289` answers
  success, and claustrum answers the locked refusal and deletes nothing:
  - The daemon has `GIT_DIR` and `GIT_COMMON_DIR` of X. The reference answers
    `{"success":true}` and deletes the folder of S. The entry of T with its
    `locked` file stays. Linux, macOS and Windows.
  - The daemon has `GIT_DIR` of X alone. The reference answers
    `{"success":true}` and deletes the folder of S. Linux, macOS and Windows.
  - `baseRepo` is `<T>/missing/..` with no folder `missing`. The reference
    answers `{"success":true,"branchKept":true}` and deletes the folder of S.
    Linux and macOS.
  - The environment of the first state, and the folder of S is gone. The
    reference answers `{"success":true}` and deletes nothing. claustrum answers
    the "is gone but its registration is locked" refusal. Linux, macOS and
    Windows.
- **With `worktreeRoot` (Linux).** With a daemon `GIT_DIR` of X, alone or with
  `GIT_COMMON_DIR`, the reference answers the transient `could not verify`
  text. With the folder gone it answers `{"success":true}`. It deletes nothing
  in these states, and claustrum answers the locked refusals. The end state is
  the same, and the frame and the git calls differ.
- **The exception.** On Linux and macOS a gone worktree whose locked entry sits
  behind a linked `<git dir>/worktrees` gets success, and its branch goes. That
  follows `5fd08069` (Linux VM), by the maintainer's decision of 2026-10-14.
- **How.** The removal can read its entries from another git directory. Then
  claustrum also reads the entries under `<baseRepo>/.git/worktrees`, with no git
  call, with and without `worktreeRoot`. For a folder there that it cannot read,
  claustrum answers the lock-check refusal and deletes nothing. A folder that does
  not exist counts as not locked. With no daemon environment both sides refuse.
- **Default.** Always-on, on every system. **Activate:** always-on. There is no
  flag and no key.
- **Why always-on.** The maintainer's decision of 2026-10-03. A lock is the
  user's own instruction not to delete, and a deleted folder cannot be taken
  back. No clause of rule 3 covers this entry. Rule 4 says that a change to a
  frame on an honest path is opt-in or does not ship. The maintainer accepted
  this frame difference. Each state needs something that the request does not
  create: a `GIT_DIR` in the daemon's environment, a `baseRepo` spelled through
  a missing name, or a folder removed by hand. No measurement shows a client
  that reaches one of them.
- **Cost.** In these states the frame differs: claustrum sends `success:false`
  with a locked text. Where the reference deletes the folder, the folder stays.
  A caller there must unlock the worktree first.
- **Not measured.** The reference on Windows with `<T>/missing/..`: Windows
  refuses that `baseRepo` on both sides. A daemon `GIT_COMMON_DIR` of a third
  repository. A daemon environment with a `<baseRepo>/.git/worktrees` folder
  that cannot be read.
- **Not covered.** A `baseRepo` with no `.git` folder of its own: a subfolder of T,
  a linked worktree or a submodule. With a daemon `GIT_DIR` of another repository,
  claustrum does not read the lock in the real repository there. It deletes the
  locked worktree, as the reference does in the first state. The maintainer
  decided on 2026-10-03 to keep that. The reference is not measured there.
- **Reopen trigger.** A caller that needs the removal of a locked worktree in
  these states. Or a measurement that shows the reference refusing there (then
  this becomes parity).
- **Pointers.** [git.worktree_remove](protocol/git-worktree-remove.md#the-repository-checks). Also
  `worktreeremove.go` (`lockedInBaseRepo`) and `methods_git.go`.

### D23 · The environment block of a child is in name order (Windows) { #d23 }

On Windows claustrum gives a child its environment block in name order. The
reference does not sort it. Go sorts the block since 1.26, and the reference
builds use go1.25.14. A claustrum build with go1.25.14 matched the order of
`5fd08069` (Windows VM, 2026-10-13). That covers `process.spawn`, the git
children and `-install`.

claustrum stays on Go 1.26 (maintainer's decision, 2026-10-13). Go 1.26.9 fixed
standard library advisories of go1.25.14, one in the folder handles of the
Windows file guards (GO-2026-6604). The entry is always-on, with no flag and no
key.

Reopen trigger: the reference moves to Go 1.26 or newer.

### D24 · In the create rollback, keep a folder that replaced the new registration and run no git call after a `.git` file swap { #d24 }

- **Behavior.** `git.worktree_create` tests the registration of the new
  worktree right after the add. When the tests accept it, claustrum takes the
  identity of the registration folder. It holds that folder open until the
  create answers, so a new folder cannot get that identity. If the open fails,
  the identity comes from a stat of the path (not measured).
  - A rollback deletes the folder at that path in one case only: it still has
    that identity. The test runs on the registrations directory that the delete
    opened. The rollback adds no text to the frame, and the branch step runs.
  - After a failed `rev-parse --show-toplevel`, the rollback deletes the one new
    entry of the listing after the add, with the identity from that listing. If
    a sibling registration was moved to that name during the call, `5fd08069`
    deletes it, and claustrum keeps it (Linux VM).
  - The placement of the index has the same test. If the folder is not the
    accepted one, claustrum places nothing. It answers a failed checkout with
    its own text,
    `the registration <entry> is not the folder that was tested after the add`.
  - On Windows the same tests guard the registration that the staging step
    moved. The identity is that of the staging folder, held open from its mkdir
    to the end of the move. The text there is
    `the registration <entry> is not the folder that was moved into place`.
  If git gave no answer to `rev-parse --absolute-git-dir`, the guard does not
  run.
- **A replaced folder.** A process renames the new
  registration and puts another folder at its name before the rollback.
  On Linux and macOS `89cb6289` removes the folder at the name. That folder can be the
  registration of a live sibling worktree. claustrum removes no folder of the
  registrations directory. On Linux and macOS the frame is the plain failed
  checkout on both sides, and the leaf and the branch are gone on both. On Windows `5fd08069`
  deletes the folder that took the name of the moved registration, claustrum
  keeps it, and the frames are equal.
- **A `.git` file swap (Windows).** Right after the add, `.git` becomes a file
  `gitdir: <the git directory of a second repository>`. `5fd08069` runs the
  branch step in that repository: it deletes a branch of the same name there
  that `main` reaches, or keeps it with `branchKept`. claustrum runs no git call
  after the swap and changes nothing in that repository. Its undo text says
  that the branch still exists. The frames differ.
- **Default.** Always-on. **Activate:** always-on. There is no
  flag and no key.
- **Why always-on.** The maintainer's decision of 2026-10-07: the guard stays.
  The harm that it refuses is the loss of the registration of another worktree.
  The decision of 2026-10-19 adds the `.git` file swap, where the harm is a
  branch deleted in the repository that the file names. No clause of rule 3
  covers this entry. It stands on those decisions, and the reopen trigger takes
  it back.
- **Cost.** For a replaced folder, a folder stays that the reference removes, and
  no frame differs. To reach the guard, the registrations folder must change
  between the add and the rollback. After a `.git` file swap the frame
  differs, and a branch can stay that `5fd08069` deletes.
- **Not measured.** The guard at the placement of the index, on every system. A
  replaced folder with a daemon `GIT_DIR` of another repository.
- **Reopen trigger.** A measurement that shows the reference keeping such a
  folder. Or a decision to match its disk for a replaced folder. Or a decision
  to run the branch step in the repository that the `.git` file names.
- **Pointers.** [git.worktree_create](protocol/git-worktree-create.md#after-a-failure).

### D27 · Keep a stale entry whose record names a path that exists (Windows) { #d27 }

- **Behavior.** On Windows the stale step of `git.worktree_create` compares the
  `gitdir` record of each entry with the new leaf without regard to letter case.
  Before it deletes an entry that matched, it tests the path that the record
  names, as written, with `Lstat`. If that path exists, the entry stays. Both
  forms of the step have the test: through the held git directory and by the
  path. Linux and macOS have an exact compare, so the missing leaf already
  proves that the entry is stale there.
- **The state that differs.** A folder `.claude\worktrees` with case sensitivity
  on holds a live worktree `W1`, and a client creates `w1`. `5fd08069` deletes
  `.git\worktrees\W1` before the add and answers success. `W1` then reads the
  records of `w1`, and its own branch is no longer checked out (Windows VM).
  claustrum keeps the entry, and `W1` stays on its branch. The create answers
  `{"success":false,"error":"git worktree add failed: Preparing worktree (new branch 'b1') fatal: '<repo>/.claude/worktrees/w1' is a missing but already registered worktree; use 'add -f' to override, or 'prune' or 'remove' to clear","errorCode":"worktree_add_failed"}`.
  The branch `b1` stays, because git makes it before it refuses.
- **Default.** On-wire, always-on, Windows only. **Activate:** always-on.
- **Why always-on.** The reference destroys the records of a live worktree in a
  state that a client can reach. The maintainer's decision of 2026-10-18.
- **Cost.** The frame above. None where the folder of a stale entry is gone
  (from the code). A `Lstat` that fails with another error than "does not
  exist" keeps a stale entry too, and then the add fails.
- **Reopen trigger.** A measurement that shows `5fd08069` with the same test.
- **Pointers.** [git.worktree_create](protocol/git-worktree-create.md#differences-by-system).

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach { #ct-1 }

- `process.spawn` / `process.reattach` accept an optional `"wantPid":true`. The
  reply of a spawn, and of a reattach that finds the process, then gains `pid`
  (the child's OS pid) and `startTime`. This is the first wire-surface
  *extension*.
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
- **Pointers.** The pages [process.spawn](protocol/process-spawn.md#response) and
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
  record of them and sends them no signal. This rule is claustrum's own. On a
  Linux VM, `-stop` with the flag sent no kill and removed the records. The
  children lived on, and a new daemon sent them no signal.
- Two limits of that rule. It covers the processes of the process table only. A
  process whose `id` a later spawn took keeps its record, so the next start ends
  it. The host cleaner does not read the records. By its own rule it ends an
  orphaned Claude Code group under the install root, a kept one too. That is not
  measured with the flag.
- **Windows.** The flag works there too. A child is in no Job Object, so the exit
  of the daemon does not end it, on the reference too. With the flag, `-stop` and
  `server.shutdown` leave the direct child alive (measured on a Windows VM).
- The reference has no such flag: `89cb6289` exits with code 2 for it.
- **Activate:** `-keep-children` or the `keep-children` key.
- **Pointers.** [PROTOCOL.md → -serve](PROTOCOL.md#-serve-run-the-daemon).

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
  `server.capabilities` still reports claustrum's own version.
- Fail-safe and hardened: regular-file-only via `Lstat`, `io.LimitReader` ≤ 64 KiB,
  per-key validation (`version-override` gated to
  `^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$` and lower-cased). claustrum uses every
  value as data, never as a format string. Any doubt → stock. Startup never fails.
- **Pointers.** [PROTOCOL.md → -version](PROTOCOL.md#-version), and `config.go`.

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
- **Pointers.** [PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken), and `tokenpersist.go`.

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
- **Pointers.** [PROTOCOL.md → Named-pipe transport](PROTOCOL.md#named-pipe-transport-windows-opt-in).
  Also `pipetransport.go` and `pipetransport_windows.go`.

## Retired entries

A retired entry is a divergence that a later reference build, or a later
measurement of the reference, made moot. The maintainer also retires an entry to
follow the reference. Its number is not reused, and its old
link points here. Where a retired entry had a flag, the flag and its
`claustrum.conf` key are still accepted, set nothing, and log one warning. The
warning comes in the mode of the flag, `-serve` or `-install`. A duration flag
whose value does not parse fails the flag parse with exit 2 (from the code).

### D1 · Verify the `-cli-zst` blob against a supplied checksum (retired) { #d1 }

Since `7d193f89` the reference checks a supplied `-cli-checksum` against the
`-cli-zst` blob, and claustrum does the same. The compare is case-sensitive, and
a mismatch answers `checksum mismatch` and keeps the blob.

### D4 · `files.read` refuses a file that is not regular (retired) { #d4 }

D4 made that refusal opt-in, because older builds read such a path. `5fd08069`
refuses it with `-32602 files.read: not a regular file` (Linux, macOS and
Windows VMs), and claustrum now does the same with no switch. The flag
`-files-read-regular-only` warns on `-serve`.

### D5 · Deadline on the `git` calls (retired) { #d5 }

D5 made a deadline on the git calls opt-in. `5fd08069` stops the git calls of
four methods at measured limits, and claustrum now has those limits, always. See
[PROTOCOL.md → Time limits of git calls](PROTOCOL.md#time-limits-of-git-calls).
The flag `-git-timeout` warns on `-serve`.

### D7 · `-cli-version` must not collide with the install temp sweep (retired) { #d7 }

Since `4534d86` the sweep removes an entry that is about 10 minutes old or more.
So the reference installs a version such as `.fetch-1` and keeps it. claustrum
does the same, and [D6](#d6) still applies to every version.

### D9 · Namespace-wide params binding (retired) { #d9 }

A method now ignores a member that another method of its namespace reads, as
`5fd08069` does (Linux, macOS and Windows VMs).

### D11 · Deadline on the `<cli> --version` run of `-install` (retired) { #d11 }

The reference stops a present CLI at 30 s (Linux, macOS and Windows). It stops
a CLI that it installed in the same run at 120 s. It answers a `cli unresponsive: …`
`cliError` with `"cliUnresponsive":true`. claustrum now does the
same, always. The flag `-cli-probe-timeout` warns on `-install`.

### D13 · `-install` verified the checksum before decompressing (retired) { #d13 }

`5fd08069` decompresses first and answers the decoder text with a right or a
wrong checksum. claustrum now decompresses into the staging file first and
verifies before the chmod, the clear and the rename.

### D14 · Deadline on the `ldd --version` libc probe (retired) { #d14 }

Since `19f30c46` the reference bounds `ldd` at 5 s and then waits up to about
2 s for its output. claustrum now does the same, always. The flag
`-libc-probe-timeout` warns on `-install`.

### D17 · An abandoned `lsof` run reads as busy, not idle (retired) { #d17 }

Take an idle daemon whose `lsof` run `SIGKILL` cannot end. There `5fd08069`
also sends no SIGTERM. It keeps the run dir and logs the same refusal line
(macOS VM).
claustrum takes longer for that pass, but no signal and no folder differ, so it
is no divergence.

### D18 · `-cli-version` must not start with `.blob-` (retired) { #d18 }

`5fd08069` counts and removes no `.blob-` file, because its prune counts version
files only (Linux, macOS and Windows VMs). claustrum writes no `.blob-` file
now, and it accepts such a version.

### D21 · A second daemon on a live socket appends to `remote-server.log` (retired) { #d21 }

On Windows the launcher now truncates a held log, and each daemon writes at its
own offset, as `5fd08069` does (Windows VM).

### D25 · Answer `"unavailable"` for the peer check and serve every caller (retired) { #d25 }

On Linux claustrum now checks the namespaces of each caller, with the frames of
`5fd08069` (Linux VM).

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
