# files.validate

`files.validate` says whether a path exists and whether it is a directory. It reports a path that the daemon cannot examine in the response, not as a JSON-RPC error.

claustrum is built to answer as the reference does. The error texts of this page match the reference as measured on Linux against a build up to `5fd08069`.

## Request

```json
{"path": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | The path to examine. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` and for a name that is not valid UTF-8. |

The daemon does not refuse a request without `path`. It answers as for a path that does not exist.

## Response

```json
{"valid": true, "isDir": true}
```

| Member | Meaning |
|---|---|
| `valid` | If the path exists, `true`. It does not say that the daemon can read the file or list the directory. |
| `isDir` | If the path is a directory, `true`. |
| `error` | The reason for `"valid": false`. A response with `"valid": true` has no `error` member. |

If the path does not exist:

```json
{"valid": false, "isDir": false, "error": "Path does not exist"}
```

If the daemon cannot examine the path, `error` holds the text of the operating system in the form `stat <path>: <reason>`. For example, a component of the path is a regular file (`not a directory`). See [Stat failures](../PROTOCOL.md#stat-failures-other-than-does-not-exist). The text holds `path` after the `~` expansion.

## Errors

This method has no `errorCode` member. It has one JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |

The daemon follows a symbolic link. The response then describes the target of the link. A link to a target that does not exist gives `"valid": false`.

## Platform notes

=== "Linux and macOS"
    The `error` texts are those of the example.
=== "Windows"
    The `error` texts differ.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method.
