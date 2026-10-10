# files.extract_tar

`files.extract_tar` unpacks a gzip tar archive into a directory. It deletes the old content of that directory first, and it deletes the archive.

A failed request can leave the directory empty or partly filled. The section [After a failure](#after-a-failure) says what stays.

The measurements behind the modes and the texts of `5fd08069` are in the [record](../record/files-extract-tar.md).

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
4. It deletes what the archive put at `.synced` at the top of `destDir`. Then it writes an empty file `.synced` there.
5. It deletes the archive. It does that after a failure too, once the archive was open.

The rules for the entries:

- The daemon writes regular files and directories only. Each other kind of entry fails the request, for example a symbolic link, a hard link or a device.
- A directory gets the mode `0700`. A file gets `0700` if its mode in the archive has the execute bit of the owner, and `0600` if it does not. No other mode bit of the archive applies, so no setuid, setgid or sticky bit arrives. On Linux and macOS the umask of the daemon applies to these modes.
- A second file entry with the name of an earlier one replaces that file. The new file has the mode of the last entry (Linux and macOS). On a volume that ignores letter case it has the name of the last entry too (Windows and macOS). `fileCount` counts each entry.
- An entry cannot leave `destDir`. A name that leaves `destDir` at a step fails the request, also when it comes back. `../evil` and `../dest/x.txt` are examples. A `..` that stays inside is accepted, for example `a/../b.txt`. A name that starts with `/` lands inside `destDir`.
- The marker replaces what the archive put at `.synced` at the top. A file entry with that name ends empty with mode `0600`. A directory entry with that name goes with all its content. `fileCount` still counts the files of the archive there. A `.synced` below another directory is a normal entry.

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

Read `success`. A failure has no `errorCode` member, so the `error` text is the one description of the cause. A failure answers `fileCount` `0`, also after the daemon wrote files, with one exception. The exception is the marker step: the daemon cannot remove the entry at `.synced`, or it cannot write the marker file. The answer then holds the number of files written before. No measurement of the reference covers that case.

| `error` starts with | Cause |
|---|---|
| `destDir must be an absolute, non-root path:` | `destDir` is relative, or it is a file system root. |
| `destDir must not be or contain the home directory:` | `destDir` is the home directory, or a directory that holds it. |
| `open archive:` | The daemon cannot open the archive. For example, it does not exist. |
| `gzip:` | The daemon cannot read a gzip header. |
| `tar read:` | The daemon cannot read the next entry. Gzip data that is not a tar archive gives this text at the first entry. Gzip data with no content is an archive with no entries: the request succeeds with `fileCount` `0`, and `destDir` is then empty but for the marker. |
| `clean destDir:` | The daemon cannot delete the old `destDir`. |
| `mkdir destDir:` | The daemon cannot create `destDir`. |
| `open parent:` | The daemon cannot open the folder that holds `destDir`. |
| `open destDir:` | The daemon cannot open the new `destDir`, or cannot search it. A daemon umask of `0400` gives `open destDir: openat <name>: permission denied` on Linux and macOS. On Linux and macOS a daemon umask of `0100` gives `open destDir: "<name>" could not be examined (statat .: permission denied)`. `<name>` is the last part of `destDir`. If another process replaced `destDir` after the daemon created it, the text is `open destDir: "<name>" changed while it was opened`, or `open destDir: openat <name>: path escapes from parent`. |
| `unsafe path in archive: <entry>` | The name of the entry leaves `destDir`. On Windows a device name such as `NUL` and a name with `:` give this text too. |
| `unsupported tar entry type <c>: <entry>` | The entry is not a regular file or a directory. `<c>` is the type character of tar, for example `2` for a symbolic link and `1` for a hard link. |
| `mkdir parent <entry>:` | The daemon cannot create the directory for a file. A file entry `a` and then an entry `a/b` give `mkdir parent a/b: mkdirat a: file exists`. |
| `mkdir <entry>:` | The daemon cannot create the directory of a directory entry. A file entry `a` and then a directory entry `a/` give `mkdir a/: mkdirat a: file exists`. |
| `create <entry>:` | The daemon cannot create the file. A directory entry `d` and then a file entry `d` give `create d: openat d: file exists` on Linux and macOS, and `create d: openat d: is a directory` on Windows. |
| `write <entry>:` | The daemon cannot copy the content of a file. For example, the archive is cut inside the file, or its compressed data is damaged. |
| `extraction size limit exceeded` | Only with `-max-extract-bytes`: the files written are larger than the limit. |
| `write .synced:` | The daemon cannot remove the entry at `.synced`, or it cannot write the marker file. |

`<entry>` is the name of the entry as the archive has it, not the path on disk. `unsafe path in archive:` and `unsupported tar entry type` end with that name. The two `destDir must` texts end with the path in quotes. The text after each other prefix comes from the operating system or from a library. The examples are those of Linux and macOS. After `mkdir parent <entry>:`, `mkdir <entry>:` and `create <entry>:` that text names the place below `destDir`, not the whole path.

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
| `gzip:` | Deleted. | Not changed. |
| `clean destDir:` | Deleted. | The old content can be partly deleted. |
| `mkdir destDir:` | Deleted. | The old content is deleted. |
| `open parent:` or `open destDir:` | Deleted. | The old content is deleted. `destDir` is empty. |
| Each later failure | Deleted. | The old content is deleted. The entries written before the failure stay. A file that the daemon wrote in part can stay too. The daemon writes no marker. |

Do not read a `.synced` file as the result of the last request. After `open archive:` and after `gzip:`, an old marker stays with the old content. An archive can also hold an entry with that name, and that entry stays after a later failure.

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

The reference pin is `89cb6289`. This method follows `5fd08069`. The examples in the table of the `error` texts are from `5fd08069` too. The [record](../record/files-extract-tar.md) has the rows of both builds.

In the failures of the delete and of the create of `destDir` claustrum keeps its own text. The record lists them under [Texts that claustrum keeps](../record/files-extract-tar.md#texts-that-claustrum-keeps). In one failure of the open the text is equal and the disk differs: see the section before it.

The test for a replaced `destDir` is claustrum's own, and it is part of [D2](../DIVERGENCES.md#d2). No measurement says what the reference does there.

The refusal of a file system root is not in the catalog. claustrum refuses a root because step 2 deletes `destDir` with all its content. No measurement says what the reference does with a root, so the refusal is neither parity nor a divergence.
