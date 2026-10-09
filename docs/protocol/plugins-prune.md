# plugins.prune

`plugins.prune` deletes the cached plugin directories that no session used for some days. The directories are under the plugin roots of the daemon. The caller cannot name a path.

## Request

```json
{"minAgeDays": 30}
```

| Parameter | Required | Meaning |
|---|---|---|
| `minAgeDays` | no | The number of days that a directory stays after its last use. The daemon limits the value to the range 7 to 3650. Without a value, or with a value below 7, the daemon uses 7. |
| `keep` | no | A list of strings. The daemon accepts it and does not use it. A directory whose name is in `keep` is deleted as any other. |

The daemon also accepts a request with no `params` member, and one with `"params": null`. Both use the defaults.

## Which directories the daemon deletes

The daemon derives two roots from the path of its own socket, `<X>/run/<clientId>/rpc.sock`:

- The root of this install, `<X>/plugins/<clientId>`.
- The shared root of older versions, `<X>/plugins`.

In a root, the daemon looks only at directories whose name is 16 lowercase hexadecimal characters. That name is the content hash of a plugin. It leaves each other entry alone, a file and a symbolic link included.

The daemon keeps a directory in two cases, and deletes it with all its content in each other case:

- The directory is live. A child that this daemon started with `process.spawn` has the hash in its command or its arguments. The daemon has not yet reported the exit of that child. A run of exactly 16 lowercase hexadecimal characters anywhere in the command or in an argument counts. For example, the agent CLI runs with `--plugin-dir <root>/<hash>`.
- The directory is young. Its last use is inside the last `minAgeDays` days. The last use is the newer of two times: the modification time of the directory, and that of its `.synced` file. The time of `manifest.json` does not count. If the daemon cannot read either time, the directory is not young.

The daemon always goes through the root of this install. If a daemon of another install runs, the daemon leaves the shared root alone, because the sessions of that daemon can use the shared directories. The member `legacy` says what it did.

## Response

```json
{"root": "/home/me/.claude/remote/plugins/abc", "pruned": ["0123456789abcdef"], "prunedLegacy": [], "staleArchives": 0, "kept": 0, "live": 1, "young": 2, "legacy": "swept", "minAgeDays": 30}
```

| Member | Meaning |
|---|---|
| `root` | The root of this install. |
| `pruned` | The hashes that the daemon deleted in the root of this install, sorted. |
| `prunedLegacy` | The hashes that the daemon deleted in the shared root, sorted. |
| `staleArchives` | Always `0`. |
| `kept` | Always `0`. |
| `live` | The number of live directories. It is one count over the roots that the daemon went through. |
| `young` | The number of young directories that are not live. It is one count over the roots that the daemon went through. |
| `legacy` | What the daemon did with the shared root: see the table below. |
| `skipped` | The reason, in a response of a daemon that has no plugin roots. Each other response has no `skipped` member. |
| `errors` | One text in the form `<path>: <error>` for each directory that the daemon cannot delete, and for a root that it cannot list. A root that does not exist is not a failure. A directory in `errors` can be deleted in part. A response with no such failure has no `errors` member. A response with `errors` is still a success. |
| `minAgeDays` | The value that the daemon used, after the limits. |

The values of `legacy`:

| `legacy` | Meaning |
|---|---|
| `absent` | The daemon cannot examine the shared root. For example, it does not exist. |
| `swept` | The daemon went through the shared root. |
| `skipped: another install's daemon is running (<detail>); its sessions may use the shared legacy dirs` | The daemon found a daemon of another install, or it cannot exclude one. It left the shared root alone. This test comes first, so the text does not say that the shared root exists. |
| `skipped: <reason>` with a `skipped` member | The daemon has no plugin roots. |

`<detail>` says what stopped the test. The daemon goes through the names under `<X>/run` in order and passes over each install that it can prove is gone. `<detail>` names the first install that it cannot rule out, or the run directory itself. `<rel>` is `run/<clientId>/rpc.sock`:

| `<detail>` | Meaning |
|---|---|
| `<rel> answers` | The socket accepted a connection inside 300 ms. |
| `cannot rule out <rel>: <error>` | The connection failed for a reason that does not show that no daemon listens. |
| `cannot inspect <socket path>: <error>` | The daemon cannot examine the socket path. |
| `cannot read <run directory>: <error>` | The daemon cannot list `<X>/run`. |

If the socket of the daemon is not at `run/<clientId>/rpc.sock`, the daemon has no plugin roots and deletes nothing. `root` is then `""` and `minAgeDays` is `0`. `skipped` holds the reason, and `legacy` is `skipped: ` and then the reason.

Do not read an empty `pruned` list as "nothing to delete". Read `errors` and `legacy` too.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params: <detail>` | The JSON decoder refuses `params` or a member of it. For example, a member has the wrong type, or `minAgeDays` is not a whole number. `<detail>` is the text of the JSON decoder. The other methods answer `Invalid params` with no detail. |

## Differences by system

The paths and the `<error>` texts in `legacy` and `errors` are those of the operating system. `<rel>` has `/` separators on each system. On Windows, the named-pipe transport of `-listen-pipe` does not change the roots: the daemon still has its socket, and the roots come from that path.

## Differences from the reference

claustrum is built to answer as the reference daemon does, and no entry of the divergence catalog applies to this method. `kept` and `staleArchives` are in the response because the reference has them. No measurement against the reference build `19f30c46` made either one differ from `0`. The `errors` member and the request with no `params` are from the code of claustrum. No measurement against the reference covers them.

## More detail

The measurements against the reference are in the [measurement record](../record/plugins-prune.md). Most readers do not need that page.
