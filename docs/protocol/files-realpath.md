# files.realpath

`files.realpath` gives the real name of a path, with every symbolic link resolved. It makes no write and no delete. Build `683cc6a9` has it.

## Request

| Parameter | Required | Meaning |
|---|---|---|
| `path` | yes | An absolute path. A leading `~` is expanded. |
| `missingLeaf` | no | If `true` and the last name is missing, the response gives `wouldBe`. |

## Response

| Member | Meaning |
|---|---|
| `exists` | If the path exists, `true`. |
| `real` | The real name, if the path exists. |
| `wouldBe` | The real name of the parent folder plus the missing last name. |
| `dangling` | With `missingLeaf`, `true` for a link to a missing target. `wouldBe` is there only if the target's parent folder exists. |

Without `missingLeaf` a dangling link gives `{"exists":false}`. `wouldBe` follows one link level and reads a relative target from the real folder of the link. All are claustrum's own, not measured.

The members come in the order of the table. A path below a missing folder gives `{"exists":false}`. With `missingLeaf` this is claustrum's own, not measured. A path below a regular file gives `{"exists":false}` (`4c111e86`, measured on Linux, and for `<file>/a` with no `missingLeaf` on macOS). With `missingLeaf` it gives the same, and a link to it `dangling` and no `wouldBe` (`4c111e86`, measured on Linux). The other macOS forms and all of Windows are claustrum's own, not measured. On Windows, `<file>\.` and `<file>\..` are cleaned as text first, so they give `exists:true` and the `real` of the file or its folder (claustrum's own, not measured). On Windows, with `missingLeaf`, a missing name behind a link to a network place gives `{"exists":false}` (claustrum's own, not measured).

### A trailing slash

A path that ends in `/` after a file, a link to a file or a FIFO gives `{"exists":false}`, also with `missingLeaf` and for `<file>//` (`4c111e86`, measured on Linux). With the `/`, a folder and a link to a folder still give the folder as `real`. A dangling link and a missing name keep the answers of their kind (measured on Linux). On macOS this is claustrum's own, not measured. On Windows the `/` is cleaned as text first (claustrum's own, not measured).

## Errors

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | Wrong type. Not measured on Windows. |
| `-32602` | `files.realpath: the path must be absolute` | The path is relative. |
| `-32602` | `files.realpath: the path contains a NUL character` | A NUL in the path. |
| `-32602` | `files.realpath: a path that starts with "~/" must have no "." or ".." component, no "//" and no trailing slash` | A `..` after `~/`. A `.` or empty component and a bare `~/` are claustrum's own, not measured. |
| `-32603` | `files.realpath: no verified path for the target (it may be deleted or moved, or on Windows on a network share)` | `data` is `{"reason":"no_usable_name"}`. On Windows, a link on the way to a mapped network drive or a `\\?\UNC\` path (measured). Other UNC forms are claustrum's own, not measured. From the code, there is no other cause. |
| `-32603` | `files.realpath: error <n>: <text>` | `data` is `{"reason","errno"}`. The reason is `too_many_links`, `no_permission` or `other`. `other` (any other errno) and `no_permission` on Windows are claustrum's own, not measured. |

## Platform notes

=== "Linux and macOS"
    A link loop gives errno 40 on Linux and 62 on macOS. A chain of 33 links on macOS (32 resolve) or 41 on Linux is over the limit. On macOS `/tmp` gives `/private/tmp`.
=== "Windows"
    Replies use `/` and a drive letter. A `..` in a link target is removed by text before the next link is followed (measured). A junction and a name below it resolve as links (`4c111e86` and `c9125eb8`, measured). A relative or empty path or `\\srv\s` gives `files.realpath: the path must begin with a drive letter, a colon and a slash`. A component ending in a dot or a space gives `files.realpath: a path component ends in a dot or a space`. A link loop gives errno 1921. Claustrum's own, not measured: that error for other forms, a passing `.` or `..` component, and a junction to a volume name acting as a folder. A `..` in a path is removed by text before the open (claustrum's own, not measured).

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies.
