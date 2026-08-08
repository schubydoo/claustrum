# Staying in lock-step with the reference daemon

claustrum is behaviorally compatible with a reference daemon that ships inside
Claude Desktop's SSH-remote feature. That daemon is versioned by **git SHA** and
distributed as per-platform zstd blobs on a public CDN. This doc is how we detect
when a new build appears and whether it changed anything we need to match. For the
running history of which builds changed what, see the
[reference build ledger](REFERENCE-BUILDS.md).

## How the reference is distributed

- Per-version manifest:
  `https://downloads.claude.ai/claude-ssh-releases/<sha>/manifest.json`
  → `{"version":"<sha>","platforms":{"<goos>-<goarch>":{"checksum":"<sha256 of .zst>","size":N}}}`
- Per-platform artifact:
  `https://downloads.claude.ai/claude-ssh-releases/<sha>/<goos>-<goarch>/claude-ssh.zst`
- Six targets: `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`,
  `windows-amd64`, `windows-arm64`. (GOARCH naming — `amd64`, not `x64`.)

There is **no "latest" index** — releases are keyed by SHA, so step 1 is always
"find the new SHA."

## Step 1 — find a candidate SHA

The reference build's SHA is whatever Claude Desktop currently deploys. Sources,
easiest first:

1. **A host Desktop has connected to** — the daemon is cached per-SHA:
   ```sh
   ls -1d ~/.claude/remote/srv/*/server | sed -E 's#.*/srv/([0-9a-f]+)/server#\1#'
   ```
2. **The Desktop app bundle** — the pinned SHA (and all six per-platform
   checksums/sizes) is a **build-time constant baked into the app**, readable
   offline without connecting anywhere. In the Linux `.deb` it is a
   `JSON.parse('{"version":"<sha>","manifest":{…},"baseUrl":".../claude-ssh-releases"}')`
   literal inside `resources/app.asar` (a minified `.vite/build/index.chunk-*.js`,
   so the chunk name and wrapper function are per-build-random); a parallel
   literal pins the CLI (`claude-code-releases`; since **1.24012.9** its
   `baseUrl` carries a channel suffix — `claude-code-releases/rc/<sha>` — so the
   extractor matches the bucket by path *segment*, not `endswith`). So a new Desktop build is
   itself the "new SHA" signal. **[`scripts/extract-desktop-pin.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/extract-desktop-pin.py)**
   reads it straight out of a `.deb` (stdlib-only: `ar` → `data.tar.xz` →
   `app.asar` → enclosure brace-match, no `dpkg`/`asar` needed), and
   **[`scripts/latest-desktop-sha.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/latest-desktop-sha.py)** does the
   full "find the newest Desktop → download → extract → compare to
   `UPSTREAM_SHA`" loop. Observed: Linux **1.18286.0** (2026-07-02) pinned
   `7c2f88d…`; **1.20186.1 → 1.24012.9** pin `5db5e4a…` (the current baseline).
3. **The Desktop machine's cache** — per-platform binaries under
   `<app-data>/claude-ssh-remote/<sha>/` (`%APPDATA%/Claude/…` on Windows),
   alongside a `.verified-<goos>-<goarch>` marker.
4. **Probe a guess** — `manifest.json` returns 200 for a real SHA, 404 otherwise.

> Note: the *CLI* release manifest
> (`claude-code-releases/<ver>/manifest.json`) has a `commit` field, but that is
> the **CLI's** commit, not the daemon's — don't confuse them.

> **How the client picks the deployed SHA (and a claustrum divergence).** Before
> a session the client runs `server --version` on the cached
> `~/.claude/remote/srv/<pinned-sha>/server` and matches `/claude-ssh\s+(\S+)/`;
> it skips re-upload only when that token equals the pinned SHA. The reference
> prints `claude-ssh <sha> (built …)`, so it hits the cache. claustrum prints
> `claustrum <ver> (built …)` — its **own** identity and **own** version — so the
> token never matches the client's pinned SHA and the daemon is re-SFTP'd
> (idempotently, ~2.3 MB) **every session**. This is a consequence of the
> `claude-ssh:`→`claustrum:` rebrand: it is CLI stdout, **not** a JSON-RPC frame,
> so the wire contract is untouched and the redeploy is harmless — the daemon
> that ends up running is still claustrum.
>
> To make claustrum a **permanent drop-in** (client sees it as up-to-date and
> stops overwriting it), `-version` need only emit `claude-ssh <pinned-sha>` as
> its **first token** — the client captures just that token, so a
> `(via Claustrum <ver>)` suffix is fine — with the binary placed at
> `~/.claude/remote/srv/<pinned-sha>/server`. The SHA is per-Desktop-build, so a
> drop-in build must track it (source it from `scripts/UPSTREAM_SHA`) and move to
> the new `srv/<sha>/` path when Desktop bumps. This is off by default (claustrum
> keeps its own identity); it would be an opt-in build stamp, not a wire change.

