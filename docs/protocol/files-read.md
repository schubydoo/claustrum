# files.read

`files.read` returns the content of one file as text. It has a size limit, and it is not for binary files.

## Request

```json
{"path": "/repo/README.md", "maxBytes": 1000000}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | The file to read. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` and for a name that is not valid UTF-8. |
| `maxBytes` | no | The size limit in bytes. Without a value, or with `0` or a negative value, the limit is `262144` (256 KiB). A positive value is the limit, and it can be above the default. |

The daemon does not refuse a request without `path`. It answers as for a file that does not exist.

## Response

```json
{"content": "# Title\n", "exists": true}
```

| Member | Meaning |
|---|---|
| `content` | The content of the file as text. It is not base64. |
| `exists` | If the daemon read a file, `true`. |

Each response has both members. If the file does not exist, the response is not an error:

```json
{"content": "", "exists": false}
```

An empty file gives `"content": ""` too. Read `exists` to tell the two apart.

The content is text. In place of each byte that is not valid UTF-8, the response holds the character U+FFFD. So the bytes of a binary file do not arrive unchanged. See [Inherited wire bytes](../ARCHITECTURE.md#inherited-wire-bytes).

## The size limit

If the file is larger than the limit, the request fails. The daemon never returns a part of a file. A file of exactly `maxBytes` bytes reads.

The daemon compares the limit with the size that the operating system reports for the open file, before the read. That size is `0` for some regular files, for example a file under `/proc`. So the limit does not apply to them. If a file grows after the comparison, the daemon reads all of it. In these cases `content` can be larger than the limit.

## Files that are not regular files

The daemon opens the path first. Then it tests the kind of the open file. It follows a symbolic link.

- A file that is not a regular file and not a directory gets `-32602 files.read: not a regular file`. Examples are a FIFO, a device such as `/dev/zero`, and on Windows `NUL` and a named pipe.
- On Linux and macOS the null device itself reads as empty content, through each path that names that file. A second device node with the same device numbers gets the refusal.
- A path that the daemon cannot open keeps the error of the open. A socket is an example. The open comes before the directory test and before the size test. So a directory or a large file that the daemon cannot open gets the error of the open too.

The open does not wait for the writer of a FIFO, so the refusal comes at once. The open is visible to a writer that waits in its own open of the FIFO: that open returns.

The reference build `5fd08069` gives these answers on Linux, macOS and Windows VMs. `89cb6289` does not refuse such a file. The flag `-files-read-regular-only` and its key in `claustrum.conf` are deprecated. They set nothing and log one warning.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `open <path>: <reason>`, for example `open <path>: permission denied` | The daemon cannot open the path, and the reason is not a missing file. See [Stat failures](../PROTOCOL.md#stat-failures-other-than-does-not-exist). |
| `-32603` | The text of the operating system | The daemon cannot get the kind of the open file. The [measurement record](../record/files-read.md) has two such paths on Windows. |
| `-32602` | `files.read: path is a directory` | The path is a directory. |
| `-32602` | `files.read: not a regular file` | The path is not a regular file, not a directory and, on Linux and macOS, not the null device. |
| `-32602` | `files.read: file exceeds maxBytes` | The file is larger than the limit. |
| `-32603` | The text of the operating system, for example `read <path>: input/output error` | The daemon cannot read the open file. |

The daemon makes the tests in the order of the table. A text with `<path>` holds `path` after the `~` expansion. The texts with `<path>` are those of Linux and macOS. On Windows the operating system gives other texts.

## Differences from the reference

claustrum is built to answer as the reference daemon does, and no entry of the divergence catalog applies to this method. [D4](../DIVERGENCES.md#d4) is retired: it was the optional refusal of a path that is not a regular file.

## More detail

The measurements of each kind of file against the reference, and the kinds that are not measured, are in the [measurement record](../record/files-read.md). Most readers do not need that page.
