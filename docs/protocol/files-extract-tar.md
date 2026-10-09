# files.extract_tar

`files.extract_tar` unpacks a gzip tar archive into a directory. It deletes the old content of that directory first, and it deletes the archive.

A failed request can leave the directory empty or partly filled. The section [After a failure](#after-a-failure) says what stays.

## Request

```json
{"archivePath": "/tmp/plugin.tar.gz", "destDir": "/home/me/.claude/remote/plugins/abc"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `archivePath` | yes | The gzip tar archive. The daemon makes no test of this path. After it opened the file, it deletes it. That applies to a file that is not an archive too. |
| `destDir` | yes | The directory to fill. The daemon deletes what is there. It must be an absolute path. It cannot be a file system root, the home directory, or a directory that holds the home directory. A directory under the home directory is accepted. The home test compares the paths as text and does not resolve symbolic links: see [D2](../DIVERGENCES.md#d2). |

[Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` in both parameters.

## What the request does

The daemon does these steps in this order:

1. It opens the archive and reads the gzip header.
2. It deletes `destDir` with all its content, and creates it again as an empty directory. If `destDir` is a file or a symbolic link, the daemon deletes that file or link. It does not delete the target of the link. Directories above `destDir` that do not exist are created.
3. It writes each entry of the archive into `destDir`.
4. It writes an empty file `.synced` at the top of `destDir`.
5. It deletes the archive. It does that after a failure too, once the archive was open.

The rules for the entries:

- The daemon writes regular files and directories only. Each other kind of entry fails the request, for example a symbolic link, a hard link or a device.
- A file gets the mode `0600` and a directory gets `0700`. On Linux and macOS the umask of the daemon applies to them. The modes in the archive do not apply. So an executable file arrives without its execute bit.
- An entry cannot leave `destDir`. A name with `..` that resolves to a place outside fails the request. A `..` that resolves to a place inside is accepted. A name that starts with `/` lands inside `destDir`.
- A file entry with the name `.synced` at the top is replaced by the empty marker file, and `fileCount` counts it. A directory entry with that name fails the request with `write .synced:`.

## Response

Success:

```json
{"success": true, "fileCount": 2}
```

`fileCount` is the number of regular files that the daemon wrote. It does not count directories, and it does not count the marker that the daemon writes.

Failure:

```json
{"success": false, "fileCount": 0, "error": "<text>"}
```

Read `success`. A failure has no `errorCode` member, so the `error` text is the one description of the cause. On a failure, do not rely on `fileCount`. Some failures give `0`, and others give the number of files written before the failure.

| `error` starts with | Cause |
|---|---|
| `destDir must be an absolute, non-root path:` | `destDir` is relative, or it is a file system root. |
| `destDir must not be or contain the home directory:` | `destDir` is the home directory, or a directory that holds it. |
| `open archive:` | The daemon cannot open the archive. For example, it does not exist. |
| `gzip:` | The gzip header is bad, or the daemon cannot read the next entry. Gzip data that is not a tar archive gives this text at the first entry. Gzip data with no content is an archive with no entries: the request succeeds with `fileCount` `0`, and `destDir` is then empty but for the marker. |
| `clean destDir:` | The daemon cannot delete the old `destDir`. |
| `mkdir destDir:` | The daemon cannot create `destDir`. |
| `unsafe path in archive: <entry>` | The entry resolves to a place outside `destDir`. |
| `unsupported tar entry type <c>: <entry>` | The entry is not a regular file or a directory. `<c>` is the type character of tar, for example `2` for a symbolic link and `1` for a hard link. |
| `mkdir parent <entry>:` | The daemon cannot create the directory for a file. For example, an earlier entry wrote a file at that place. |
| `create <entry>:` | The daemon cannot create the file. For example, a directory is at that place. |
| `extraction size limit exceeded` | Only with `-max-extract-bytes`: the files written are larger than the limit. |
| `write .synced:` | The daemon cannot write the marker file. |

The table is not complete. Two failures give a text with no prefix: the daemon cannot create the directory of a directory entry, or it cannot write the content of a file. The text is then that of the operating system or of the tar reader. For example, a cut archive can give `unexpected EOF`.

`<entry>` is the name of the entry as the archive has it, not the path on disk. `unsafe path in archive:` and `unsupported tar entry type` end with that name. The two `destDir must` texts end with the path in quotes. The text after `open archive:`, `gzip:`, `clean destDir:`, `mkdir destDir:`, `mkdir parent <entry>:`, `create <entry>:` and `write .synced:` comes from the operating system or from a library. The examples are those of Linux and macOS.

The method has two JSON-RPC errors:

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `archivePath and destDir are required` | One of the two is absent or empty. |

## After a failure

No failure restores the old content of `destDir`. The table says what stays of the archive and of `destDir`.

| Failure | The archive | `destDir` |
|---|---|---|
| A JSON-RPC error, or a refusal of `destDir` (`destDir must ...`) | Stays. | Not changed. |
| `open archive:` | Not changed. | Not changed. |
| A `gzip:` failure before the first entry | Deleted. | Not changed. |
| `clean destDir:` | Deleted. | The old content can be partly deleted. |
| `mkdir destDir:` | Deleted. | The old content is deleted. |
| Each later failure | Deleted. | The old content is deleted. The entries written before the failure stay. A file that the daemon wrote in part can stay too. The daemon writes no marker. |

`gzip:` has two rows. A `gzip:` failure at an entry, also at the first entry, is in the last row. The text alone does not say which row applies. The disk does.

Do not read a `.synced` file as the result of the last request. After `open archive:` and after a `gzip:` failure before the first entry, an old marker stays with the old content. An archive can also hold a file entry with that name, and that file stays after a later failure.

The daemon ignores a failure of the delete of the archive. So an archive that it cannot delete stays. If `archivePath` is an empty directory, the daemon on Linux deletes that directory and answers `gzip:`. The home test does not apply to `archivePath`. So if `archivePath` is the home directory and that directory is empty, the daemon deletes it.

With `-max-extract-bytes`, the daemon deletes the file that went over the limit. The files written before it stay.

## The size limit

By default there is no limit on the size of the extracted files. An operator can set one. Start the daemon with the flag `-max-extract-bytes <n>`, or set the key `max-extract-bytes` in `claustrum.conf`. The limit is on the sum of the file contents. See [D3](../DIVERGENCES.md#d3).

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| A file system root | `/` | A drive root such as `C:\`, or the root of a share such as `\\server\share\`. |
| The modes `0600` and `0700` | Applied, less the umask. | They do not limit access to the owner. |
| An absolute `destDir` | Starts with `/`. | Has a drive or a share. `\dir` and `C:dir` are relative. |
| `\` in the name of an entry | A character of the name. | A separator. |
| The home test | Letter case counts. | Letter case does not count. |

## Differences from the reference

claustrum is built to answer as the reference daemon does. These entries of the divergence catalog apply to this method:

- [D2](../DIVERGENCES.md#d2): claustrum refuses a `destDir` that is the home directory or holds it. The reference deletes the home directory for `"destDir": "~"`.
- [D3](../DIVERGENCES.md#d3): the optional size limit. It is off by default.

The refusal of a file system root is not in the catalog. claustrum refuses a root because step 2 deletes `destDir` with all its content. No measurement says what the reference does with a root, so the refusal is neither parity nor a divergence.
