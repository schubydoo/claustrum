# Staying in lock-step with the reference daemon

claustrum is behaviorally compatible with a reference daemon. That daemon ships
inside Claude Desktop's SSH-remote feature. A **git SHA** gives its version, and a
public CDN holds it as per-platform zstd blobs. This document tells you how to
detect a new build. It also tells you how to find out if that build changed
anything claustrum must match. For the running history of which builds changed
what, see the [reference build ledger](REFERENCE-BUILDS.md).

## How the reference is distributed

- Per-version manifest:
  `https://downloads.claude.ai/claude-ssh-releases/<sha>/manifest.json`
  → `{"version":"<sha>","platforms":{"<goos>-<goarch>":{"checksum":"<sha256 of .zst>","size":N}}}`
- Per-platform artifact:
  `https://downloads.claude.ai/claude-ssh-releases/<sha>/<goos>-<goarch>/claude-ssh.zst`
- Six targets: `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`,
  `windows-amd64`, `windows-arm64`. (GOARCH naming — `amd64`, not `x64`.)

There is **no "latest" index**. The SHA is the key to each release, so step 1 is
always "find the new SHA."

## Step 1 — find a candidate SHA

The SHA of the reference build is the SHA Claude Desktop deploys now. These are the
sources, easiest first:

1. **A host Desktop has connected to** — the cache holds one daemon per SHA:
   ```sh
   ls -1d ~/.claude/remote/srv/*/server | sed -E 's#.*/srv/([0-9a-f]+)/server#\1#'
   ```
