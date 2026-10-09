# plugins.prune: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-09, unchanged. It holds the detailed rules of `plugins.prune` and the measurements behind them, against the reference build `19f30c46`. A new measurement of the method goes into this page.

To use the method, read [plugins.prune](../protocol/plugins-prune.md).

`{[keep],[minAgeDays]}` → `{"root":"<per-install root>","pruned":[…],"prunedLegacy":[…],"staleArchives":0,"kept":0,"live":<n>,"young":<n>,"legacy":"absent|swept|skipped: …","minAgeDays":<clamped>}`

Prunes cached CLI plugin directories the daemon keeps under its socket's
run-directory layout. The daemon socket is `<X>/run/<clientId>/rpc.sock`. The
per-install root is `<X>/plugins/<clientId>`, and its results go in `pruned`. The
shared legacy root is `<X>/plugins`, and its results go in `prunedLegacy`.

- A plugin directory is named by its content hash, 16 lowercase hex. Its
  last-used time is the newer of the directory's own mtime and its `.synced`
  marker's mtime. The `manifest.json` mtime does not count. A directory is kept
  when it is live, and it is kept when it is young. Live means its hash appears in
  a running child's argv, because the agent CLI is launched with
  `--plugin-dir <…>/<hash>`. That gives the `live` count. Young means last-used
  within `minAgeDays`, which gives the `young` count. Otherwise it is removed and
  its hash is appended to `pruned` or `prunedLegacy`, each sorted.
- `minAgeDays` is the retention window, clamped to `[7, 3650]`. An absent value
  reads as 0 and clamps up to 7. The clamped value is echoed.
- `keep` is a `[]string` accepted on the params. `kept` and `staleArchives` are
  always-present counters. Neither `keep`, a caller list, nor `staleArchives`,
  stale non-directory files, was observed to fire against `19f30c46`. A valid old
  plugin whose hash was in `keep` was still pruned, and no plain file was swept
  whatever its extension. Both counters therefore stay 0 here. They
  are reproduced as fields for parity. Their non-zero triggers are not yet pinned.
- `legacy` reports the shared `plugins/` sweep, and the sibling test runs
  first. If a sibling daemon on the host is present, or cannot be ruled out gone,
  `legacy` is `"skipped: another install's daemon is running (<detail>); its sessions
  may use the shared legacy dirs"`, even when the shared directory does not exist.
  `<detail>` describes what blocked the sweep. It names the first present sibling
  in sorted `run/<clientId>` order, or the run directory itself on a failed
  listing. It takes one of four forms, where `<rel>` is
  `run/<clientId>/rpc.sock`:
  - `"<rel> answers"`: the sibling socket accepted a 300 ms unix dial.
  - `"cannot rule out <rel>: <err>"`: the dial failed for a reason other than
    connection-refused, not-a-socket, or not-found.
  - `"cannot inspect <sock>: <err>"`: a stat of the socket path failed.
  - `"cannot read <run-dir>: <err>"`: a listing of the run directory failed.

  With no sibling present, `legacy` is `"absent"` when the shared directory is
  missing, or `"swept"` once it is processed. The per-install sweep (`pruned`)
  always runs. The daemon skips only the legacy sweep, because a present sibling's
  sessions can reference the shared legacy plugins.
- If the socket is not `run/<clientId>/rpc.sock`, the daemon has no per-install
  root. `root` is then `""`, `minAgeDays` is 0, `legacy` is
  `"skipped: <reason>"`, and a `skipped` field carries the reason.
- Bad params answer `-32602 "Invalid params: <encoding/json detail>"`. That frame
  carries the detail, unlike the other methods' bare `Invalid params`. A
  `plugins.<other>` method answers `-32601 "Unknown method: plugins.<x>"`.
  `plugins.prune` requires auth like every method except `server.shutdown`.
