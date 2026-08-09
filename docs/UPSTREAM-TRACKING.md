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
   so the chunk name and wrapper function are per-build-random). A parallel literal
   pins the CLI (`claude-code-releases`); since **1.24012.9** its `baseUrl` carries
   a channel suffix — `claude-code-releases/rc/<sha>` — so the extractor matches the
   bucket by path *segment*, not `endswith`. A new Desktop build is itself the "new
   SHA" signal. Two scripts read it:
   - **[`scripts/extract-desktop-pin.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/extract-desktop-pin.py)**
     reads it straight out of a `.deb` (stdlib-only: `ar` → `data.tar.xz` →
     `app.asar` → enclosure brace-match; no `dpkg`/`asar` needed).
   - **[`scripts/latest-desktop-sha.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/latest-desktop-sha.py)**
     runs the full "find the newest Desktop → download → extract → compare to
     `UPSTREAM_SHA`" loop.

   Observed pins: Linux **1.18286.0** (2026-07-02) pinned `7c2f88d…`; **1.20186.1
   → 1.24012.9** pin `5db5e4a…` (the current baseline).
3. **The Desktop machine's cache** — per-platform binaries under
   `<app-data>/claude-ssh-remote/<sha>/` (`%APPDATA%/Claude/…` on Windows),
   alongside a `.verified-<goos>-<goarch>` marker.
4. **Probe a guess** — `manifest.json` returns 200 for a real SHA, 404 otherwise.

> Note: the *CLI* release manifest
> (`claude-code-releases/<ver>/manifest.json`) has a `commit` field, but that is
> the **CLI's** commit, not the daemon's — don't confuse them.

### claustrum re-uploads every session (harmless)

Before a session the client runs `server --version` on the cached
`~/.claude/remote/srv/<pinned-sha>/server`, matches `/claude-ssh\s+(\S+)/`, and
skips re-upload only when that first token equals the pinned SHA. claustrum prints
`claustrum <ver> (built …)` — its **own** identity — so the token never matches and
the daemon is re-SFTP'd (idempotently, ~2.3 MB) **every session**. That is a
consequence of the `claude-ssh:`→`claustrum:` rebrand; it is CLI stdout, **not** a
JSON-RPC frame, so the wire contract is untouched and the redeploy is harmless — the
daemon that ends up running is still claustrum.

A drop-in build stamp (emit `claude-ssh <pinned-sha>` as the **first** token,
placed at `srv/<pinned-sha>/server`) would make the client see claustrum as
up-to-date and stop overwriting it — off by default, an opt-in build stamp, not a
wire change. Mechanism and the `-version` format:
[docs/PROTOCOL.md → `-version`](PROTOCOL.md#-version).

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
check is the **real** desktop client's traffic. The method:

- The bridge (`server --bridge`) is a dumb stdio relay, so teeing its
  stdin/stdout captures the exact client↔daemon NDJSON of a live session, which
  can then be replayed against claustrum and diffed.
- Tooling lives in `scratch/capture/` (gitignored): a `capture-bridge`,
  `replay.js`, and `REPLAY.md` runbook. `replay.js` diffs order-insensitively —
  responses keyed by `id`, stream frames by `processId`+`seq` — and masks the
  version SHA, the token, and (with `--mask-data`) nondeterministic agent payloads.
- The session is captured under a **throwaway** SSH user via a `ForceCommand`
  wrapper scoped to that user, so it never touches a live daemon. Raw dumps carry
  the session token and host paths — keep them in `scratch/` (gitignored); never
  commit them.

Last exercised against a then-pinned reference (`8de85faa`, now well behind the
current `5db5e4a` baseline): a real Desktop session — the full `process.*`
lifecycle including a >32 KiB output stream and a mid-stream disconnect/reconnect
that drove `process.reattach` — verified **byte-identical** for the methods that
build exposed. The `server.capabilities` set, the stream-envelope field order, and
per-process `reattach` replay all matched; the frame shapes themselves are canonical
in [docs/PROTOCOL.md](PROTOCOL.md).

Use this as a periodic spot-check, or when a new build changes `process.*` behavior
the synthetic battery can't fully model (real reconnect timing, multi-process
reattach).

## Step 4 — reconcile

If the check reports drift:

1. Identify exactly what changed (new method? changed error string? new flag? new
   install fact? changed frame shape?).
2. Implement the change in claustrum behind the existing structure, keeping every
   unchanged behavior unchanged.
