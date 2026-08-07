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
> **Opt-in — off the default path.** The JSON-RPC battery (`validate.sh` /
> `battery.js`) never exercises these, so they won't show as a diff there:
> - **D1** — `-cli-zst` SHA-256 verification when a `-cli-checksum` is supplied.
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
>   any finite default fails an install the reference completes. ⚠️ **A probe that
>   sees a D11-shaped difference on a stock claustrum is drift, not D11** — check
>   for a `cli-probe-timeout` key in `claustrum.conf` or a `-cli-probe-timeout`
>   flag first, **and confirm the value actually parses to a positive duration**:
>   a bare number or a negative leaves the deadline off, silently on the config
>   path and with only a stderr warning on the flag path, so a key being *present*
>   does not mean a deadline is *in force*. Once genuinely opted in, the three
>   surfaces below apply.
> - **CT-1** — `wantPid` adds `pid`/`startTime` to spawn/reattach replies.
> - **CT-2** — `-keep-children` leaves children running across shutdown.
> - **CT-3** — the `claustrum.conf` file (`version-override` / `keep-children` /
>   `metrics-addr` / `listen-pipe` / `max-extract-bytes` / `max-cli-bytes` /
>   `cli-probe-timeout`).
> - **CT-5** — `-listen-pipe`, the additional Windows named-pipe transport.
>
> **Always-on and measured — a probe that reaches the path sees a real
> difference**, and that is expected, not drift:
> - **D2** — a destructive path target that is or contains the home directory is
>   refused (`files.extract_tar` `destDir`, `git.worktree_remove` `worktreePath`).
> - **D4** — `files.read` refuses a non-regular file.
> - **D5** — every git invocation is capped at 60 s. **Two ways to probe this and
>   see nothing:** a harness deadline under 60 s records "no reply" for both
>   binaries, and a git that spawns a surviving child blocks claustrum past its
>   own cap too.
> - **D6 / D7** — `-cli-version` must be a single path component, and must not
>   collide with the install temp sweep.
> - **D8** — `remote-server.log` is declined rather than shared with another user.
>   Measured 2026-08-06: in a sticky directory holding a root-owned world-writable
>   log, the reference truncates it and writes its own output in; claustrum leaves
>   it alone and falls back to inherited stdio.
> - **D9** — namespace-wide params binding rejects a type-mismatched field the
>   reference ignores. Adversarial params only.
> - **D12** — `-install` bounds the download at 5 minutes; the reference was still
>   downloading at 400 s. An honest download merely too slow to finish trips it as
>   surely as a stalled one does, derived from `http.Client.Timeout` bounding the
>   whole exchange rather than measured.
> - **D11 is NOT in this group any more** — it moved to the opt-in list above when
>   its deadline was defaulted off. On a stock claustrum the runnability probe has
>   no deadline and matches the reference. ⚠️ Its three surfaces are only reachable
>   once `-cli-probe-timeout` / `cli-probe-timeout` is set, and **one of them is
>   silence**, so a triager who has confirmed the key IS set should not look only
>   for an error: `installed cli at <path> is not runnable` (staged binary deleted,
>   blob consumed on `-cli-zst`), `cli <v> missing and no --cli-url or --cli-zst
>   provided` with the working CLI still on disk, or **no `cliError` at all** —
>   where `cliWasPresent:false` is the only FACTS field that moves, but the cli-dir
>   moves too: every cached shape runs the orphan sweep and the silent one runs the
>   prune, where the reference cache-hits and touches nothing. All three surfaces
>   measured 2026-08-07 against the then-hardcoded 15 s; the sweep/prune contrast is
>   derived. (On the no-cache shape the reference installs the CLI outright,
>   measured; on the cached shapes it should simply cache-hit with
>   `cliWasPresent:true` and install nothing — derived, not separately measured.)
> - **D13** — `-install` verifies the download's checksum BEFORE decompressing,
>   where the reference decompresses first. Of the three combinations measured, one
>   tells them apart: a blob that is both corrupt zstd and wrong-checksummed.
>
> **This list covers the D/CT-numbered divergences only.** Several claustrum-only
> behaviors are catalogued by *tier number* instead and are just as real:
> **item 5** (**linux only** — the `ldd --version` libc probe is capped at 5 s.
> Two symptoms, not one: a `libc` difference on linux MAY be this (though the
> fallback usually matches the true value, so rule out the loader-glob path
> first), **and — more likely — "the reference's `-install` never returned while
> claustrum's did"**, because a stalled `ldd` caps at 5 s here and is assumed to
> block there — **not measured on either binary**, unlike D11's 45 s/90 s and
> D12's 400 s runs (other rows in those entries are derived too).
> Check this before concluding D12 on an `ldd`-slow host — and before concluding
> D11 at all, which on a stock claustrum is not a live suspect, since its deadline
> is off by default. Off linux there
> is no probe at all, so a `libc` difference there is NOT this. The `gitTimeout` half of that
> tier item is **D5** and is listed above), **item 16** (`-metrics-addr`), **item 17** (the orphaned
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
