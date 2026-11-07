# plugins.prune

`plugins.prune` deletes the cached plugin directories that no session used for some days. The caller cannot name a path. An empty `pruned` list does not mean "nothing to delete": read `errors` and `legacy` too.

An untagged rule here matches build `5fd08069` or an earlier build.

## Request

```json
{"minAgeDays": 30}
```

| Parameter | Required | Meaning |
|---|---|---|
| `minAgeDays` | no | Days that a directory stays after its last use, limited to 7 to 3650. The default is 7. |
| `keep` | no | Strings that the daemon accepts and ignores. A name in it is deleted as any other. |

No `params` member gets `-32602 Invalid params: unexpected end of JSON input` (measured on Linux). `"params": null` gets the defaults (not measured).

## Response

```json
{"root": "/home/me/.claude/remote/plugins/abc", "pruned": ["0123456789abcdef"], "prunedLegacy": [], "staleArchives": 0, "kept": 0, "live": 1, "young": 2, "legacy": "swept", "minAgeDays": 30}
```

| Member | Meaning |
|---|---|
| `root` | The root of this install. |
| `pruned` | The hashes deleted in the root of this install, sorted. |
| `prunedLegacy` | The hashes deleted in the shared root, sorted. |
| `staleArchives` | Always `0`. |
| `kept` | Always `0`. |
| `live` | Live directories, over the roots that the daemon went through or counted. |
| `young` | Young directories that are not live, over the same roots. |
| `legacy` | What the daemon did with the shared root: see [The `legacy` member](#the-legacy-member). |
| `skipped` | The reason, only where the daemon goes through no root. |
| `errors` | `<path>: <error>` for each directory that the daemon cannot delete and each root that it cannot list. A missing root is no failure. A directory in `errors` can be deleted in part. With no failure, the member is absent. A response with `errors` is still a success (not measured). |
| `minAgeDays` | The value that the daemon used, after the limits. |

If the socket of the daemon is not at `run/<clientId>/rpc.sock`, the daemon has no plugin roots and deletes nothing. `root` is then `""` and `minAgeDays` is `0`. claustrum also gives it for a `<clientId>` that is neither 8 lowercase hexadecimal characters nor a host term (claustrum's own, not measured). `c9125eb8` did so for each such name it got.

## Errors

A failure is a JSON-RPC error, with no `errorCode` member.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params: <detail>` | The JSON decoder refuses `params`, for example a member of the wrong type, or `minAgeDays` is not a whole number. `<detail>` is its text. The other methods answer `Invalid params` with no detail. |

## Which directories the daemon deletes

The daemon derives two roots from the path of its own socket, `<X>/run/<clientId>/rpc.sock`:

- The root of this install, `<X>/plugins/<clientId>`.
- The shared root of older versions, `<X>/plugins`.

In a root, the daemon looks only at directories whose name is 16 lowercase hexadecimal characters, the content hash of a plugin. It leaves each other entry alone, files and symbolic links too.

The daemon keeps a directory in two cases and else deletes it as a tree:

- The directory is live. A run of exactly 16 lowercase hexadecimal characters in the command or an argument of a child of `process.spawn` names it, until the daemon reports that child's exit.
- The directory is young. Its last use is inside `minAgeDays` days. The last use is the newer modification time of the directory and of its `.synced` file, not of `manifest.json`. With neither time, the directory is not young.

Except at [two skips](#skips-that-keep-both-roots), the daemon goes through the root of this install, and through the shared root while no other daemon runs.

### The `legacy` member

| `legacy` | Meaning |
|---|---|
| `absent` | The daemon cannot examine the shared root, for example a missing one. |
| `swept` | The daemon went through the shared root. |
| `skipped: another daemon is running (<detail>); its sessions may use the shared legacy dirs` | Another daemon runs, or the daemon cannot exclude one. It does not show that the shared root exists. |
| `skipped: another daemon of this install may be running (<detail>); its sessions may use the same dirs` | As above, for any such daemon whose run directory name is neither 8 lowercase hexadecimal characters nor, on macOS and Windows, a host term of another `<clientId>`. |
| `skipped: machines share this home (this daemon's run directory bears a host term), and another machine's sessions cannot be seen from here` | A [host term](#skips-that-keep-both-roots). |
| `skipped: this daemon's socket is not under a run directory of an install, so it has no per-install plugin root to prune` | The daemon has no plugin roots. |

The last three also put the text after `skipped: ` in `skipped`. `c9125eb8` gave the first and the last skip text on Linux, macOS and Windows.

### The `<detail>` text

The daemon goes through the names under `<X>/run` in order. `<detail>` names the first install that it cannot prove gone, or the run directory itself. In the `of this install` text it names the first such install whose run directory name is neither 8 lowercase hexadecimal characters nor, on macOS and Windows, a host term of another `<clientId>`. `<rel>` is `run/<clientId>/rpc.sock`:

| `<detail>` | Meaning |
|---|---|
| `<rel> answers` | The socket accepted a connection inside 300 ms. |
| `cannot rule out <rel>: <error>` | The connection failed, but not in a way that shows no listener. |
| `cannot inspect <socket path>: <error>` | The daemon cannot examine the socket path. |
| `cannot read <run directory>: <error>` | The daemon cannot list `<X>/run`. |

### Skips that keep both roots

A run directory `<8 lowercase hex>-<8 lowercase hex>` bears a host term. Its root is `plugins/<the first 8 hex>`. This test comes first (`c9125eb8`, measured on Linux, macOS and Windows).

`run/beef5678-<host name>/rpc.sock` answering gave the other skip (`c9125eb8`, measured on Linux and macOS). On Windows: claustrum's own, not measured. claustrum's own, not measured: the name test over each sibling, the sibling that the text names, the other `<detail>` forms.

A sibling of the host-term shape, `run/beef5678-<machine-id first 8>/rpc.sock`, gave no such skip. `c9125eb8` deleted the old directories of this root and gave the first sibling text (measured on macOS and Windows). There `young` counted only this root. Another 8 hex after the dash: claustrum's own, not measured. On Linux claustrum keeps both roots for that name (claustrum's own, not measured). A host-term sibling whose first 8 hex are the `<clientId>` of this daemon uses this same root. claustrum keeps both roots for it on each system (claustrum's own, not measured).

Both skips delete nothing, and `young` counts each hash directory of this root that is not live, old ones too (`c9125eb8`, measured on Linux and macOS). How a live one counts: claustrum's own, not measured.

## Differences by system

Paths and `<error>` texts come from the operating system, except below. `<rel>` has `/` separators on each system. On Windows, `-listen-pipe` does not change the roots, which come from the socket path.

A sibling socket path over 107 bytes on Linux or 103 on macOS (`be1c1ca5`, measured on Linux and macOS):

- Linux dials it through `/proc`, as at a [long socket path](../DAEMON.md#a-long-socket-path-linux-and-macos). A dead sibling counts as gone. The maintainer approved acting at a long root on 2026-11-01.
- Where no shorter name reaches it, `<error>` is `dial unix <socket path>: the socket path is longer than the 107 bytes that this system allows` (`103 bytes` on macOS).
- If the shorter name fails otherwise, `; no shorter name for it worked: <error of that dial>` follows the limit text. A sibling socket of mode 000 on Linux gives it. Other failures of that dial: claustrum's own, not measured.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method. The `errors` member is not measured.