## Step 2 — run the drift check

```sh
scripts/check-upstream.sh <sha>
# or, with the pinned baseline SHA in scripts/UPSTREAM_SHA:
scripts/check-upstream.sh
```

The script (network access required) will:

1. Compare `<sha>` to the pinned baseline in `scripts/UPSTREAM_SHA`; a difference
   is itself the "new release" signal.
2. Fetch the manifest, download the `linux-amd64` `claude-ssh.zst`, **verify it
   against the manifest checksum**, and decompress it.
3. Build claustrum, then diff the two binaries on the things we must match:
   - **method names** (`server.*`/`files.*`/`git.*`/`process.*` literals),
   - **CLI flags** (`-help` output),
   - **`-version` format**,
   - the **app-facing string set** (errors, `[Server]`/`[process.Manager]`/
     `[frameSink]`/`[shellenv]` log lines, flag help).
4. Print `PASS` (no drift in the checked surface) or `DRIFT` with specifics, and
   exit non-zero on drift.

This static check needs no running daemon and is safe to run anywhere with
network access.

## Step 3 — authoritative byte-for-byte recheck (local only)

The static check catches added/removed methods, flags, and strings. To confirm
**frame-level** byte-identity (result field order, stream framing, error bodies,
`reattach` semantics), run the private validation battery (kept in `scratch/`,
not published) against both the new reference and claustrum:

```sh
# starts each binary as a PRIVATE -serve on a throwaway /tmp socket, runs the
# full method battery, and diffs normalized frames. Never touches a live daemon.
scratch/probe/validate.sh <path-to-reference> > /tmp/ref.json
scratch/probe/validate.sh ./claustrum          > /tmp/mine.json
diff /tmp/ref.json /tmp/mine.json
```

> Safety: only ever probe a **private** instance on a `/tmp` socket with its own
> `-token-file`; never point the harness at a live daemon's socket, and clean up
> every probe.

## Step 3b — real-session capture (highest fidelity, optional)

Steps 2–3 drive the daemon with **synthetic** requests we author. The ultimate
check is the **real** desktop client's traffic.

- The bridge (`server --bridge`) is a dumb stdio relay, so teeing its
  stdin/stdout captures the exact client↔daemon NDJSON of a live session —
  which can then be replayed against claustrum and diffed.
- Tooling lives in `scratch/capture/` (gitignored): a `capture-bridge`,
  `replay.js`, and `REPLAY.md` with the capture runbook.
- `replay.js` diffs order-insensitively — responses keyed by `id`, stream
  frames by `processId`+`seq` — and masks the version SHA, the token, and
  (with `--mask-data`) the nondeterministic agent payloads.
- The session is captured under a **throwaway** SSH user via a `ForceCommand`
  wrapper scoped to that user, so it never touches a live daemon.

This was exercised against the then-pinned reference (`8de85faaa…`, since superseded by `7cbfa471`): a real Desktop
session — 10 of the 18 methods, the full `process.*` lifecycle including a
>32 KiB output stream and a mid-stream disconnect/reconnect that drove
`process.reattach` — verified **byte-identical**. Concretely, on real client
framing:

- the live client uses only a subset of the 18 methods (no hidden method), and
  every param shape it sends is one claustrum already accepts (incl.
  `process.spawn` with and without `cwd`/`env`);
- `server.capabilities` matches exactly (version-masked), including the 18-method
  order;
- the stream envelope is `{type,processId,stream,seq,data}` (and `…,exitCode` on
  exit) in that field order, with non-zero exit codes propagated verbatim;
