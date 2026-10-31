# files.realpath

`files.realpath` gives the real name of a path, with every symbolic link resolved. It makes no write and no delete. Build `683cc6a9` has it.

## Request

| Parameter | Required | Meaning |
|---|---|---|
| `path` | yes | An absolute path. A leading `~` is expanded. |
| `missingLeaf` | no | If `true` and the last name does not exist, the response gives `wouldBe`. |

## Response

| Member | Meaning |
|---|---|
| `exists` | If the path exists, `true`. |
| `real` | The real name, if the path exists. |
| `wouldBe` | With `missingLeaf`, the real name of the parent folder plus the missing last name. |
| `dangling` | `true` for a link whose target does not exist, with `missingLeaf`. `wouldBe` is there only if the parent folder of the target exists. |

Without `missingLeaf` a dangling link gives `{"exists":false}`. `wouldBe` follows one link level, and a relative target is read from the real folder of the link. All are claustrum's own, not measured.

The members come in the order of the table. A path below a missing folder gives `{"exists":false}`. With `missingLeaf` this is claustrum's own, not measured. On Windows, with `missingLeaf`, a missing name behind a link to a network place gives `{"exists":false}` (claustrum's own, not measured).

## Errors

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | Wrong type. Not measured on Windows. |
| `-32602` | `files.realpath: the path must be absolute` | The path is relative. |
| `-32602` | `files.realpath: the path contains a NUL character` | A NUL in the path. |
| `-32602` | `files.realpath: a path that starts with "~/" must have no "." or ".." component, no "//" and no trailing slash` | A `..` after `~/`. A `.` or empty component and a bare `~/` are claustrum's own, not measured. |
| `-32603` | `files.realpath: no verified path for the target (it may be deleted or moved, or on Windows on a network share)` | `data` is `{"reason":"no_usable_name"}`. On Windows, a link on the way whose target is on a mapped network drive or is a `\\?\UNC\` path (measured on Windows). Any other UNC form is claustrum's own, not measured. From the code, claustrum gives it for no other cause. |
| `-32603` | `files.realpath: error <n>: <text>` | `data` is `{"reason","errno"}`. The reason is `too_many_links`, `no_permission` or `other`. `other` (any other errno) and `no_permission` on Windows are claustrum's own, not measured. |

## Platform notes

=== "Linux and macOS"
    A link loop gives errno 40 on Linux and 62 on macOS. A chain of 33 links on macOS (32 resolve) and of 41 links on Linux is over the limit. On macOS `/tmp` gives `/private/tmp`.
=== "Windows"
    Replies use `/` and a drive letter. A `..` in a path is removed by text before the path is opened (claustrum's own, not measured). A `..` inside a link target is removed by text before the next link is followed (measured on Windows). A relative path, an empty path or `\\srv\s` gives `files.realpath: the path must begin with a drive letter, a colon and a slash`. Other forms are claustrum's own, not measured. A component that ends in a dot or a space gives `files.realpath: a path component ends in a dot or a space`. A `.` or `..` component passes (claustrum's own, not measured). A link loop gives errno 1921.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies.
