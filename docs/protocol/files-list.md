# files.list

`files.list` lists the entries of one directory. It does not go into subdirectories.

The error texts of this page match the reference as measured on `7d193f89`.

## Request

```json
{"path": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `includeDotNames` | no | `true` lists dot names, and `false` or no member keeps them out (build `52d82f0a`, measured on Linux, macOS and Windows). Another type gets `-32602` (claustrum's own, not measured). |
| `junctionsAsDirs` | no | `true` makes a Windows junction a directory (build `683cc6a9`). Another type gets `-32602` (claustrum's own, not measured). |
| `path` | yes | The directory to list. The daemon follows a symbolic link at `path`. See [Path handling](../PROTOCOL.md#path-handling). |

## Response

```json
{"entries": [
  {"name": "README.md", "path": "/repo/README.md", "isDir": false},
  {"name": "docs", "path": "/repo/docs", "isDir": true}
]}
```

| Member | Meaning |
|---|---|
| `name` | The name of the entry. |
| `path` | `path` of the request after the `~` expansion, joined with the name. The daemon cleans the result: it removes a doubled or trailing separator and resolves `.` and `..` as text. |
| `isDir` | `true` for a directory, or for a symbolic link to one. Windows differs (platform notes). |

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | No `params`, or a wrong type in `params`. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `open <path>: no such file or directory` | The path does not exist. |
| `-32603` | `open <path>: not a directory` | The path is not a directory. |
| `-32603` | `open <path>: permission denied` | The daemon has no permission for the directory, or for a directory above it. |

The `-32603` texts come from the operating system and hold `path` after the `~` expansion. A request without `path` gets `-32603`: the empty path does not exist.

## The list

- The entries are in the byte order of their names.
- The daemon omits names that start with `.` unless `includeDotNames` is `true` (build `52d82f0a`).
- One response holds all entries, with no paging.
- An empty directory gives `"entries": []`.
- `isDir` is `false` for a dangling link or for an entry that the daemon cannot examine.

## Platform notes

On Windows each entry `path` uses `\` separators, and the `-32603` texts differ.

There (build `683cc6a9`), a file link, a link to `\\localhost\c$` and a chain over 31 links give `false`, and a junction needs `junctionsAsDirs`. A link to `\\?\UNC\localhost\c$\o\real` or to a folder of a mapped network drive gives `false` too (measured on Windows).

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method.
