# files.extract_tar

`files.extract_tar` unpacks a gzip tar archive into a directory. It deletes the old content of that directory first, and it deletes the archive. A failed request can leave the directory empty or partly filled. [After a failure](#after-a-failure) says what stays.

An untagged rule on this page follows build `5fd08069`, and so do the example texts of the error tables. The entries under [Differences from the reference](#differences-from-the-reference) and the tags "claustrum's own", "from the code" and "not measured" flag the exceptions.

## Request

```json
{"archivePath": "/tmp/plugin.tar.gz", "destDir": "/home/me/.claude/remote/plugins/abc"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `archivePath` | yes | The gzip tar archive. The daemon makes no test of this path. After it opened the file, it deletes it, also a file that is not an archive. |
| `destDir` | yes | The directory to fill. The daemon deletes what is there. It must be an absolute path. It cannot be a file system root, the home directory, or a directory that holds the home directory. A directory under the home directory is accepted. The first home test compares the paths as text. On Linux and macOS the [test by identity](#before-the-entries) covers a link above the last name: see [D2](../DIVERGENCES.md#d2). |

[Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~` in both parameters.

## Response

```json
{"success": true, "fileCount": 2}
```

`fileCount` is the number of regular files that the daemon wrote. It counts no directory and not the marker that the daemon writes.

```json
{"success": false, "fileCount": 0, "error": "<text>"}
```

Read `success`. A failure has no `errorCode` member, so the `error` text is the one description of the cause. A failure answers `fileCount` `0`, also after the daemon wrote files, with one exception, the marker step. If the daemon cannot remove the entry at `.synced`, or cannot write the marker file, the answer holds the number of files written before (not measured).

## Errors

`<entry>` is the name of the entry as the archive has it, not the path on disk. `unsafe path in archive:` and `unsupported tar entry type` end with that name. The two `destDir must` texts end with the path in quotes. The text after each other prefix comes from the operating system or a library. The examples are those of Linux and macOS. After `mkdir parent <entry>:`, `mkdir <entry>:` and `create <entry>:` that text names the place below `destDir`, not the whole path.

### JSON-RPC errors

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `archivePath and destDir are required` | One of the two is absent or empty. |

### Before the entries

| `error` starts with | Cause |
|---|---|
| `destDir must be an absolute, non-root path:` | `destDir` is relative. Or it is a file system root (not measured). |
| `destDir must not be or contain the home directory:` | `destDir` is the home directory, or holds it. The daemon tests the text of the path first. Later it tests the directory at the last name of `destDir` by identity. |
| `open archive:` | The daemon cannot open the archive. For example, it is absent. |
| `gzip:` | The daemon cannot read a gzip header. |
| `open parent:` | The daemon cannot open or search the directory that holds `destDir`. The text is `open parent: open <path of that directory>: <reason>`. |
| `clean destDir:` | The daemon cannot delete the old `destDir`, or an entry in it. The text is `clean destDir: RemoveAll <name>: <reason>`, with the last name of `destDir` as `<name>`. |
| `mkdir parent: mkdir <path>:` | The daemon cannot create a directory above `destDir`. `<path>` is the whole path of that directory. This text has no entry name: `mkdir parent <entry>:` in the next group is another failure. |
| `mkdir destDir:` | The daemon cannot create `destDir`. The text is `mkdir destDir: mkdirat <name>: <reason>`. |
| `clean destDir: "<name>" is not the entry that the path names`, and the same after `mkdir destDir:` | The path of `destDir` and its last name inside the parent directory do not name one entry (claustrum's own, [D2](../DIVERGENCES.md#d2)). Nothing in `destDir` goes, except the new folder after `mkdir destDir:` (from the code). On Windows a path with the `\\?\` prefix and a dot or a space after its last name gives this text. |
| `open destDir:` | The daemon cannot open or search the new `destDir`. On Linux and macOS, a daemon umask of `0400` gives `open destDir: openat <name>: permission denied`, and a daemon umask of `0100` gives `open destDir: "<name>" could not be examined (statat .: permission denied)`. `<name>` is the last part of `destDir`. If another process replaced `destDir` after the daemon created it, the text is `open destDir: "<name>" changed while it was opened`, or the text can be `open destDir: openat <name>: path escapes from parent` (claustrum's own). |

### Writing the entries

| `error` starts with | Cause |
|---|---|
| `tar read:` | The daemon cannot read the next entry. Gzip data that is not a tar archive gives this text at the first entry. Gzip data with no content is an archive with no entries: the request succeeds with `fileCount` `0`, and `destDir` is empty but for the marker. |
| `unsafe path in archive: <entry>` | The name of the entry leaves `destDir`. On Windows a device name such as `NUL` and a name with `:` give this text too. |
| `unsupported tar entry type <c>: <entry>` | The entry is not a regular file or a directory. `<c>` is the type character of tar, for example `2` for a symbolic link and `1` for a hard link. |
| `mkdir parent <entry>:` | The daemon cannot create the directory for a file. A file entry `a` and then an entry `a/b` give `mkdir parent a/b: mkdirat a: file exists`. |
| `mkdir <entry>:` | The daemon cannot create the directory of a directory entry. A file entry `a` and then a directory entry `a/` give `mkdir a/: mkdirat a: file exists`. |
| `create <entry>:` | The daemon cannot create the file. A directory entry `d` and then a file entry `d` give `create d: openat d: file exists` on Linux and macOS, and `create d: openat d: is a directory` on Windows. |
| `write <entry>:` | The daemon cannot copy the content of a file. For example, the archive is cut inside the file, or its data is damaged. |
| `extraction size limit exceeded` | Only with `-max-extract-bytes`: the files written are larger than the limit. |
| `write .synced:` | The daemon cannot remove the entry at `.synced`, or it cannot write the marker file. |

## What the request does

The daemon does these steps in order:

1. It opens the archive and reads the gzip header.
2. It prepares `destDir`:
    1. It creates the directories above `destDir` that do not exist, with mode `0700`.
    2. It opens the directory that holds `destDir`.
    3. It deletes `destDir` with all its content. If `destDir` is a file or a symbolic link, the daemon deletes that file or link. It does not delete the target of a link, at `destDir` or inside it.
    4. It creates `destDir` again as a new empty directory with mode `0700`.
    5. It opens `destDir`.
3. It writes each entry of the archive into `destDir`.
4. It deletes what the archive put at `.synced` at the top of `destDir`. Then it writes an empty file `.synced` there.
5. It deletes the archive. It does that after a failure too, once the archive was open.

- The daemon writes regular files and directories only. Each other kind of entry fails the request, for example a symbolic link, a hard link or a device.
- A directory gets the mode `0700`. If the mode of a file in the archive has the execute bit of the owner, the file gets `0700`. If it does not, the file gets `0600`. No other mode bit of the archive applies, so no setuid, setgid or sticky bit arrives. On Linux and macOS the daemon umask applies to these modes.
- A second file entry with the name of an earlier one replaces that file. The new file has the mode of the last entry (Linux and macOS). On a volume that ignores letter case it has the name of the last entry too (Windows and macOS). `fileCount` counts each entry.
- An entry cannot leave `destDir`. If a name leaves `destDir` at a step, the request fails, also when the name comes back. `../evil` and `../dest/x.txt` are examples. A `..` that stays inside, as in `a/../b.txt`, is accepted. A name that starts with `/` lands inside `destDir`.
- The marker replaces what the archive put at `.synced` at the top. A file entry with that name ends empty with mode `0600`. A directory entry with that name goes with all its content. `fileCount` still counts the files of the archive there. A `.synced` below another directory is a normal entry.

## After a failure

No failure restores the old content of `destDir`.

| Failure | The archive | `destDir` |
|---|---|---|
| A JSON-RPC error, or a refusal of `destDir` (`destDir must ...`) | Stays. | Not changed. |
| The home refusal from the test by identity | Deleted. | Not changed. |
| `open archive:` | Not changed. | Not changed. |
| `gzip:` | Deleted. | Not changed. |
| `open parent:` | Deleted. | Not changed. Directories above `destDir` that did not exist can be new. |
| `clean destDir:` | Deleted. | The old `destDir` stays. The entries that the daemon was able to delete are gone. With the text `is not the entry that the path names`, nothing is deleted. |
| `mkdir parent: mkdir <path>:` | Deleted. | Not changed. Directories above `destDir` that did not exist can be new (from the code). |
| `mkdir destDir:` | Deleted. | The old content is deleted, and there is no `destDir`. |
| `open destDir:` | Deleted. | The old content is deleted. `destDir` is empty. |
| Each later failure | Deleted. | The old content is deleted. The entries written before the failure stay. A file that the daemon wrote in part can stay too. The daemon writes no marker. |

Do not read `.synced` as the result of the last request. After `open archive:` or `gzip:`, an old marker stays with the old content. An archive can hold an entry with that name, and that entry stays after a later failure.

The daemon ignores a failure of the delete of the archive. So an archive that it cannot delete stays. With `-max-extract-bytes`, the daemon deletes the file that went over the limit. The files written before it stay.

## The size limit

By default there is no limit on the size of the extracted files. An operator sets one with the flag `-max-extract-bytes <n>` or the key `max-extract-bytes` in `claustrum.conf`. The limit is on the sum of the file contents. See [D3](../DIVERGENCES.md#d3).

## Differences by system

=== "Linux and macOS"

    - File system root: `/`.
    - The modes `0600` and `0700` apply, less the umask.
    - An absolute `destDir` starts with `/`.
    - `\` in an entry name is a character of the name.
    - The home test counts letter case.
    - A last name of `destDir` that ends in a dot or a space, with no `\\?\` prefix, is a plain name (from the code). So are the last names `NUL`, `a:b`, `CON`, `COM1`, `dest?` and `dest*` (from the code).

=== "Windows"

    - File system root: a drive root such as `C:\`, or a share root such as `\\server\share\`.
    - The modes `0600` and `0700` do not limit access to the owner.
    - An absolute `destDir` has a drive or a share. `\dir` and `C:dir` are relative.
    - `\` in an entry name is a separator.
    - The home test ignores letter case.
    - A last name of `destDir` that ends in a dot or a space, with no `\\?\` prefix: `destDir` is wiped and filled (measured on Windows).
    - The last names `NUL`, `a:b`, `CON`, `COM1`, `dest?` and `dest*` give `clean destDir: RemoveAll <name>: …`, and `destDir` does not change (measured on Windows).

## Rare states

??? note "An archive path that is a directory"
    If `archivePath` is an empty directory, the daemon on Linux deletes that directory and answers `gzip:`. The home test does not apply to `archivePath`. If `archivePath` is the home directory and it is empty, the daemon deletes it.

## Differences from the reference

- [D2](../DIVERGENCES.md#d2): claustrum refuses a `destDir` that is the home directory or holds it. The reference deletes the home directory for `"destDir": "~"`.
- [D3](../DIVERGENCES.md#d3): the optional size limit. It is off by default.

The test for a replaced `destDir` and the test of the two views of `destDir` before the delete are claustrum's own, and both are part of [D2](../DIVERGENCES.md#d2).

The refusal of a file system root is not in the catalog. claustrum refuses a root because step 2 deletes `destDir` with all its content. No measurement says what the reference does with a root, so the refusal is neither parity nor a divergence.