3. Re-run the static check **and** the byte-for-byte battery until both are clean.
4. Update `scripts/UPSTREAM_SHA` to the new SHA, note the change in `CHANGELOG.md`,
   and update `docs/PROTOCOL.md` if the wire surface changed.

## Don't reconcile the deliberate divergences away

Not every claustrum behavior is meant to match the reference. The **deliberate
divergences** (D1–D14, CT-1..CT-5) and the four-rule standard that governs them are
catalogued in [docs/DIVERGENCES.md](DIVERGENCES.md) — that is the canonical home
for what each one is, why it exists, how to activate it, and its reopen trigger.
When the drift check flags one of them, it is expected, not drift; confirm against
the catalog before "fixing" it.

What this triage index adds is the one question the catalog doesn't answer
directly: **can a probe even see this divergence, and if so, is the symptom drift
or an activated opt-in?**

### Which divergences a probe can see

`battery-visible?` = would the standard frame battery (`validate.sh` / `battery.js`)
surface a diff. The install-path bounds (D10–D14) run under `-install`, which the
frame battery never drives at all.

| ID | Default | Battery-visible? | What it is |
|----|---------|------------------|------------|
| D1 | Conditional — fires when `-cli-checksum` is supplied (caller-activated, **not** operator-declinable; on `-install` the caller is Desktop) | No via frames; `scratch/probe/cli_probe.sh` **does** drive it | `-cli-zst` SHA-256 verification |
| D3 | Off (`0` = unlimited) | No — off the default path | `files.extract_tar` size cap |
| D4 | Off | **Yes** — `battery.js` id 70 reads `/dev/null`; no diff at default (guard off), turns red once armed | `files.read` regular-file guard |
| D5 | Off (`0` = no deadline) | No — off the default path | git-invocation deadline |
| D10 | Off (`0` = unlimited) | No — install path | `-install` CLI size cap |
| D11 | Off (`0` = no deadline) | No — install path | `-install` runnability-probe deadline |
| D12 | Off (`0` = no bound) | No — install path | `-install` download bound |
| D14 | Off (`0` = no deadline), **linux only** | No — install path | `ldd --version` libc probe deadline |
| D2 | Always-on | Maybe, if a probe reaches the path (expected) | destructive-path home-dir refusal |
| D6 / D7 | Always-on | Maybe, if a probe reaches the path (expected) | `-cli-version` single path component / temp-sweep collision |
| D8 | Always-on | No — falls back to inherited stdio, not a frame | `remote-server.log` declined, not shared |
| D9 | Always-on | Maybe — a type-mismatched namespace field is rejected | namespace-param binding vs. the reference's ignore |
| D13 | Always-on (**unresolved** in DIVERGENCES.md) | No — install path | verify-before-decompress ordering |
| CT-1 | Opt-in (`wantPid`) | **Yes**, when requested — adds `pid`/`startTime` | spawn/reattach reply extension |
| CT-2 | Opt-in (`-keep-children`, POSIX) | No | children survive shutdown |
| CT-3 | Opt-in (`claustrum.conf`) | Only `version-override`, via the static check's `-version` diff | the config file itself |
| CT-5 | Opt-in (`-listen-pipe`, Windows) | No | additional named-pipe transport |

**Check both indexes.** Beyond the D/CT catalog above, several claustrum-only
behaviors are numbered by *tier item* in the improvements ledger
([docs/IMPROVEMENTS.md](IMPROVEMENTS.md)) and are just as real: item 16
(`-metrics-addr`), item 17 (the orphaned previous process tree is torn down), item
18 (`-token-fd`), item 21 (the kill signal is skipped when the child has already
exited). Check both the divergence catalog and the tier index before concluding
that something is drift.

### Drift, or an activated opt-in? — the parse-behaviour table

An opt-in key that is *present* is not a deadline that is *in force*: a mistyped or
inert value leaves the divergence off, and the resulting parity behavior reads
exactly like drift. So when a symptom matches a D-shaped divergence on a stock
claustrum, **check for the key or flag first, and confirm the value parses to a
positive duration.** The four duration knobs — D5 (`git-timeout`, logs under
`[Server]`), D11 (`cli-probe-timeout`), D12 (`cli-download-timeout`), D14
(`libc-probe-timeout`); the last three log under `[Install]` — all parse the same
way:

| Value shape | Config key in `claustrum.conf` | `-…` flag on the argv |
|-------------|--------------------------------|-----------------------|
| Unparseable (`= 15`) | Dropped **silently** — no log — run proceeds with no deadline | `flag.Duration` rejects it before any mode runs: `invalid value "15" for flag …: parse error` + usage, **exit 2**, no facts line |
| Negative (`= -1s`) | Dropped **silently** — no log — no deadline | Normalised to 0 with a `[Server]`/`[Install]` stderr warning, then runs normally (exit 0, facts line printed) |
| Any zero (`0`, `+0`, `-0`, `0s`, `-0m`, `-0.4ns`) | **Accepted** — reads as opted-in but the deadline stays off | Accepted — deadline off |

The config path being silent is the shape that looks like drift. A missing
`__INSTALL_RESULT__` does **not** by itself mean a malformed flag — a stock
claustrum blocked on a CLI that never answers also prints no facts line (that is the
deadline-off parity behavior). Discriminate on exit: **exit 2 with `parse error` on
stderr** is the bad flag; **still running with nothing on stderr** is the parity
wait.

**D4 is the one exception — it is a bool, not a duration** (`files-read-regular-only`):

- Config value unrecognised (`= maybe`): dropped **silently**, key left unset, so
  the flag value stands (off, unless a flag was also passed).
- Flag `=value` form unrecognised (`-files-read-regular-only=maybe`): rejected by
  `flag.Bool` — usage + exit 2.
- Flag space form (`-files-read-regular-only maybe`): **arms** the guard (a bool
  never consumes the next arg); `maybe` becomes a positional and parsing **stops**,
  silently dropping every later flag — so the guard is armed only if `-serve` and
  the socket/token flags were parsed *before* the typo.
- Inert-but-accepted spelling: `false` (both forms) — contains the name a triager
  greps for, but arms nothing.

### Triage gotchas — when a probe result is misleading

Full measurements live in [docs/DIVERGENCES.md](DIVERGENCES.md); the traps that
matter for telling drift from expected:

- **D4 / D5 (writerless FIFO, surviving-child git):** a harness that records "no
  reply from both binaries" is non-discriminating — the off state blocks too.
  `/dev/null` is D4's discriminating input; a surviving child makes D5 soft
  (`CombinedOutput` waits on the output pipe, not on git's exit).
- **D5 has a wire-invisible arm:** a killed `git ls-files` makes
  `git.worktree_create` answer `{"success":true}` with `.worktreeinclude` files
  absent — nothing on the wire says so, so a clean frame diff does not cover it.
- **D12 needs a VALID zstd body** (an invalid one is answered by D13's ordering at
  0 s, which reads like "no divergence"); and a zero download timeout frees the
  **body read only** — `http.DefaultTransport` still applies `net.Dialer{Timeout:
  30s}` and `TLSHandshakeTimeout: 10s`.
- **D13 has two honest shapes** a triager must not merge: a short/truncated
  artifact reaches the checksum (claustrum `checksum mismatch: …`), while a genuine
  interrupted transfer never does — `io.Copy`'s error returns first — so claustrum
  diverges on the *prefix* (`download failed: <err>`).
- **D14 fires in only one of two stall shapes** (a surviving child blocks both
  binaries) and there is **no libc probe off linux at all**, so a `libc` difference
  off linux is not D14.

## Automating it

- **[`.github/workflows/upstream-desktop-watch.yml`](https://github.com/schubydoo/claustrum/blob/main/.github/workflows/upstream-desktop-watch.yml)**
  runs **twice daily** (cron `17 6,18 * * *`): it calls
  `scripts/latest-desktop-sha.py` to discover the SHA the *newest* Claude Desktop
  for Linux pins (Step 1, automated — no out-of-band source needed) and compares it
  to `scripts/UPSTREAM_SHA`. Only when the pin has moved does it run
  `check-upstream.sh <sha>` for the static drift diff and open a single idempotent
  tracking issue; the usual run is just download-extract-compare. Reconciliation
  (Step 4) stays a human decision.
- The watcher currently covers **Linux** only. macOS/Windows Desktop builds pin the
  same per-SHA CDN artifacts, so they're a redundant cross-check rather than a new
  signal; extraction from those bundles is a possible follow-up.
- `check-upstream.sh` can still be run by hand against any SHA (e.g. a
  freshly-discovered one, or to confirm a re-published build hasn't shifted).