- `process.reattach` is per-process with `fromSeq`; on reconnect the daemon
  replays buffered frames with `seq > fromSeq` and the sequence stays contiguous
  across the reconnect.

Use this as a periodic spot-check or when a new build changes `process.*`
behavior the synthetic battery can't fully model (real reconnect timing,
multi-process reattach). The raw dumps contain the session token and host paths —
keep them in `scratch/` (gitignored); never commit them.

## Step 4 — reconcile

If the check reports drift:

1. Identify exactly what changed (new method? changed error string? new flag? new
   install fact? changed frame shape?).
2. Implement the change in claustrum behind the existing structure, keeping every
   unchanged behavior unchanged.
3. Re-run the static check **and** the byte-for-byte battery until both are clean.
4. Update `scripts/UPSTREAM_SHA` to the new SHA, note the change in `CHANGELOG.md`,
   and update `docs/PROTOCOL.md` if the wire surface changed.

> Not every claustrum behavior is meant to match the reference. A set of
> **deliberate divergences** is catalogued in
> [`IMPROVEMENTS.md`](IMPROVEMENTS.md#deliberate-divergences-post-parity) — don't
> "reconcile" any of them away as drift. They split two ways, and the split is
> what decides whether a probe can see them. (A third bucket — "always-on,
> reference behavior unknown" — held only D8 and was emptied when D8 was measured
> on 2026-08-06. Re-add it if another entry ever needs it: an unmeasured
> divergence must not sit silently among measured ones.)
>
> **Off the default path.** The JSON-RPC battery (`validate.sh` /
> `battery.js`) never exercises these, so they won't show as a diff there:
> - **D1** — `-cli-zst` SHA-256 verification when a `-cli-checksum` is supplied.
>   ⚠️ **D1 is caller-activated, NOT operator-declinable** — it fires whenever the
>   caller supplies `-cli-checksum`, and on `-install` that caller is Desktop. Do not
>   rule it out as "nobody opted in"; see IMPROVEMENTS D1.
>   ⚠️ Unlike the rest of this group, `scratch/probe/cli_probe.sh` **does** drive
>   this path on both binaries — the JSON-RPC battery is not the only instrument.
> - **D3** — the `files.extract_tar` size cap, `-max-extract-bytes` / the
>   `max-extract-bytes` config key. **Off by default (`0` = unlimited), and OFF is
>   the parity position** — the reference applies no cap at any size the probe
>   could reach, so a non-zero default would fail an extraction the reference
>   completes.
> - **D10** — the `-install` CLI size cap, `-max-cli-bytes` / the `max-cli-bytes`
>   config key, governing both the decompressed CLI and the download body. **Off by
>   default**, same reasoning as D3: measured, the reference takes a 600 MiB payload
>   all the way to the runnability check.
> - **D11** — the `-install` runnability-probe deadline, `-cli-probe-timeout` / the
>   `cli-probe-timeout` config key. **Off by default (`0` = no deadline), and OFF is
>   the parity position** — measured, the reference was still running at 45 s
>   against a CLI that never answers and INSTALLED one that answered at 90 s, so
>   any default **at or below 90 s** fails an install the reference completes.
>   (Above 90 s the reference is unmeasured; "no deadline at all" is the practical
>   reading, not a result.) ⚠️ **A probe that
>   sees a D11-shaped difference on a stock claustrum is drift, not D11** — check
>   for a `cli-probe-timeout` key in `claustrum.conf` or a `-cli-probe-timeout`
>   flag first, **and confirm the value actually parses to a positive duration** —
>   a key being *present* does not mean a deadline is *in force*, and the two paths
>   fail differently:
>   - **config key, unparseable** (`cli-probe-timeout = 15`) **or negative**
>     (`= -1s`): dropped **silently** — no log line at all — and the run proceeds
>     with no deadline and a normal `__INSTALL_RESULT__`. This is the shape that
>     looks like drift.
>   - **flag, negative** (`-cli-probe-timeout -1s`): normalises to 0 with an
>     `[Install]` warning on stderr, then runs normally and **does** print a facts
>     line, exit 0. Not the same as the config path — measured both ways.
>   - **flag, unparseable** (`-cli-probe-timeout 15`): `flag.Duration` rejects it
>     before any mode runs, so claustrum prints `invalid value "15" for flag
>     -cli-probe-timeout: parse error` plus usage and **exits 2 with no facts line
>     at all** (measured).
>     ⚠️ **A missing `__INSTALL_RESULT__` does NOT by itself mean a malformed
>     flag.** A stock claustrum blocked on a CLI that never answers also emits no
>     facts line — that is the deadline-off behaviour, and it is the row this very
>     entry documents at 45 s. Discriminate on the exit: **2 plus `parse error` on
>     stderr** is the bad flag; **still running until the harness kills it, nothing
>     on stderr** is the parity wait.
>   - **any zero** (`0`, `+0`, `-0`, `0s`, `-0m`, and a negative that truncates to
>     zero such as `-0.4ns`) parses and is *accepted*, so it reads as opted-in while
>     leaving the deadline off. `0s` is the spelling an operator most often writes
>     meaning "disabled".
>
>   Once genuinely opted in, the three surfaces below apply — but note the same
>   strings are also produced by a genuinely broken or absent CLI on both binaries,
>   so a surface alone does not identify a timeout.
> - **D12** — the `-install` CLI download bound, `-cli-download-timeout` / the
>   `cli-download-timeout` config key. **Off by default (`0` = no bound), and OFF is
>   the parity position** — measured, the reference was still downloading at 400 s
>   against a body that never arrives, and it completes a slowly-dribbled one that
>   takes **324 s**, exactly as claustrum now does — while claustrum with the
>   retracted 5-minute value fails that same download at 300 s. That straddling run
>   is the measurement; a 14 s dribble used earlier proves nothing, because the old
>   always-on build would have installed it too. (The 324 s row is measured on both
>   binaries; claustrum at the new default was **not** re-run against the 400 s
>   never-arrives body.) ⚠️ **A
>   D12-shaped difference on a stock claustrum is drift, not D12** — check for the
>   key or flag first, and confirm the value parses to a positive duration: the same
>   config-silent / flag-warns / flag-exits-2 / any-zero-accepted table as D11 above
>   applies verbatim, with `cli-download-timeout` substituted.
>   ⚠️ **A D12 probe needs a VALID zstd body.** With an invalid one the reference
>   returns at 0 s on `decompressing: invalid input: magic number mismatch` — that
>   is D13's ordering answering, not a download bound, and it reads like "no
>   divergence" if you are not watching for it.
>   ⚠️ **Zero frees the body read only.** `http.DefaultTransport` still applies
>   `net.Dialer{Timeout: 30s}` and `TLSHandshakeTimeout: 10s`; a SYN-black-holed
>   host still fails at 30 s at the default. Both are unnumbered and unprobed on the
>   reference.
> - **D5** — the git deadline on every git invocation, `-git-timeout` / the
>   `git-timeout` config key. **Off by default (`0` = no deadline), and OFF is the
>   parity position** — measured on `git.worktree_remove`, the reference gave no
>   reply at 75 s where claustrum answered at 60.1 s under the retracted cap, with a
>   fast-git control proving the fixture could answer. Above 75 s is unmeasured on
>   both binaries, and an honest 61 s git has never been run against either.
>   ⚠️ **A D5-shaped difference on a stock claustrum is drift, not D5** — check for
>   the key or flag first, and confirm the value parses to a positive duration: the
>   same config-silent / flag-warns / flag-exits-2 / any-zero-accepted table as D11
>   above applies verbatim, with `git-timeout` substituted.
>   ⚠️ **Two ways to probe an opted-in D5 and see nothing:** a harness deadline
>   under the configured value records "no reply" for both binaries, and a git that
>   spawns a surviving child blocks claustrum past its own cap too — `CombinedOutput`
>   waits on the output pipe, not on git's exit.
>   ⚠️ **One arm is invisible to a client**: a killed `git ls-files` makes
>   `git.worktree_create` answer `{"success":true}` with every `.worktreeinclude`
>   file absent. Nothing on the wire says so, so a "no divergence" verdict from the
>   frame battery does not cover it.
> - **D4** — the `files.read` regular-file guard, `-files-read-regular-only` / the
>   `files-read-regular-only` config key. **Off by default, and OFF is the parity
>   position** — measured against `5db5e4a`, the reference reads `/dev/null` as
>   `{"content":"","exists":true}` and blocks on a writerless FIFO until one opens,
>   refusing neither, so a guard on by default fails a read the reference completes.
>   ⚠️ **D4 is the exception to this group's heading, since 2026-08-08.** The battery
>   DOES now drive it — `battery.js` id 70 reads `/dev/null`, added because all six
>   of its other `files.read` cases use regular files, a directory or a missing path,
>   so nothing standing could see this divergence. It shows no diff at the default
>   because the guard is **off**, not because the path is unexercised, and it turns
>   red the moment the guard is armed (verified with a `claustrum.conf` beside the
>   binary). The other five shapes cannot go in a battery — a writerless FIFO parks
>   the run, `/dev/zero` OOMs the daemon, and the socket and device rows are uid- and
>   platform-dependent — so those stay in the one-off session.
>   ⚠️ **A D4-shaped difference on a stock claustrum is drift, not D4** — check for
>   the key or flag first. The parse table differs from the four durations
>   (D5, D11, D12, D14) because this key is a **bool**: an unrecognised config value
>   (`files-read-regular-only = maybe`) is dropped **silently**, leaving the key
>   unset so the flag value stands — off, unless a flag was also passed — while an
>   unrecognised *flag* value (`-files-read-regular-only=maybe`) is rejected by
>   `flag.Bool` before any mode runs (usage plus exit 2, measured).
>   ⚠️ **There IS an "accepted but inert" spelling here, exactly as `0s` is for the
>   durations** — `files-read-regular-only = false` and `-files-read-regular-only=false`
>   are both accepted and both contain the name a triager greps for. The flag form is
>   inert unconditionally; the **config** form is inert only when no flag was passed,
>   since the flag wins. Presence of the key is not evidence the guard is armed, and
>   absence of a `true` is not evidence it is off — read both, and read the flag first.
>   ⚠️ **One spelling ARMS it, but look for the right symptom**:
>   `-files-read-regular-only maybe` (a space, not `=`). Go's `flag` never lets a
>   bool consume the next argument, so the guard goes **on** and `maybe` becomes a
>   positional — where the same typo on any of the four durations exits 2. But
>   `flag` also **stops parsing at the first non-flag argument**, so every flag AFTER
>   the typo is silently dropped (measured: `-files-read-regular-only maybe -version`
>   exits 2 with `one of --version/--install/--serve/--bridge/--stop is required`).
>   So the guard is actually armed only if `-serve` and the socket/token flags were
>   already parsed *before* the typo; otherwise the operator sees "the daemon will
>   not start" or "wrong socket", **not** a D4-shaped frame. Do not go looking for a
>   `-32602` on this one unless the argv order puts the typo last.
>   ⚠️ **Probing the off state on a FIFO needs a writer.** With the guard off
>   claustrum blocks in `open` exactly as the reference does, so a harness that
>   never opens a writer records "no reply" for **both** binaries — the
>   non-discriminating shape that produced this file's retracted "never replies"
>   claim in the first place. `/dev/null` is the discriminating input: both binaries
>   answer immediately, and only a guarded claustrum answers `-32602`.
> - **D14** — **linux only** — the `ldd --version` libc probe deadline,
>   `-libc-probe-timeout` / the `libc-probe-timeout` config key. **Off by default
>   (`0` = no deadline), and OFF is the parity position** — measured, the reference
>   showed no deadline at or below 45 s against a stalled `ldd`. ⚠️ **Do not confuse
>   the key with `cli-probe-timeout`** (D11): that one bounds `<cli> --version` on
>   every platform, this one bounds `ldd --version` on linux only. They are one
>   letter apart and the same type.
>   ⚠️ **A D14-shaped difference on a stock claustrum is drift, not D14** — check for
>   the key or flag first, and confirm the value parses to a positive duration: the
>   same config-silent / flag-warns / flag-exits-2 / any-zero-accepted table as D11
>   above applies verbatim, with `libc-probe-timeout` substituted.
>   Once genuinely opted in, two symptoms, not one: a `libc` difference on linux MAY
>   be this (though the fallback usually matches the true value, so rule out the
>   loader-glob path first — and note `libc` selects which CLI build Desktop
>   downloads, so this symptom is not cosmetic; that last part is a **driver** claim
>   the parity harness cannot settle, see ARCHITECTURE → Driver claims and their provenance), **and — more likely —
>   "the reference's `-install` never returned while claustrum's did"**.
>   ⚠️ **Even opted in, the bound fires in only one of the two stall shapes:** if the
>   stalled `ldd` leaves a child holding its output pipe, NEITHER binary replies — so
>   "claustrum returned and the reference did not" is **consistent with** D14 (confirm
>   with the `libc` field and whether the loader glob matches), while "neither
>   returned" does not rule it out. (The 45 s reference result comes from the
>   discriminating shape only; the surviving-child arm cannot support it, since
>   claustrum had a deadline and looked identical there.)
>   ⚠️ **Off linux there is no probe at all**, so a `libc` difference there is NOT
>   this — and on a host whose `/lib/ld-musl-*.so.*` glob matches, `ldd` is never
>   spawned, so the deadline cannot fire however it is set.
> - **CT-1** — `wantPid` adds `pid`/`startTime` to spawn/reattach replies.
> - **CT-2** — `-keep-children` leaves children running across shutdown.
> - **CT-3** — the `claustrum.conf` file (`version-override` / `keep-children` /
>   `metrics-addr` / `listen-pipe` / `max-extract-bytes` / `max-cli-bytes` /
>   `cli-probe-timeout` / `cli-download-timeout` / `libc-probe-timeout` / `git-timeout` /
>   `files-read-regular-only`).
> - **CT-5** — `-listen-pipe`, the additional Windows named-pipe transport.
>
> **Always-on and measured — a probe that reaches the path *may* see a real
> difference**, and that is expected, not drift.
> - **D2** — a destructive path target that is or contains the home directory is
>   refused (`files.extract_tar` `destDir`, `git.worktree_remove` `worktreePath`).
> - **D6 / D7** — `-cli-version` must be a single path component, and must not
>   collide with the install temp sweep.
> - **D8** — `remote-server.log` is declined rather than shared with another user.
>   Measured 2026-08-06: in a sticky directory holding a root-owned world-writable
>   log, the reference truncates it and writes its own output in; claustrum leaves
>   it alone and falls back to inherited stdio.
> - **D9** — namespace-wide params binding rejects a type-mismatched field the
>   reference ignores. The trigger is a **type error** in a namespace field the
>   target method does not read; a correctly typed extra field is ignored by both.
>   ⚠️ "No real client sends that" is an assertion, not a measurement — Desktop's
>   per-method param set has never been enumerated against this binding.
> - **D14 is no longer in this group either** — the `ldd` probe deadline moved to the
>   off-the-default-path list above when it was flipped on 2026-08-08, so on a stock
>   claustrum the libc probe has no deadline at all and a `libc` difference is NOT D14
>   unless the key or flag is set. Look for it above, not here.
> - **Neither D11 nor D12 is in this group any more** — both bounds moved to the
>   off-the-default-path list above and are off by default, so on a stock claustrum the
>   runnability probe has no deadline and the download has no bound. D11 matches the
>   reference on every input measured (still running at 45 s; installing a CLI that
>   answers at 90 s; above 90 s neither binary has been probed); D12 matches on the
>   324 s dribble, with the 400 s never-arrives row measured on the reference only.
>   ⚠️ D11's three surfaces are only reachable once `-cli-probe-timeout` /
>   `cli-probe-timeout` is set, and **one of them is silence**, so a triager who has
>   confirmed the key IS set should not look only for an error: `installed cli at
>   <path> is not runnable` (staged binary deleted, blob consumed on `-cli-zst`),
>   `cli <v> missing and no --cli-url or --cli-zst provided` with the working CLI
>   still on disk, or **no `cliError` at all** — where `cliWasPresent:false` is the
>   only FACTS field that moves, but the cli-dir moves too: every cached shape runs
>   the orphan sweep and the silent one runs the prune, where the reference
>   cache-hits and touches nothing. All three surfaces measured 2026-08-07 against
>   the then-hardcoded 15 s; the sweep/prune contrast is derived. (On the no-cache
>   shape the reference installs the CLI outright, measured; on the cached shapes it
>   should simply cache-hit with `cliWasPresent:true` and install nothing — derived,
>   not separately measured.)
> - **D13** — `-install` verifies the download's checksum BEFORE decompressing,
>   where the reference decompresses first. `IMPROVEMENTS.md` used to call the
>   trigger unreachable by honest callers — it is not, and that is retracted there.
>   ⚠️ **D13's always-on status is UNRESOLVED** — the two measured rows differ on disk
>   too (the reference creates an empty cli-dir, claustrum creates none), so it is
>   listed in IMPROVEMENTS as unresolved rather than justified — and since D14's
>   flip it is the ONLY entry there. D4, D5 and D14 all left that group by being
>   flipped to opt-in, which is the option still open here.
>   ⚠️ **Measured 2026-08-08, so do not re-derive it:** a failing install followed
>   by the retry ends in the **same reply and same on-disk end state** on both
>   binaries whether or not the cli-dir was pre-created, and the on-disk delta is
>   **conditional on the cli-dir being absent** (pre-create it and claustrum's
>   failing install leaves what the reference leaves; it does not self-heal across
>   repeated failures). ⚠️ That last part is measured on the **short-artifact** row
>   and **derived** for the interrupted transfer, which was never run pre-created —
>   so it does not rule an interrupted-transfer report out of D13. That narrows the divergence; it does **not** resolve it, and
>   it is not evidence about Desktop, which is what the reopen condition asks for.
>   ⚠️ Do **not** upgrade this to "the leftover directory is inert" — the *staging
>   location* does depend on the pre-state (PROTOCOL → Staging and cleanup); only
>   the end state is identical.
>   ⚠️ **Two different honest
>   shapes, and a triager must not merge them:**
>   **(1) an origin serving a SHORT or truncated artifact** (bad mirror, partial
>   upload, stale short object) reaches the checksum — reference
>   `decompressing: unexpected EOF`, claustrum `checksum mismatch: …`. A bad-magic
>   blob gives the same claustrum string against the reference's
>   `decompressing: invalid input: magic number mismatch`.
>   **(2) a genuine INTERRUPTED transfer** never reaches the checksum on claustrum —
>   `io.Copy`'s error returns first — so the divergence is in the *prefix*:
>   claustrum `download failed: <transport error>`, reference
>   `decompressing: <transport error>`. Same architectural cause, different
>   observable. "Flaky network ⇒ checksum mismatch" is the wrong generalisation.
>
> **This list covers the D/CT-numbered divergences only.** Several claustrum-only
> behaviors are catalogued by *tier number* instead and are just as real:
> (tier item 5 is now fully numbered — its `gitTimeout` half is **D5**, now opt-in, and its
> `ldd` half is **D14**, both listed above), **item 16** (`-metrics-addr`), **item 17** (the orphaned
> previous process tree is torn down), **item 18** (`-token-fd`), **item 21** (the
> signal is skipped when the child has already exited). Check both indexes before
> concluding that something is drift.
>
> **CT-3 is the one the static check *can* flag:** it diffs `-version` format, so
> a deploy carrying a `claustrum.conf` with `version-override` reports the
> impersonation line — expected, not drift (the no-config default stays
> byte-identical).

## Automating it

- **[`.github/workflows/upstream-desktop-watch.yml`](https://github.com/schubydoo/claustrum/blob/main/.github/workflows/upstream-desktop-watch.yml)**
  runs weekly: it calls `scripts/latest-desktop-sha.py` to discover the SHA the
  *newest* Claude Desktop for Linux pins (Step 1, automated — no out-of-band
  source needed), and compares it to `scripts/UPSTREAM_SHA`. Only when the pin has
  moved does it run `check-upstream.sh <sha>` for the static drift diff and open a
  single idempotent tracking issue; the normal weekly run is just
  download-extract-compare. Reconciliation (Step 4) stays a human decision.
- The watcher currently covers **Linux** only. macOS/Windows Desktop builds pin
  the same per-SHA CDN artifacts, so they're a redundant cross-check rather than a
  new signal; extraction from those bundles is a possible follow-up.
- `check-upstream.sh` can still be run by hand against any SHA (e.g. a
  freshly-discovered one, or to confirm a re-published build hasn't shifted).