2. **The Desktop app bundle** — the app contains the pinned SHA, and all six
   per-platform checksums and sizes, as a **build-time constant**. You can read
   that constant offline, and you connect to nothing. In the Linux `.deb` it is a
   `JSON.parse('{"version":"<sha>","manifest":{…},"baseUrl":".../claude-ssh-releases"}')`
   literal inside `resources/app.asar`. That file is a minified
   `.vite/build/index.chunk-*.js`, so the chunk name and the wrapper function are
   random for each build. A parallel literal pins the CLI
   (`claude-code-releases`). From version **1.24012.9** onward, its `baseUrl`
   carries a channel suffix — `claude-code-releases/rc/<sha>` — so the extractor
   matches the bucket by path *segment*, not by `endswith`. A new Desktop build is
   itself the "new SHA" signal. Two scripts read the constant:
   - **[`scripts/extract-desktop-pin.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/extract-desktop-pin.py)**
     reads it directly from a `.deb`. It uses the standard library only: `ar` →
     `data.tar.xz` → `app.asar` → enclosure brace-match. You need no `dpkg` and no
     `asar`.
   - **[`scripts/latest-desktop-sha.py`](https://github.com/schubydoo/claustrum/blob/main/scripts/latest-desktop-sha.py)**
     runs the full loop: find the newest Desktop → download → extract → compare to
     `UPSTREAM_SHA`.

   Observed pins: Linux **1.18286.0** (2026-07-02) pinned `7c2f88d…`. Versions
   **1.20186.1 → 1.24012.9** pin `5db5e4a…` (the current baseline).
3. **The Desktop machine's cache** — the per-platform binaries are under
   `<app-data>/claude-ssh-remote/<sha>/` (`%APPDATA%/Claude/…` on Windows),
   beside a `.verified-<goos>-<goarch>` marker.
4. **Probe a guess** — `manifest.json` returns 200 for a real SHA. It returns 404
   for all other values.

> Note: the *CLI* release manifest
> (`claude-code-releases/<ver>/manifest.json`) has a `commit` field. That field
> gives the **CLI's** commit, not the daemon's. Do not confuse the two.

### claustrum re-uploads every session (harmless)

Before a session the client runs `server --version` on the cached
`~/.claude/remote/srv/<pinned-sha>/server`. It then matches `/claude-ssh\s+(\S+)/`,
and it skips the re-upload only when that first token equals the pinned SHA.
claustrum prints `claustrum <ver> (built …)`, which is its **own** identity. So the
token never matches, and the client sends the daemon again by SFTP **every
session**. That transfer is idempotent and is approximately 2.3 MB. The
`claude-ssh:`→`claustrum:` rebrand causes this. The version line is CLI stdout,
**not** a JSON-RPC frame, so the wire contract does not change and the redeploy is
harmless — the daemon that runs at the end is still claustrum.

A drop-in build stamp would make the client see claustrum as up-to-date and stop
the overwrite. Such a stamp emits `claude-ssh <pinned-sha>` as the **first** token,
and the binary goes at `srv/<pinned-sha>/server`. It is off by default — an opt-in
build stamp, not a wire change. Mechanism and the `-version` format:
[docs/PROTOCOL.md → `-version`](PROTOCOL.md#-version).

## Step 2 — run the drift check

```sh
scripts/check-upstream.sh <sha>
# or, with the pinned baseline SHA in scripts/UPSTREAM_SHA:
scripts/check-upstream.sh
```

The script needs network access. It does these steps:

1. Compare `<sha>` to the pinned baseline in `scripts/UPSTREAM_SHA`. A difference
   is itself the "new release" signal.
2. Fetch the manifest. Download the `linux-amd64` `claude-ssh.zst`. **Verify it
   against the manifest checksum**. Decompress it.
3. Build claustrum. Then diff the two binaries on the items claustrum must match:
   - **method names** (`server.*`/`files.*`/`git.*`/`process.*` literals),
   - **CLI flags** (`-help` output),
   - **`-version` format**,
   - the **app-facing string set** (errors, `[Server]`/`[process.Manager]`/
     `[frameSink]`/`[shellenv]` log lines, flag help).
4. Print `PASS` if there is no drift in the checked surface. Print `DRIFT` with
   the specifics if there is drift, and exit with a non-zero status.

This static check needs no running daemon. It is safe to run on any machine that
has network access.

## Step 3 — authoritative byte-for-byte recheck (local only)

The static check catches methods, flags, and strings that were added or removed. To
confirm **frame-level** byte-identity (result field order, stream framing, error
bodies, `reattach` semantics), run the private validation battery against both the
new reference and claustrum. That battery stays in `scratch/` and is not published:

```sh
# starts each binary as a PRIVATE -serve on a throwaway /tmp socket, runs the
# full method battery, and diffs normalized frames. Never touches a live daemon.
scratch/probe/validate.sh <path-to-reference> > /tmp/ref.json
scratch/probe/validate.sh ./claustrum          > /tmp/mine.json
diff /tmp/ref.json /tmp/mine.json
```

> Safety: probe only a **private** instance on a `/tmp` socket with its own
> `-token-file`. Never point the harness at a live daemon's socket. Clean up after
> every probe.

## Step 3b — real-session capture (highest fidelity, optional)

Steps 2–3 drive the daemon with **synthetic** requests we write. The best check is
the traffic of the **real** desktop client. This is the method:

- The bridge (`server --bridge`) is a simple stdio relay. So a tee on its stdin and
  stdout records the exact client↔daemon NDJSON of a live session. You can then
  replay that record against claustrum and diff it.
- The tools are in `scratch/capture/` (gitignored): a `capture-bridge`,
  `replay.js`, and a `REPLAY.md` runbook. `replay.js` diffs without regard to
  order — it keys responses by `id` and stream frames by `processId`+`seq`. It
  masks the version SHA, the token, and (with `--mask-data`) nondeterministic agent
  payloads.
- A **throwaway** SSH user records the session, through a `ForceCommand` wrapper
  scoped to that user, so the capture never touches a live daemon. Raw dumps carry
  the session token and host paths. Keep them in `scratch/` (gitignored). Never
  commit them.

We last ran this capture against a then-pinned reference — `8de85faa`, now well
behind the current `5db5e4a` baseline. It used a real Desktop session and covered
the full `process.*` lifecycle, which included a >32 KiB output stream and a
mid-stream disconnect/reconnect that drove `process.reattach`. The result was
**byte-identical** for the methods that build exposed. The `server.capabilities`
set, the stream-envelope field order, and per-process `reattach` replay all
matched. [docs/PROTOCOL.md](PROTOCOL.md) holds the canonical frame shapes.

Use this capture as a periodic spot-check. Also use it when a new build changes
`process.*` behavior the synthetic battery can't fully model (real reconnect
timing, multi-process reattach).

## Step 4 — reconcile

If the check reports drift:

1. Identify exactly what changed: a new method, a changed error string, a new
   flag, a new install fact, or a changed frame shape.
2. Implement the change in claustrum behind the existing structure. Keep every
   unchanged behavior unchanged.
3. Run the static check **and** the byte-for-byte battery again, until both are
   clean.
4. Update `scripts/UPSTREAM_SHA` to the new SHA. Note the change in
   `CHANGELOG.md`. Update `docs/PROTOCOL.md` if the wire surface changed.

## Don't reconcile the deliberate divergences away

Not every claustrum behavior is meant to match the reference.
[docs/DIVERGENCES.md](DIVERGENCES.md) catalogues the **deliberate divergences**
(D1–D14, CT-1..CT-5) and the four-rule standard that governs them. That file is the
canonical home for what each one is, why it exists, how to activate it, and its
reopen trigger. When the drift check flags one of them, it is expected, not drift.
Confirm against the catalog before you "fix" it.

This triage index adds the one question the catalog doesn't answer directly:
**can a probe even see this divergence, and if so, is the symptom drift or an
activated opt-in?**

### Which divergences a probe can see

`battery-visible?` asks whether the standard frame battery (`validate.sh` /
`battery.js`) would show a diff. The install-path bounds (D10–D14) run under
`-install`, which the frame battery never drives at all.

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

**Check both indexes.** The D/CT catalog above is not the only index. The shipped
ledger ([docs/IMPROVEMENTS.md](IMPROVEMENTS.md)) numbers several more
claustrum-only behaviors, and they are just as real: item 16 (`-metrics-addr`),
item 17 (the orphaned previous process tree is torn down), item 18 (`-token-fd`),
item 21 (the kill signal is skipped when the child has already exited). Check both
the divergence catalog and the shipped ledger before you conclude that something is
drift.

### Drift, or an activated opt-in? — the parse-behaviour table

An opt-in key that is *present* is not a deadline that is *in force*. A mistyped or
inert value leaves the divergence off, and the parity behavior that results reads
exactly like drift. So when a symptom matches a D-shaped divergence on a stock
claustrum, **check for the key or flag first, and confirm the value parses to a
positive duration.** There are four duration knobs: D5 (`git-timeout`), D11
(`cli-probe-timeout`), D12 (`cli-download-timeout`), and D14
(`libc-probe-timeout`). D5 logs under `[Server]`; the last three log under
`[Install]`. All four parse the same way:

| Value shape | Config key in `claustrum.conf` | `-…` flag on the argv |
|-------------|--------------------------------|-----------------------|
| Unparseable (`= 15`) | Dropped **silently** — no log — run proceeds with no deadline | `flag.Duration` rejects it before any mode runs: `invalid value "15" for flag …: parse error` + usage, **exit 2**, no facts line |
| Negative (`= -1s`) | Dropped **silently** — no log — no deadline | Normalised to 0 with a `[Server]`/`[Install]` stderr warning, then runs normally (exit 0, facts line printed) |
| Any zero (`0`, `+0`, `-0`, `0s`, `-0m`, `-0.4ns`) | **Accepted** — reads as opted-in but the deadline stays off | Accepted — deadline off |

The config path is silent, and that silence is the shape that looks like drift. A
missing `__INSTALL_RESULT__` does **not** by itself mean a malformed flag — a stock
claustrum blocked on a CLI that never answers also prints no facts line (that is the
deadline-off parity behavior). Discriminate on exit: **exit 2 with `parse error` on
stderr** is the bad flag; **still running with nothing on stderr** is the parity
wait.

**D4 is the one exception — it is a bool, not a duration** (`files-read-regular-only`):

- Config value unrecognised (`= maybe`): the parser drops it **silently** and
  leaves the key unset, so the flag value stands (off, unless a flag was also
  passed).
- Flag `=value` form unrecognised (`-files-read-regular-only=maybe`): `flag.Bool`
  rejects it — usage + exit 2.
- Flag space form (`-files-read-regular-only maybe`): **arms** the guard, because a
  bool never consumes the next arg. `maybe` becomes a positional and parsing
  **stops**, which silently drops every later flag — so the guard is armed only if
  the parser read `-serve` and the socket/token flags *before* the typo.
- Inert-but-accepted spelling: `false` (both forms) — it contains the name a
  triager greps for, but it arms nothing.

### Triage gotchas — when a probe result is misleading

[docs/DIVERGENCES.md](DIVERGENCES.md) holds the full measurements. These are the
traps that matter when you tell drift from expected:

- **D4 / D5 (writerless FIFO, surviving-child git):** a harness that records "no
  reply from both binaries" does not discriminate — the off state blocks too.
  `/dev/null` is D4's discriminating input; a surviving child makes D5 soft
  (`CombinedOutput` waits on the output pipe, not on git's exit).
- **D5 has a wire-invisible arm:** when the deadline kills `git ls-files`,
  `git.worktree_create` still answers `{"success":true}` and the
  `.worktreeinclude` files are absent. Nothing on the wire says so, so a clean
  frame diff does not cover it.
- **D12 needs a VALID zstd body** — D13's ordering answers an invalid one at 0 s,
  which reads like "no divergence". Also, a zero download timeout frees the **body
  read only**: `http.DefaultTransport` still applies `net.Dialer{Timeout: 30s}` and
  `TLSHandshakeTimeout: 10s`.
- **D13 has two honest shapes** a triager must not merge. A short/truncated
  artifact reaches the checksum (claustrum `checksum mismatch: …`). A genuine
  interrupted transfer never does, because `io.Copy`'s error returns first — so
  claustrum diverges on the *prefix* (`download failed: <err>`).
- **D14 fires in only one of two stall shapes** (a surviving child blocks both
  binaries), and there is **no libc probe off linux at all** — so a `libc`
  difference off linux is not D14.

## Automating it

- **[`.github/workflows/upstream-desktop-watch.yml`](https://github.com/schubydoo/claustrum/blob/main/.github/workflows/upstream-desktop-watch.yml)**
  runs **twice daily** (cron `17 6,18 * * *`). It calls
  `scripts/latest-desktop-sha.py` to find the SHA the *newest* Claude Desktop for
  Linux pins (Step 1, automated — no out-of-band source needed), then compares that
  SHA to `scripts/UPSTREAM_SHA`. Only when the pin has moved does it run
  `check-upstream.sh <sha>` for the static drift diff and open a single idempotent
  tracking issue; the usual run is just download-extract-compare. Reconciliation
  (Step 4) stays a human decision.
- The watcher currently covers **Linux** only. macOS/Windows Desktop builds pin the
  same per-SHA CDN artifacts, so they give a redundant cross-check rather than a
  new signal. Extraction from those bundles is a possible follow-up.
- You can still run `check-upstream.sh` by hand against any SHA — for example, a
  SHA you just found, or a check that a re-published build hasn't shifted.
