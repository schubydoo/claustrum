# files.mkdir

`files.mkdir` makes one folder in an existing parent folder. It deletes nothing. Build `c9125eb8` has it on Linux and macOS (measured). Windows answers `-32601` `Unknown method: files.mkdir` (measured on Windows).

## Request

| Parameter | Meaning |
|---|---|
| `parent` | An absolute, clean path: no `.` or `..` component, no `//`, no trailing `/`, no NUL, no U+FFFD. A `~` is not expanded (claustrum's own, not measured). A `.` inside an absolute path is claustrum's own, not measured. |
| `name` | One name: not empty, no `/` or `\`, no control character, no U+2028 to U+202E, no U+FFFD, no space or `.` at the end. A space at the start is allowed. U+2029 to U+202D and most controls are claustrum's own, not measured. |

A name with the byte 0xff gets `name_refused` (measured on Linux and macOS). A new folder gets mode 0777 under the umask (measured). An extra member is ignored (measured). A `mode` member is claustrum's own, not measured.

## Response

The result is `{"code":"<code>"}`, with `"errno":<n>` after it for an OS error. Each result logs `[Files] mkdir: <code>`, and with an errno `[Files] mkdir: <code> (error <n>: <OS text>)`.

| Code | Cause |
|---|---|
| `created` | The only code that makes a folder. |
| `exists_folder`, `exists_other`, `exists_link` | The name is a folder, another kind, or a link (a dangling link and a link to a file measured on Linux only). |
| `parent_refused`, `name_refused` | The parameter test failed. The parent is tested first (claustrum's own, not measured). |
| `parent_denied` | Errno 13 from the open of the parent (mode 0300 or 0111). |
| `parent_gone` | Errno 2 (no parent, or a dangling link) or 20 (a file, or a link to one). |
| `parent_elsewhere` | The open parent has another real path: a link is at or above it (on macOS also `/tmp` and `/var/tmp`, measured). |
| `parent_unverified` | The read of that real path failed (errno 5, measured on Linux). |

### Errnos of the mkdir

| Code | Cause |
|---|---|
| `denied` | Errno 13 (a parent with mode 0555). |
| `name_too_long` | Errno 36 on Linux, 63 on macOS. |
| `read_only_fs` | Errno 30, also the parent `/` on macOS. |
| `not_permitted`, `no_space`, `over_quota`, `parent_removed` | Errno 1, 28, 122 or 2 (measured on Linux). |
| `name_unsupported` | Errno 22 or 84 (measured on Linux). |
| `failed` | Errno 5 (measured on Linux), or another errno (claustrum's own, not measured). |

A `parent` of a wrong type and absent `params` give `-32602` `Invalid params` and no log line (measured on Linux).

claustrum's own, not measured: the folder is made through the open handle of the parent, so a swapped parent cannot move it. A Linux host with no `/proc` answers `parent_unverified` to every call that opens its parent. On macOS `fcntl` `F_GETPATH` gives the real path, and the compare is exact. So a parent in another letter case or Unicode form gets `parent_elsewhere`. A parent open that fails with another errno, such as a link loop, gives `failed`.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies.
