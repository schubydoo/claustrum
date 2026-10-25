# files.stat

`files.stat` says whether a path exists. If it exists, the response gives its kind, its size and its mode.

claustrum is built to answer as the reference does. The error texts of this page match the reference as measured on Linux against a build up to `5fd08069`.

## Request

```json
{"path": "/repo/README.md"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | The path to examine. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` and for a name that is not valid UTF-8. |

The daemon does not refuse a request without `path`. It answers as for a path that does not exist.

## Response

```json
{"exists": true, "isDir": false, "size": 1024, "mode": "-rw-r--r--"}
```

| Member | Meaning |
|---|---|
| `exists` | If the path exists, `true`. |
| `isDir` | If the path is a directory, `true`. |
| `size` | The size in bytes of a regular file. For a directory or another kind of file, the value comes from the operating system. Do not rely on it. |
| `mode` | The kind and the permissions as text, for example `-rw-r--r--` for a file and `drwxr-xr-x` for a directory. The text has the form of the Go type `fs.FileMode`, not of `ls`. For example, a sticky directory gives `dtrwxrwxrwx`. |

Each response has all four members. If the path does not exist, the response is not an error:

```json
{"exists": false, "isDir": false, "size": 0, "mode": ""}
```

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `stat <path>: <reason>` | The daemon cannot examine the path. For example, a component of the path is a regular file (`not a directory`), or a name is too long (`file name too long`). See [Stat failures](../PROTOCOL.md#stat-failures-other-than-does-not-exist). |

The text holds `path` after the `~` expansion.

The daemon follows a symbolic link. The response describes the target, and no member says that the path is a link. A link to a target that does not exist gives `"exists": false`.

## Platform notes

=== "Linux and macOS"
    The `<reason>` texts are those of the table.
=== "Windows"
    The permissions in `mode` do not show the access control list of the file. The `<reason>` texts differ.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method.
