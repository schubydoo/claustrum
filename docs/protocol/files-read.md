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

The daemon compares the limit with the size that the operating system reports before the read. On Linux that size is `0` for a FIFO, a socket and a device. So the limit does not apply to them. It does not apply to a regular file with a reported size of `0` either, for example a file under `/proc`. If a file grows after the comparison, the daemon reads all of it. In these cases `content` can be larger than the limit.

## Files that are not regular files

By default the daemon reads a FIFO or a device as it reads a regular file. That has two effects:

- A read of a FIFO with no writer gets no response. The response comes after a writer opened the FIFO, wrote and closed it.
- A read of a device that never ends, for example `/dev/zero`, makes the daemon grow until the operating system stops it.

The reference daemon does the same (measured on Linux). An operator can make the daemon refuse these reads. Start the daemon with the flag `-files-read-regular-only`, or set the key `files-read-regular-only` in `claustrum.conf`. A path that exists and is not a regular file or a directory then gets `-32602 files.read: not a regular file`. See [D4](../DIVERGENCES.md#d4).

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `stat <path>: <reason>` | The daemon cannot examine the path. See [Stat failures](../PROTOCOL.md#stat-failures-other-than-does-not-exist). |
| `-32602` | `files.read: path is a directory` | The path is a directory. |
| `-32602` | `files.read: not a regular file` | Only with `-files-read-regular-only`: the path is not a regular file. |
| `-32602` | `files.read: file exceeds maxBytes` | The file is larger than the limit. |
| `-32603` | The text of the operating system, for example `open <path>: permission denied` | The daemon cannot open or read the file. |

The daemon makes the tests in the order of the table. A text with `<path>` holds `path` after the `~` expansion. The texts with `<path>` are those of Linux and macOS. On Windows the operating system gives other texts.

## Differences from the reference

claustrum is built to answer as the reference daemon does. One entry of the divergence catalog applies to this method:

- [D4](../DIVERGENCES.md#d4): the optional refusal of a path that is not a regular file. It is off by default.

## More detail

The measurements of each kind of file against the reference are in the [measurement record](../record/files-read.md). Most readers do not need that page.
