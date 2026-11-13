# files.find

`files.find` walks up to 8 folders and lists the regular files whose path ends with a suffix. It reads only. A reply that is not whole names the cut that the walk met.

An untagged rule on this page matches build `4c111e86`, measured on Linux and macOS. Windows answers `-32601` `Unknown method: files.find` (measured on Windows).

## Request

| Parameter | Meaning |
|---|---|
| `roots` | 1 to 8 absolute, clean paths: not empty, no `.` or `..` component, no `//`, no trailing `/`, no NUL. A `~` is not expanded (claustrum's own, not measured). |
| `suffix` | 1 to 8 names joined by `/`: no empty, `.` or `..` name, no NUL. If the last names of a file equal the suffix, byte for byte, the file matches (byte for byte measured on Linux). |
| `limit` | The most paths in the reply. Absent or 0 gives 5, and a value above 20 gives 20. |
| `timeoutMs` | The time bound of the walk. Absent or 0 gives 1500, and a value above 5000 gives 5000 (measured on Linux). |

An extra member is ignored. A member sent twice keeps its last value (measured on Linux).

## Response

```json
{"paths":["/w/a/.claude/settings.json"],"whole":false,"cut":"links","passedOver":["/w/node_modules"],"links":[{"path":"/w/b","leadsTo":"other"}]}
```

| Member | Meaning |
|---|---|
| `paths` | The regular files that match. |
| `whole` | If the walk met no cut, `true`. |
| `cut` | If `whole` is `false`, the cut word that the walk met. Else the member is absent. |
| `passedOver` | The folders that the walk did not enter: `.git`, `.hg`, `.svn`, `node_modules`, and a folder that holds a regular file `pyvenv.cfg`. A file `.git` and a folder `pyvenv.cfg` do not count (measured on Linux). A root is never passed over. |
| `links` | Up to 20 links that the walk met (measured on Linux). A link is never followed. |

If two roots overlap, each list still holds a path once. Each list sorts by the number of names in the path, then by bytes. The walk is depth first, as inferred from the rows, so a bound keeps the entries that it met first. The order inside a folder is the order of the file system (claustrum's own, not measured).

## Errors

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | `params` is absent or is no object. A member has a wrong type, or `limit` is no integer. `limit` or `timeoutMs` is negative. A root or the suffix breaks the rules of [Request](#request). |

## Behavior

### Cuts before the walk

| Cut | Cause |
|---|---|
| `name` | The suffix holds a control character, U+200F, U+202E, U+2028 or U+FFFD, or a space or U+2800 at its start or end. Nothing is walked. |
| `busy` | 4 walks run already (measured on Linux). Nothing is walked. |
| `home` | macOS only, see [Platform notes](#platform-notes). The root is skipped. |
| `root` | The root is missing, is no folder, or has a link at or above it. The root is skipped. |

### Cuts during the walk

| Cut | Cause |
|---|---|
| `count` | The walk found one match more than `limit`. The walk ends. |
| `not_a_file` | A match is a folder or a link, or a FIFO (measured on Linux). Another kind that is no regular file gives it too (claustrum's own). |
| `unreadable` | A folder did not open. The walk goes on. |
| `depth` | A folder lies more than 12 levels below its root, so it is not read. |
| `entries` | The walk read too many entries. The rows put the bound between 49000 and 51000 entries (measured on Linux). The walk ends. |
| `changed` | The entries of a folder changed while the walk read it (measured on Linux). |
| `time` | The time bound passed. The walk ends. If a folder read blocks, the reply comes at the bound, and the walk keeps its slot until it ends (measured on Linux). How claustrum does it is claustrum's own. |
| `many_links` | The walk met no other cut and more than 20 links. The walk goes on past the 20 (measured on Linux). |
| `links` | The walk met no other cut, and `links` holds 1 to 20 links (measured on Linux). |

### Order of the cuts

A reply gives `not_a_file` over `links` and `many_links`, and `unreadable` over `links` (measured on Linux). A walk with 300 matching links lists 20 (measured on Linux).

### Links

A relative link target that stays in the root leads to `folder` for a folder. For a regular file the link is left out of the list. Every other link leads to `other`: an absolute target, a missing target and a target whose last name is a link. These link rules are measured on Linux, and an absolute target on macOS too. The in-root test of a target and a relative target out of the root are claustrum's own, not measured.

## Platform notes

=== "Linux"
    A root equal to the daemon's `HOME`, its parent and `/` are walked, with no `home` cut.

=== "macOS"
    A root that is home or holds home gets `home`, also `/System/Volumes/Data`. A root below `/System/Volumes/Data` gets `root`, as does a root through the `/tmp` link.

=== "Windows"
    There is no `files.find`, and `server.capabilities` does not list it.

## Rare states

??? note "Choices that no row settles"
    These rules are claustrum's own, not measured. A walk that finds exactly `limit` matches stays whole. The first cut that the walk meets wins, and `links` and `many_links` come last. The entry bound is exactly 50000 entries. A change is found by the stamps of the open folder, and the walk goes on after `changed`. A link that is left out does not count toward the 20. The pass test comes before the depth test. A link to a folder alone gives the cut `links`. A root that fails to open for another reason gets `root`. Other format characters, U+2029, other blanks and a blank at the edge of an inner name give `name`. The suffix is matched against the whole path, so it can reach above the root. A full walk slot comes after the `name` test. A reply at the bound keeps a cut that the walk met before it.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies.
