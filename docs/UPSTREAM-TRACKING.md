# Staying in lock-step with the reference daemon

claustrum is behaviorally compatible with a reference daemon that ships inside
Claude Desktop's SSH-remote feature. That daemon is versioned by **git SHA** and
distributed as per-platform zstd blobs on a public CDN. This doc is how we detect
when a new build appears and whether it changed anything we need to match.

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
2. **The Desktop machine** — it caches per-platform binaries under
   `%APPDATA%/Claude/claude-ssh-remote/<sha>/` (macOS/Linux: the app's data dir),
   alongside a `.verified-<goos>-<goarch>` marker.
3. **Probe a guess** — `manifest.json` returns 200 for a real SHA, 404 otherwise.

> Note: the *CLI* release manifest
> (`claude-code-releases/<ver>/manifest.json`) has a `commit` field, but that is
> the **CLI's** commit, not the daemon's — don't confuse them.

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
check is the **real** desktop client's traffic. The bridge (`server --bridge`) is
a dumb stdio relay, so teeing its stdin/stdout captures the exact client↔daemon
NDJSON of a live session — which can then be replayed against claustrum and
diffed. Tooling lives in `scratch/capture/` (gitignored): a `capture-bridge`, a
`replay.js` (order-insensitive diff: responses keyed by `id`, stream frames by
`processId`+`seq`; masks the version SHA, token, and — with `--mask-data` — the
nondeterministic agent payloads), and `REPLAY.md` with the capture runbook. The
session is captured under a **throwaway** SSH user via a `ForceCommand` wrapper
scoped to that user, so it never touches a live daemon.

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

## Automating it

- A scheduled CI job (e.g. weekly `nightly.yml`) can run `check-upstream.sh`
  against the pinned SHA and open an issue if the manifest/strings/flags it can
  reach have changed. Because finding a *new* SHA needs an out-of-band source
  (above), the cron primarily guards against the pinned build being re-published
  or its surface shifting; feed it a freshly-discovered SHA to check a new build.
