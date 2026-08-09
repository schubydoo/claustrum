# claustrum — deliberate divergences

Almost everything claustrum does is byte-identical to the reference daemon. This
file is the canonical catalog of the exceptions — the behaviours that *knowingly*
change a frame or an action — plus the rules that gate every one of them.

The [catalog table](#catalog) is the fast path: one row per divergence, with its
default, how to activate it, and its reopen trigger. The [rules](#how-we-decide-the-rules)
come first because which shape an entry may take — always-on, opt-in, or
conditional — is not a matter of taste; it follows from them.

Per-method wire facts (params, result field order, error strings) live in
[PROTOCOL.md](PROTOCOL.md). Driver-claim provenance lives in
[ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance). Exhaustive
per-entry measurement forensics were condensed out of the committed docs.

## The one hard rule

Byte-identical JSON-RPC frames are the product. A divergence needs a reason;
matching does not. "Off by default" must mean byte-identical, not nearly — an
opt-in divergence that is not activated leaves the wire exactly as the reference
leaves it.

Every intentional divergence below is also documented in
[PROTOCOL.md](PROTOCOL.md) and in the PR that shipped it. The inverse — changes we
will never make — is the [out-of-scope list](#explicitly-out-of-scope-would-break-compatibility)
at the end.

## How we decide (the rules)

The standard, in priority order:

> 1. **Claude Desktop must keep working.** Claustrum is a drop-in. If a behaviour
>    is reachable by Desktop and matching the reference is what keeps it working,
>    we match — no matter how ugly the reference's behaviour is.
> 2. **Match by default.** A wart Desktop tolerates is the contract, not a bug.
>    Divergence needs a reason; matching does not. The burden of proof is always
>    on the divergence.
> 3. **A divergence earns ALWAYS-ON only if** *either* **(a)** the reference's
>    behaviour on that path **is not a frame at all** — an unbounded wait,
>    unbounded memory, or unrecoverable data loss — **and** no honest caller can
>    observe the difference; *or* **(b)** the **trigger itself** is unreachable on
>    an honest path; *or* **(c)** the trigger is reachable, but **both binaries
>    fail the operation** and the only delta is diagnostic text.
> 4. **Anything else that changes an honest-path frame is OPT-IN, default off**, or
>    it does not ship.

Corollaries:

- **Keeping a wart does not mean hiding it.** We match *and* document it, so a user
  can see the edge before it bites.
- **"The reference does it too" is a reason to match, never a reason to call it
  safe.** D2 is the standing counter-example: the reference wipes a home directory
  and we still refuse.
- **Every always-on divergence owes a reopen trigger** — the observation that would
  make us take it back out. An always-on divergence with no reopen trigger is a
  preference, not a decision.

### Reading the clauses

**Clause (a) is an AND.** Both halves must hold: the reference's behaviour is not a
frame, *and* no honest caller observes the difference. The second half is where
thresholds fail. A bound is a *threshold*, so it cannot separate a hostile input
from a merely slow or large one — an honest input trips it too. The test:

> **Who pays when the guard fires on an honest input, and can they decline?**

If the honest input is reachable and the caller cannot turn the guard off,
always-on is not justified no matter how the reference behaves on the hostile path.
That is what flipped every timeout and size cap (D3, D5, D10, D11, D12, D14; D4 is
the non-threshold sibling) from always-on to opt-in.

*Canonical example:* D2 satisfies both halves — the reference's home-wipe is
unrecoverable data loss, and no honest caller has a legitimate *use* for deleting
home (it can still be reached by accident, which is exactly what the guard is for).

**Clause (b): the trigger is unreachable on an honest path.** Most surviving
always-on entries use this form (D6, D7, D8, D9), each with its own trigger — the
glosses are not interchangeable. Two of the four are asserted rather than
enumerated: Desktop's per-method param set has never been enumerated against D9's
binding, and D6/D7 rest on an observed value plus a measured accepted-set. Read
them as unenumerated, not established (rule 2 puts the burden on the divergence).

*Canonical example:* D6 — a `-cli-version` naming a destructive path outside the
cli-dir is not something any correct client emits.

**Clause (c): both binaries fail, and the only delta is diagnostic text.**
Deliberately narrow, written for D13, and — measured — D13 does not meet it, so it
justifies no entry in this file today. Two things a reader should not take on
trust: an `error.code` is not diagnostic text (a client branches on it), and
neither is on-disk state a caller can `files.stat`. D13's honest-path rows differ
in both, so the clause keeps its literal wording and D13 sits
[unresolved](#d13) rather than have the clause widened to fit its one candidate.

*Canonical example:* none currently qualifies.

### The "Desktop owns the argv" premise

Every opt-in tag rests on one claim about the driver: Claude Desktop owns the
daemon's argv on both `-serve` and `-install`, so an operator cannot reach a
flag-only knob and the `claustrum.conf` key (read beside the executable) is the
reachable one. It is the premise under D3, D4, D5, D10, D11, D12, D14 and the
"(opt-in)" tag itself.

Because it is load-bearing, it is *tracked*, not assumed. Its provenance, current
evidence, and reopen trigger are canonical in
[ARCHITECTURE.md → Driver claims and their provenance](ARCHITECTURE.md#driver-claims-and-their-provenance),
alongside the other two driver claims (Desktop parses `cliError`; Desktop uses the
reported `libc` to choose which CLI build it downloads). It reopens if Desktop
turns out to have a way for an operator to influence the daemon's argv — which
would make a flag-only opt-in sufficient for Desktop-driven hosts, without mooting
the config key (which serves every other driver).

## Conventions for opt-in divergences

These hold for every opt-in entry, stated once here rather than repeated per entry:

- **A flag and a matching `claustrum.conf` key.** The config key is the reachable
  knob (see the argv premise above). Precedence is **explicit CLI flag > config >
  default**, resolved via `flag.Visit`.
- **Default off is the zero value, and disabled bypasses the guard entirely.** A
  cap set to `0` skips its `io.LimitReader`; a timeout set to `0` skips
  `context.WithTimeout` (or, for the download, relies on `http.Client{Timeout: 0}`
  being the stdlib's own "no timeout"). Never a huge-but-finite value — the `cap+1`
  / armed-cancel arithmetic is what defines the boundary, and routing the unlimited
  case through it invents a boundary the reference does not have.
- **No opt-in bound is a hang detector.** Each is a threshold, so an
  honest-but-slow or honest-but-large input trips it too. That is precisely why
  they are off by default.
- **At the shipped defaults, no claustrum-chosen `-install` wall-clock bound
  applies.** Only stdlib transport clocks remain on the `-cli-url` path
  (`net.Dialer{Timeout: 30s}`, `TLSHandshakeTimeout: 10s`) — always-on, unnumbered,
  and unprobed on the reference.

## Catalog

| ID | What it does | Default | How to activate | Why (rule / clause) | Reopen trigger |
|----|--------------|---------|-----------------|---------------------|----------------|
| [D1](#d1) | SHA-256-verify the local `-cli-zst` blob | trusting (no verify) | conditional — caller supplies `-cli-checksum` | rule 3: only a *wrong* checksum pays | Desktop supplying a checksum mismatching its own SFTP blob |
| [D2](#d2) | Refuse a destructive path that is or contains `$HOME` | always-on | always-on | rule 3 clause (a) | an honest caller legitimately targeting a path that is/contains home |
| [D3](#d3) | Cap `files.extract_tar` output size | off (`0` = unlimited) | `-max-extract-bytes` / `max-extract-bytes` | rule 4 (who-pays) | operator's cap refuses a legit extraction, or default lets a bomb through |
| [D4](#d4) | `files.read` refuses non-regular files | off | `-files-read-regular-only` / key | rule 4 | opt-in refuses a legit read; or default parks/OOMs the daemon in normal use |
| [D5](#d5) | Deadline on every `git` invocation | off (`0`) | `-git-timeout` / key | rule 4 | opt-in kills an honest slow git |
| [D6](#d6) | `-cli-version` must be a single path component | always-on | always-on | rule 3 clause (b) | Desktop passing a multi-component `-cli-version` |
| [D7](#d7) | `-cli-version` must not collide with the temp sweep | always-on | always-on | rule 3 clause (b) | Desktop passing `.fetch-*` or `*.zst` |
| [D8](#d8) | Decline (not share) a foreign-owned `remote-server.log` | always-on | always-on | rule 3 clause (b) — unreachable on the deployed path | a shared socket dir that also needs the log file |
| [D9](#d9) | Namespace-wide params binding (type error in an unread field → `-32602`) | always-on | always-on | rule 3 clause (b) | a real client sending a type-mismatched unread namespace field |
| [D10](#d10) | Cap `-install` CLI size (decompressed + download body) | off (`0`) | `-max-cli-bytes` / key | rule 4 (who-pays) | Desktop ceasing to treat a disk-full message as terminal |
| [D11](#d11) | Deadline on the `<cli> --version` runnability probe | off (`0`) | `-cli-probe-timeout` / key | rule 4 | Desktop turning out not to parse `cliError` (retraction rider) |
| [D12](#d12) | Bound on the `-install` download exchange | off (`0`) | `-cli-download-timeout` / key | rule 4 | operator with the bound set reporting an honest slow download failed |
| [D13](#d13) | Verify checksum before decompressing (`-cli-url`) | always-on | always-on | **UNRESOLVED** — clause (c) written for it, measured not met | any change to how Desktop classifies `cliError` |
| [D14](#d14) | Deadline on the `ldd --version` libc probe (linux) | off (`0`) | `-libc-probe-timeout` / key | rule 4 | a musl host the glob misses whose `ldd` exits 0 + is slow; or the reference bounding it above 45 s |
| [CT-1](#ct-1) | Opt-in `wantPid` → `pid` + `startTime` on spawn/reattach | off (fields omitted) | caller sends `"wantPid":true` | sanctioned optional-param extension | — (additive, degrades both ways) |
| [CT-2](#ct-2) | `-keep-children` leaves the child tree running on shutdown | off | `-keep-children` / `keep-children` key | off-wire opt-in extension | — |
| [CT-3](#ct-3) | `claustrum.conf` config file | absent ⇒ stock | create the file | the opt-in mechanism itself | — |
| [CT-4](#ct-4) | Hardened token persistence | not built (deferred idea) | — | deferred | — |
| [CT-5](#ct-5) | `-listen-pipe` Windows named-pipe transport | off | `-listen-pipe` / `listen-pipe` key (Windows) | additive opt-in transport | — |

Tags: **opt-in** = operator-declinable (flag + config key). **conditional** =
activated by the caller (D1). **always-on** = no switch. The CT block uses
"opt-in" in the looser "off unless asked for" sense — CT-1 is caller-activated and
CT-3 *is* the config mechanism, so neither is operator-declinable; only CT-2 and
CT-5 carry a flag and a key.

## Entries

### D1 · Re-harden the `-cli-zst` checksum (conditional) { #d1 }

- **Behavior.** The reference verifies `-cli-checksum` only on the `-cli-url`
  download, not on the local `-cli-zst` (SFTP) blob. claustrum SHA-256-verifies
  `-cli-zst` **when and only when** a `-cli-checksum` is supplied; a mismatch is
  rejected with `checksum mismatch: …` (source blob left intact). An absent/empty
  checksum stays trusting → byte-identical to the reference.
- **Why conditional, not opt-in.** It is activated by the *caller* supplying
  `-cli-checksum` (on `-install`, that caller is Desktop), not by an operator, so
  it has no flag or config key. It needs none: the delta requires a *wrong*
  checksum — a correct one is byte-identical — so no honest caller pays for it.
- **Observable delta** (supplied-wrong checksum only): a valid blob the reference
  would install returns `checksum mismatch`; a corrupt blob returns `checksum
  mismatch` instead of `decompressing: …`.
- **Reopen trigger.** Desktop supplying a `-cli-checksum` that does not match the
  blob it uploaded over SFTP — conditional is not the same as unreachable.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`; `install.go`.

### D2 · Refuse a home directory as a destructive path target (always-on) { #d2 }

- **Behavior.** Two methods hand a caller-supplied, `~`-expanded path to
  `os.RemoveAll`: `files.extract_tar` wipes `destDir`, and `git.worktree_remove`
  deletes `worktreePath` when git exits non-zero. `wipesHomeDir` (`homeguard.go`)
  refuses any target that **is or contains** the home directory. Descendants stay
  allowed — extracting into `~/.claude/…` is the daemon's own install path.
- **Containment is the test, and the predicate resolves relative paths**
  (`filepath.Abs`) before comparing. Without that, `"worktreePath":".."` from a
  daemon whose cwd is under home deletes it (measured). `git.worktree_remove` is
  the more exposed of the two: `wipesHomeDir` is its *only* gate, where
  `extract_tar` keeps `IsAbs` + `isFilesystemRoot` behind it as well.
- **This fired.** On 2026-08-02 an in-repo fuzzer sent `"destDir":"~"` at a live
  daemon and destroyed the maintainer's home directory. `"~"` is the first value in
  the adversarial list that survives the old `IsAbs && !isFilesystemRoot` gate — a
  home directory is exactly "absolute and not a filesystem root".
- **Why always-on.** Satisfies both halves of rule 3 clause (a): the reference's
  behaviour is unrecoverable data loss — measured, it destroys home on *both*
  methods — and no honest caller has a legitimate *use* for deleting home. It can be
  reached by accident, which is the point; what none has is a use for it.
- **Not a security boundary.** The socket + token already grant `process.spawn`
  ([SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
  This stops the accidental, generated, or fat-fingered path; symlinks are not
  resolved.
- **Reopen trigger.** An honest caller legitimately naming a destructive target
  that *is or contains* a home directory.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → both methods; `homeguard.go`,
  `homeguard_test.go` (the destructive call is seamed via `wipeDestDir`, so the
  suite is safe against an unfixed tree). Measurement: forensics.

### D3 · Make the `files.extract_tar` size cap opt-in { #d3 }

- **Behavior.** `maxExtractBytes` caps extraction output; over-cap returns
  `{"success":false,"fileCount":0,"error":"extraction size limit exceeded"}` and
  removes the truncated entry.
- **Default.** `0` = unlimited = byte-identical. **Activate:** `-max-extract-bytes
  <n>` or the `max-extract-bytes` key; disabled bypasses `io.LimitReader`
  (`io.Copy(out, tr)`).
- **Why opt-in.** Measured, the reference completes a 629 MB extraction with no cap
  at the pin — a frame, not an unbounded wait — so a non-zero default fails an
  extraction the reference completes, and Desktop owns the argv. It cleared clause
  (a)'s not-a-frame half; the who-pays cost flips it (rule 4).
- **Reopen trigger.** An operator's cap refusing a legitimate extraction, or the
  default letting a size bomb through in normal use.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md); `methods_files.go`. Measurement:
  forensics.

### D4 · Make the `files.read` regular-file guard opt-in { #d4 }

- **Behavior.** With the guard on, `files.read` refuses any non-regular path with
  `-32602 files.read: not a regular file`; the reference refuses none. Off, the
  predicate (`filesReadRegularOnly && !fi.Mode().IsRegular()`) is not evaluated at
  all.
- **Default.** Off (byte-identical). **Activate:** `-files-read-regular-only` or
  the key.
- **Why a flag and not a narrower predicate.** `/dev/null` and `/dev/zero` are
  indistinguishable by mode, so any predicate that admits the first admits the
  second. Whole or nothing.
- **The default has two measured costs** (both the reference's own behaviour too): a
  writerless FIFO parks a request goroutine *and* a descriptor (linux reserves the
  fd number before blocking, drawing down `RLIMIT_NOFILE`, which `accept()` shares),
  and an unbounded device read (`/dev/zero`) never reaches EOF — under a 2 GiB cgroup
  cap the kernel OOM-killed both binaries. `maxBytes` cannot save you: it keys off
  the stat size, `0` for every non-regular kind on linux.
- **Why opt-in.** Across six non-regular shapes, claustrum-off matches the
  reference byte-for-byte; the always-on guard cost an honest `/dev/null` read a
  `-32602` the reference never produces (rule 4).
- **Reopen trigger.** An operator with the flag set reporting a legitimate read
  refused; or — the direction that would say the default is wrong — a report of the
  daemon parked or OOMed by a non-regular read in normal use.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `files.read` → Non-regular files.
  Full table, OOM/fd reasoning, unmeasured shapes: forensics.

### D5 · Make the `gitTimeout` deadline opt-in { #d5 }

- **Behavior.** With the deadline on, every git invocation is bounded (shared
  `gitCtx` across `git` / `gitStdoutErr` / `gitDeadline`). On `git.worktree_remove`
  a hit answers `git worktree remove timed out after <dur>; no cleanup was
  attempted, and git may have partially removed the worktree` and deletes nothing;
  on `git.status` / `git.list_branches` it surfaces as `-32603 signal: killed`; a
  killed repo-detection call answers `isRepo:false`.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-git-timeout
  <dur>` or the key; disabled bypasses `context.WithTimeout`.
- **A timeout must never be read as "git refused."** `git.worktree_remove` treats a
  failed git as permission to delete `worktreePath`, so the timeout reply is
  separated from the failure arm. The cap is also softer than it reads:
  `CombinedOutput` waits on git's output pipe, so a git leaving a surviving child
  stays blocked past the deadline.
- **One opted-in arm loses data silently.** `copyWorktreeIncludes`
  (`worktreecopy.go`) killed mid-`git ls-files` takes the early return, and
  `populateWorktree` is best-effort — so `git.worktree_create` still answers
  `{"success":true}` while every manifest-selected file is missing. No frame moves.
  Absent at the default.
- **Why opt-in.** The reference showed no deadline at or below 75 s on
  `worktree_remove`; an honest 61 s git was never measured. Cleared clause (a)'s
  not-a-frame half; the `-32603 signal: killed` arm is an honest caller observing
  the difference (rule 4).
- **Reopen trigger.** An operator with `-git-timeout` set reporting an honest slow
  git killed by it — the `-32603` arm makes a single report enough.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `git.worktree_remove`,
  `git.list_branches`; `methods_git.go`, `worktreecopy.go`.

### D6 · `-cli-version` must name a single path component (always-on) { #d6 }

- **Behavior.** The install's clearing step is
  `os.RemoveAll(filepath.Join(cliDir, cliVersion))`, so a version reaching outside
  the cli-dir deletes unrelated data. claustrum answers `cli version "…" must be a
  single path component` and touches nothing; `.`, `..`, and both `/` and `\` are
  refused on every OS, so the accepted set does not change with the platform.
- **A single component rather than a lexical containment check.** A lexical check
  accepts `link/1.0.0` (an intermediate symlink under the cli-dir, followed at open
  time), and `EvalSymlinks` would only add a TOCTOU window before the `RemoveAll`. A
  final component that is itself a symlink stays legal (`os.RemoveAll` unlinks it
  rather than following) — so the rule is narrower than "no symlinks".
- **Why always-on.** Rule 3 clause (b) — measured, the reference destroys the
  target on both `../victim` and `link/1.0.0`; the real client passes bare version
  strings (`1.0.86`, a commit sha, `latest`, all measured accepted). The evidence is
  an observed value plus a measured accepted-set, not an enumeration.
- **Reopen trigger.** Desktop passing a `-cli-version` that is not a single path
  component.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`; `install.go`.

### D7 · `-cli-version` must not collide with the install temp sweep (always-on) { #d7 }

- **Behavior.** The orphan sweep claims `.fetch-*` and `*.zst` after *every*
  install, so `-cli-version .fetch-x` or `1.0.zst` installs correctly and is deleted
  moments later in the same run (measured: both binaries finish with an empty
  cli-dir and no `cliError`, reporting success having installed nothing). claustrum
  answers `cli version "…" collides with the install temp sweep` instead. The sweep
  predicate and this check share one definition, so they cannot drift apart.
- **Unlike D6 this gives up exact parity** (an error beats a success that installed
  nothing) — but that preference is not what earns it; clause (b) is.
- **Why always-on.** Rule 3 clause (b) — same evidence as D6.
- **Reopen trigger.** Desktop passing a `-cli-version` matching `.fetch-*` or
  `*.zst` (one observation reopens D6 too — both rest on the same evidence).
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`; `install.go`.

### D8 · `remote-server.log` is declined rather than shared (always-on) { #d8 }

- **Behavior.** Recreating the log fresh on every start (unlink + create, not
  truncate in place) is measured parity. The divergence is only the fallback: when
  the existing log cannot be replaced — a sticky directory holding another user's
  file — claustrum declines the log and falls back to inherited stdio, where the
  reference truncates and writes its diagnostics into the foreign file (measured
  2026-08-06).
- **Hardening, not a defect claim.** Reaching it needs a local user who can already
  plant a file in that directory. The justification is a design position — a daemon
  should not write into a file it does not own — and does not depend on the reference
  being wrong.
- **Why always-on.** Rule 3 clause (b) — unreachable on the deployed path
  (`~/.claude/remote/` is per-user, not world-writable), which is also the only
  place the reference's behaviour is a disclosure risk. A flag would gate a branch
  no honest deployment reaches.
- **Reopen trigger.** A deployment that puts the socket directory somewhere shared
  *and* needs the log file — there the fallback sends diagnostics to the launcher's
  stdio, which a client may parse.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Daemon log; `server.go`.

### D9 · Namespace-wide params binding is stricter than the reference's (always-on) { #d9 }

- **Behavior.** claustrum binds `params` into one struct per namespace
  (`pathParams`, `gitParams`), so a field valid for the *namespace* but unused by
  *this* method still participates in decoding — a type-mismatched value there
  answers `-32602` (e.g. `files.stat {"maxBytes":"{"}`, `git.status
  {"baseRepo":[1,2]}`). The reference binds only the field the method reads and
  ignores the rest. A genuinely unknown key is ignored by both.
- **Why always-on.** Rule 3 clause (b) — the trigger is a type error in a field the
  method does not read (a client bug). Stated honestly: this is narrower than "a
  real client never sends them"; Desktop's per-method param set has never been
  enumerated against this binding, so it is unenumerated, not established.
- **Reopen trigger.** Any real client sending a type-mismatched value in a
  namespace field the target method does not read — also the measurement this entry
  owes and does not have.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Params presence and typing.

### D10 · Make the `-install` CLI size cap opt-in { #d10 }

- **Behavior.** `maxCLIBytes` governs two reads — the decompressed CLI
  (`decompressing: decompressed CLI exceeds <n> bytes`) and the HTTP download body
  (`download failed: response exceeds <n> bytes`).
- **Default.** `0` = unlimited (byte-identical). **Activate:** `-max-cli-bytes <n>`
  or the key; both call sites bypass their `LimitReader` when disabled.
- **The blob is streamed, never buffered** — a `.blob-<random>` temp (or
  `$TMPDIR/claustrum-fetch-<random>` on a first install, before the cli-dir exists),
  hashed in one pass, so "cap off" does not mean unbounded memory (measured
  886 MB → 10 MB on a 400 MiB payload). **`.blob-`, not `.fetch-`, is load-bearing:**
  `.fetch-*` is the swept namespace, so a `.fetch-` blob would be deleted by a
  concurrent install's sweep and defeat the staging retry (`errStagingVanished`).
  `blobTempPrefix` is read by the creator, both housekeeping passes, and
  `validateCLIVersion`.
- **Who pays for opting in.** A cap set below free space replaces a disk-full report
  with the cap's own message, costing the user the free-space hint — but that cost
  turns on the `cliError` driver claim
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)). At the
  default the disk-full report is preserved and matches the reference.
- **Why opt-in.** Measured, the reference takes a 600 MiB payload to the runnability
  check on both the decompress and download paths — a frame, not an unbounded wait —
  so a non-zero default fails an install the reference completes, and Desktop owns
  the argv (rule 4).
- **Reopen trigger.** Desktop ceasing to treat a disk-full message as terminal
  (removes the cost). One plausible Desktop change — broadening its terminal match
  to any `decompressing:` error — fires this trigger and D13's at once.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md); `install.go` (`fetchToFile`,
  `zstdDecompress`). RSS and cap-below-free-space tables: forensics.

### D11 · Make the `-install` runnability probe deadline opt-in { #d11 }

- **Behavior.** `isRunnable` runs `<cli> --version`. There are two probe sites (the
  cache-hit guard and after extraction); a timeout on the first is
  indistinguishable from a cache miss, so an opted-in timeout produces one of three
  outcomes depending on the flags — `cli <v> missing and no --cli-url or --cli-zst
  provided` (no source), a *silent* reinstall (fast replacement; only
  `cliWasPresent:false` moves), or `installed cli at <path> is not runnable` (same
  slow CLI supplied). On `-cli-zst` the blob is consumed whenever decompression
  succeeds.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-cli-probe-timeout
  <dur>` or the key; disabled bypasses `context.WithTimeout`.
- **A bound is not a hang detector.** Measured 2026-08-07: a CLI answering honestly
  in 20 s makes an opted-in claustrum fail the install and delete the staged binary
  (and consume the blob), where the reference installs it and returns at 20 s; the
  reference also installed a 90 s CLI, waiting 91 s. So the reference has no deadline
  at or below 90 s and any finite bound diverges for some honest input — picking
  30 s or 60 s only moves the boundary. Above 90 s is unmeasured on both.
- **Why opt-in.** Cleared clause (a)'s not-a-frame half (the reference's wait is
  apparently unbounded) but an honest-but-slow CLI pays and, with Desktop owning the
  argv, cannot decline (rule 4). The flip was verified against the reference, not
  just argued.
- **Reopen trigger** (a retraction rider, not the flip): Desktop turning out not to
  parse `cliError` after all — see
  [ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance). Whether any
  client reads `cliWasPresent` is unprobed either way.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md); `install.go`. Probe-site table, 20 s /
  90 s table, sweep/prune, zero-parsing edges: forensics.

### D12 · Make the `-install` CLI download bound opt-in { #d12 }

- **Behavior.** The download ran with `http.Client{Timeout: 5m}`; now
  `Timeout: cliDownloadTimeout`, defaulting to `0`. It bounds the whole exchange,
  including the body read, so a real body over a link that needs six minutes trips
  it exactly as a black hole does.
- **Default.** `0` = no bound (byte-identical) — `http.Client{Timeout: 0}` is the
  stdlib's own "no timeout" sentinel. **Activate:** `-cli-download-timeout <dur>` or
  the key.
- **Zero frees the body read, not every clock.** `fetchToFile` leaves `Transport`
  nil, so `http.DefaultTransport` still applies `net.Dialer{Timeout: 30s}` and
  `TLSHandshakeTimeout: 10s` on `-cli-url` — a SYN-black-holed host fails at 30 s
  with the bound off. Both are always-on stdlib defaults, unnumbered and unprobed on
  the reference.
- **Why opt-in.** The reference showed no bound at or below 400 s on a stalled body.
  Measured on a valid zstd blob dribbled over ~324 s, the reference and
  claustrum-at-default both install it while claustrum at the retracted 5 m fails at
  300 s — an honest slow download pays, and Desktop owns the argv (rule 4).
- **Reopen trigger.** An operator with the bound set reporting an honest slow
  download failed by it.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md); `install.go` (`fetchToFile`). Straddle
  and stall tables, confounders: forensics.

### D13 · `-install` verifies the checksum before decompressing (always-on, UNRESOLVED) { #d13 }

!!! warning "This is the one entry that does not currently clear rule 3."
    It is recorded rather than explained away. It stays in the code, labelled,
    rather than justified by a rule bent around it.

- **Behavior.** On the `-cli-url` path the reference decompresses first and aborts
  on the first invalid bytes; claustrum hashes the response as it streams to disk,
  verifies the checksum, then decompresses. On a blob that is both undecompressable
  and wrong-checksummed the reference says `decompressing: unexpected EOF` where
  claustrum says `checksum mismatch`. A *genuine mid-transfer interruption* never
  reaches the checksum on claustrum (`io.Copy`'s error returns first), so it
  diverges on the prefix instead — `download failed: <err>` vs the reference's
  `decompressing: <err>`.
- **Why it is unresolved.** Clause (c) was written for it and, measured, it does not
  meet it. On both honest-path rows the reference **creates an empty cli-dir** where
  claustrum **creates nothing** (when the cli-dir did not already exist), so the
  delta is not confined to diagnostic text — an empty directory is state a caller
  keeps, distinguishable with a `files.stat`. The reopen fixture run 2026-08-08 did
  not meet its condition: the on-disk delta is conditional on the cli-dir being
  absent, and it does not self-heal.
- **The trigger is reachable** (not "an input no honest caller produces", which was
  measured wrong): a bad mirror, a partial upload, or a stale short proxy object is
  undecompressable *and* checksum-mismatched with no adversary. This is *not* the
  generic "flaky network" case — a genuine interruption is the prefix-divergence row
  above.
- **Why still always-on despite being unresolved.** The delta stays cheap because
  neither string is disk-full-shaped, so Desktop (per the `cliError` driver claim)
  classifies both the same way and retries rather than failing terminally. That is a
  claim about a third binary the parity harness cannot settle
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)); if it is
  ever falsified, the cheapness argument fails and this entry owes an opt-in flip.
- **Reopen trigger.** Any change to how Desktop classifies `cliError` — e.g.
  distinguishing `checksum mismatch` from a decompression error, or matching either
  as terminal.
- **Not the same as D1.** D1 is about *whether* the local `-cli-zst` blob is
  verified at all; D13 is about the *order* of verify and decompress on the
  `-cli-url` download.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Staging and cleanup; `install.go`.
  Ordering, clause-(c), and reopen-fixture tables: forensics.

### D14 · Make the `-install` libc probe deadline opt-in (linux) { #d14 }

- **Behavior.** `detectLibcWith` returns `musl` from the loader glob
  (`/lib/ld-musl-*.so.*`) *before* spawning `ldd`; only when the glob misses does it
  run `ldd --version` (the deadline applies there). `libc_other.go` returns `""`
  without probing off linux — so the bound cannot fire on a host whose loader the
  glob matches. The predicate is the glob, not the host.
- **Default.** `0` = no deadline (byte-identical). **Activate:** `-libc-probe-timeout
  <dur>` or the key; disabled bypasses `context.WithTimeout` (`lddCtx`). **Not the
  same knob as `-cli-probe-timeout`** (D11), which it is one letter apart from — that
  one bounds `<cli> --version`, this one bounds `ldd --version`;
  `TestInstallArmWiresEachFlagToItsOwnGlobal` exists because a swap compiles and
  passes every isolated test.
- **The value delta is narrow but not cosmetic.** Fallback and true value coincide
  except where the glob misses **and** `ldd` reports musl **and** exits 0 (a
  faithful musl `ldd` exits 1) **and** is slower than the deadline. Per the driver
  claim that Desktop uses `libc` to choose a CLI build
  ([ARCHITECTURE.md](ARCHITECTURE.md#driver-claims-and-their-provenance)), that
  means fetching a glibc build for a musl host.
- **Softer than it reads.** It fires in only one of two stall shapes — a stalled
  `ldd` leaving a surviving child keeps claustrum blocked past the deadline (the
  same softness as D5). It also addresses the stall half only: a hostile `ldd`
  resolved earlier in `PATH` that answers in 1 s is untouched, and `classifyLibc`
  then trusts its `musl` banner verbatim.
- **Why opt-in.** Cleared clause (a)'s not-a-frame half (the reference gave no reply
  at 45 s in the discriminating shape) but the honest-path cost was untested in
  either direction, and there was no escape hatch — an untested conjunction is not a
  justification (rule 4).
- **Reopen trigger.** A musl host whose loader the glob misses **and** whose `ldd`
  exits 0 with a musl banner, reported together with a slow `ldd` (all four
  conjuncts); or any measurement showing the reference bounds this probe above 45 s.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → `-install`; `install.go` (canonical
  stall table), `libc_linux.go`, `libc_other.go`. Full measurement: forensics.

### CT-1 · Opt-in `wantPid` (pid + startTime) on spawn/reattach { #ct-1 }

- `process.spawn` / `process.reattach` accept an optional `"wantPid":true`; the
  reply then gains `pid` (the child's OS pid) and `startTime`. This is the first
  wire-surface *extension* (vs D1, which changes an install-path behaviour).
- **Default path is byte-identical:** absent/false, both fields are omitted
  (`omitempty`) and the frame is exactly the old `{"success":true}` /
  `{found,running,firstSeq,lastSeq}`. The fields live on a dedicated `spawnResult`
  struct, so they can never leak into the `successResult` shared by
  `process.stdin` / `process.kill`.
- `startTime` is an **opaque daemon token**: the daemon's epoch-seconds wall clock
  captured at spawn, returned identically on spawn and reattach for the same id — for
  detecting PID reuse / orphans, **not** OS-comparable (do not equality-check it
  against psutil `create_time`).
- Tolerant both directions (an older daemon ignores the param; an older client never
  sees the fields), so a client may send `wantPid` unconditionally. Contract fixed
  by the sibling clauster client.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`process.spawn` + `process.reattach`);
  `results.go`.

### CT-2 · Opt-in `-keep-children` serve flag { #ct-2 }

- A `-serve` flag (off the wire — no method/frame/capability change). Off by
  default, graceful shutdown kills the whole child tree. **Set**, it leaves spawned
  children running so they survive a daemon restart/upgrade, logging one line with
  the surviving count; the new daemon does not re-adopt them (an out-of-band
  consumer reconciles via the CT-1 `pid`/`startTime`).
- **Caveat: survivors lose their stdio.** The daemon-side pipe ends die with the
  daemon (child sees EOF on stdin; a later write gets SIGPIPE/EPIPE), so only
  children that tolerate dead stdio genuinely survive.
- **POSIX-only.** On Windows children are confined to a Job Object the OS terminates
  on daemon exit regardless, so the flag is ignored with a startup warning
  (`honorKeepChildren`).
- **Activate:** `-keep-children` or the `keep-children` key.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`-serve` flags).

### CT-3 · Opt-in `claustrum.conf` config file { #ct-3 }

- **A single place to turn deviations on; absent ⇒ stock.** An optional
  `key = value` file read from the directory holding the binary. If it is missing,
  unreadable, non-regular, or malformed, claustrum behaves as a stock replica —
  every key gates an already-opt-in divergence. Zero new dependency (stdlib `bufio`
  + `strings.Cut`, `#` comments); unknown keys and invalid values are ignored
  (forward-compatible, fail-safe). Precedence: **explicit CLI flag > config >
  default**.
- **Keys mirror the flags:** `version-override`, `keep-children`, `metrics-addr`,
  `listen-pipe`, `max-extract-bytes` (D3), `max-cli-bytes` (D10), `cli-probe-timeout`
  (D11), `cli-download-timeout` (D12), `libc-probe-timeout` (D14), `git-timeout`
  (D5), `files-read-regular-only` (D4). Durations use `time.ParseDuration` (a bare
  number is rejected, except zero, which parses in unboundedly many spellings and
  always means disabled); **no accepted oddity can switch a divergence *on***.
- **`version-override` makes claustrum a permanent drop-in.** The desktop client
  decides whether to re-upload the daemon by running `<bin> --version` on the cached
  `~/.claude/remote/srv/<pinned-sha>/server` and matching the SHA it pins. Stock
  claustrum prints its own version, so the client re-SFTPs the reference every
  session. Set to that **bare commit SHA** (40-hex git SHA-1; 64-hex also accepted;
  anything else is a no-op), claustrum prints `claude-ssh <sha> (via Claustrum …)`,
  the client hits the cache, and stops overwriting.
- **Off the wire, off by default.** `-version` is CLI stdout, not a JSON-RPC frame;
  `server.version` / `server.capabilities` still report claustrum's own version.
- **Fail-safe & hardened:** regular-file-only via `Lstat`, `io.LimitReader` ≤ 64 KiB,
  per-key validation (`version-override` gated to
  `^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$` and lower-cased), values used as data never
  as a format string. Any doubt → stock; startup never fails.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`-version`); `config.go`.

### CT-4 · Opt-in hardened token persistence — deferred idea { #ct-4 }

- **Context.** The daemon persists its token to `daemon.token` (`0600`) beside the
  socket for client reconnect — parity, on by default (`tokenpersist.go`). Two
  accepted parity caveats: the file survives an unclean kill/crash (cleanup runs
  only on graceful shutdown), and on Windows `0600` is not an owner-only DACL.
- **Idea (not built).** A `claustrum.conf` key — e.g. `persist-token = false` — and/or
  a Windows owner-only DACL on the file, for operators who prefer a smaller on-disk
  token window over drop-in reconnect. Must stay **absent ⇒ stock**.
- **Why deferred.** No demand; the default matches the reference and the socket
  directory is already owner-scoped in the real deployment. Recorded so the security
  trade-off isn't lost.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) → Token persistence; `tokenpersist.go`.

### CT-5 · Opt-in `-listen-pipe` Windows named-pipe transport { #ct-5 }

- **Shipped.** `-listen-pipe` makes `-serve` *additionally* serve the exact same
  NDJSON JSON-RPC dispatch over a Windows named pipe, concurrently with the
  `AF_UNIX` socket. Off ⇒ stock; the wire contract, field ordering, and framing are
  unchanged whether a request arrives over the socket or the pipe.
- **Why.** A Windows client that cannot consume an `AF_UNIX` socket — notably Python
  `asyncio`, whose Windows Proactor loop natively supports named pipes — otherwise
  can't attach (clauster's ask).
- **Discovery + auth.** claustrum picks the pipe name
  (`\\.\pipe\claustrum-<instance-id>`, client-opaque) and publishes it to `rpc.pipe`
  beside the socket (atomic write before accepting; removed on graceful shutdown).
  Same in-band `"auth"` + `daemon.token` handshake. Owner-only DACL (SDDL
  `D:P(A;;GA;;;<current-user-SID>)`), local-only, no new authenticated surface
  ([SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md)).
- **Windows-only:** ignored with a warning elsewhere (`honorListenPipe`); setup
  failure non-fatal (the socket still serves).
- **Activate:** `-listen-pipe` or the `listen-pipe` key.
- **Pointers.** [PROTOCOL.md](PROTOCOL.md) (`-serve` flags); `pipetransport.go`,
  `pipetransport_windows.go`.

## Candidates considered but not taken

Both are places where claustrum was friendlier than the reference and matching it
would cost something real. Recorded so the code can point somewhere durable — **no
decision is implied**, and nothing here is shipped or scheduled.

- **Conditional `-stop` socket unlink.** `-stop` removes the socket path on every
  exit, including when no daemon answered — matching the reference (measured on three
  arms, two of them attributing: the live-daemon control says nothing, because the
  daemon removes the socket itself). That means removing a path it did not create
  and cannot identify the owner
  of; `os.Remove` does not distinguish shapes, so a regular file or empty directory
  at the `-socket` path goes the same way. A `stat`-first variant that removed only a
  socket would be strictly safer **and a divergence**.
- **Fail fast on a missing `-serve` token source.** The check runs in the detached
  child, so the launcher reports its ~10 s accept timeout and the real reason reaches
  only the child's log — reference parity (measured 10.02 s vs 10.07 s). A parent-side
  check answered in 0.03 s and named the actual problem: better operator experience,
  and a divergence.

## Explicitly out of scope (would break compatibility)

- Changing method names, params, result field order, error codes, or the
  stream-frame shape.
- Replacing the in-band `"auth"` scheme.
- Adding **required** new params to existing methods. *(An **optional**,
  gracefully-ignored param whose result fields vanish by default — the D1 / CT-1
  pattern — is the sanctioned exception: it leaves the default frame byte-identical
  and degrades both ways.)*

Any of these would need a deliberate, documented protocol version bump.
