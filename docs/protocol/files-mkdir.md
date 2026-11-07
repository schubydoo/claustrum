# files.mkdir

`files.mkdir` makes one folder in an existing parent folder. It deletes nothing. Build `c9125eb8` has it on Linux and macOS (measured). Windows answers `-32601` `Unknown method: files.mkdir` (measured on Windows).

## Request

| Parameter | Meaning |
|---|---|
| `parent` | An absolute, clean path: no `.` or `..` component, no `//`, no trailing `/`, no NUL. A `~` is not expanded (claustrum's own, not measured). A `.` inside an absolute path is claustrum's own, not measured. |
| `name` | One name: not empty, not `.` or `..`, no `/`, no NUL, no space at the start or the end. A space at one end only is claustrum's own, not measured. |

A new folder gets mode 0755 under the umask. An extra member is ignored (measured). A `mode` member and the mode under another umask are claustrum's own, not measured.

## Response

The result is `{"code":"<code>"}`, with `"errno":<n>` after it for an OS error. Each result logs `[Files] mkdir: <code>`, and with an errno `[Files] mkdir: <code> (error <n>: <OS text>)`.

| Code | Cause |
|---|---|
| `created` | The only code that makes a folder. |
| `exists_folder`, `exists_other`, `exists_link` | The name is a folder, another kind, or a link (a dangling link and a link to a file measured on Linux only). |
| `parent_refused`, `name_refused` | The parameter test failed. The parent is tested first (claustrum's own, not measured). |
| `parent_elsewhere` | The parent is a link. |
| `parent_gone` | Errno 2 (no parent) or 20 (the parent is a file, measured on Linux). |
| `name_too_long` | Errno 36 from the mkdir of the name (measured on Linux). On macOS claustrum sends 63 (claustrum's own, not measured). |
| `denied` | Errno 13 (a parent with mode 0555). |

A `parent` of a wrong type and absent `params` give `-32602` `Invalid params` and no log line (measured on Linux).

claustrum's own, not measured: the folder is made through a handle of the parent that follows no link, so a swapped parent cannot move it. A link above the parent is followed. A parent that does not open for reading gives `denied`. Any other errno, also 36 from the open of the parent, gives `-32603` `files.mkdir: error <n>: <OS text>` and logs `[Files] mkdir: failed (error <n>: <OS text>)`.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies.
