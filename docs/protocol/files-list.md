# files.list

`files.list` lists the entries of one directory. It does not list hidden entries, and it does not go into subdirectories.

## Request

```json
{"path": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | yes | The directory to list. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` and for a name that is not valid UTF-8. |

The daemon follows a symbolic link at `path`.

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
| `path` | `path` of the request, joined with the name. The daemon cleans the result: it removes a doubled or trailing separator and resolves `.` and `..` as text. On Windows the separators of the result are `\`. |
| `isDir` | If the entry is a directory or a symbolic link to a directory, `true`. |

The rules of the list:

- The entries are in the byte order of their names. So `B` comes before `a`.
- The daemon omits each entry whose name starts with `.`, for example `.git` and `.env`. No parameter includes them.
- One response holds all the entries. There is no limit and no paging.
- An empty directory gives `"entries": []`.
- `isDir` is `false` for a symbolic link to a target that does not exist. It is also `false` for an entry that the daemon cannot examine.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `open <path>: no such file or directory` | The path does not exist. |
| `-32603` | `open <path>: not a directory` | The path is not a directory. |
| `-32603` | `open <path>: permission denied` | The daemon has no permission for the directory, or for a directory above it. |

The `-32603` texts come from the operating system, and the table gives those of Linux. On Windows the texts are different. The text holds `path` after the `~` expansion. A request without `path` gets a `-32603` error too, because the empty path does not exist.

## Differences from the reference

claustrum is built to answer as the reference daemon does. One entry of the divergence catalog applies to this method:

- [D9](../DIVERGENCES.md#d9): a `maxBytes` member of the wrong type gets `-32602`, although this method does not use `maxBytes`. The entry has the measured case, on `files.stat`.
