# files.read

`files.read` returns the content of one file as text. It has a size limit, and it is not for binary files.

An untagged rule on this page matches the reference as measured on `5fd08069` or on an earlier build.

## Request

```json
{"path": "/repo/README.md", "maxBytes": 1000000}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | The file to read. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` and for a name that is not valid UTF-8. |
| `maxBytes` | no | The size limit in bytes. Without a value, or with `0` or a negative value, the limit is `262144` (256 KiB). A positive value is the limit, and it can be above the default. |
| `real` | no | If `true`, the response has `real`. Build `683cc6a9` has it. |

The daemon does not refuse a request without `path`. It answers as for a file that does not exist.

The daemon never returns a part of a file. A file of exactly `maxBytes` bytes reads.

## Response

```json
{"content": "# Title\n", "exists": true}
```

| Member | Meaning |
|---|---|
| `content` | The content of the file as text. It is not base64. |
| `exists` | If the daemon read a file, `true`. |
| `real` | With `"real":true`, the real name of the file, as [files.realpath](files-realpath.md) gives it. It comes after `exists`. |

If the file does not exist, the response is not an error:

```json
{"content": "", "exists": false}
```

Without `real` the member is absent (Linux, macOS and Windows, `683cc6a9`), and with `"real":false` too (macOS, `683cc6a9`). On Linux with a `/proc` that holds only `self`, `683cc6a9` gives no `real` and logs the second [start line](../DAEMON.md#daemon-log-remote-serverlog). The exact test is claustrum's own. A missing file has no `real`, a relative path gives an absolute name, and a `real` that is not a boolean gets `Invalid params`. These are claustrum's own, not measured.

An empty file gives `"content": ""` too.

The content is text. In place of each byte that is not valid UTF-8, the response holds the character U+FFFD. See [Inherited wire bytes](../ARCHITECTURE.md#inherited-wire-bytes).

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. A `real` that is not a boolean is claustrum's own, not measured. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `open <path>: <reason>`, for example `open <path>: permission denied` | The daemon cannot open the path, and the reason is not a missing file. See [Stat failures](../PROTOCOL.md#stat-failures-other-than-does-not-exist). |
| `-32603` | The text of the operating system | The daemon cannot get the kind of the open file. |
| `-32602` | `files.read: path is a directory` | The path is a directory. |
| `-32602` | `files.read: not a regular file` | The path is not a regular file, not a directory and, on Linux and macOS, not the null device. |
| `-32602` | `files.read: file exceeds maxBytes` | The file is larger than the limit. |
| `-32603` | The text of the operating system, for example `read <path>: input/output error` | The daemon cannot read the open file. |

A text with `<path>` holds `path` after the `~` expansion. The daemon makes the tests in the order of the table.

## Files that are not regular files

The daemon follows a symbolic link.

- A file that is not a regular file and not a directory gets `-32602 files.read: not a regular file`. Examples are a FIFO and a device such as `/dev/zero`.
- A path that the daemon cannot open keeps the error of the open.

## Platform notes

=== "Linux and macOS"
    The null device itself reads as empty content, through each path that names that file. A second device node with the same device numbers gets the refusal.
=== "Windows"
    `NUL` and a named pipe get the refusal. The `<path>` texts differ.

## Rare states

??? note "Size 0, growth and FIFO"
    The daemon compares the limit with the size that the operating system reports for the open file, before the read. That size is `0` for some regular files, for example a file under `/proc`, so the limit does not apply to them. If a file grows after the comparison, the daemon reads all of it.

    The open does not wait for the writer of a FIFO, so the refusal comes at once. A writer that waits in its own open of the FIFO sees the open, and that open returns.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method. The refusal of a file that is not regular is new in `5fd08069`: `89cb6289` does not refuse such a file (see [UPSTREAM-TRACKING.md](../UPSTREAM-TRACKING.md)).
