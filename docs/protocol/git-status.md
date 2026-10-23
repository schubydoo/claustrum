# git.status

`git.status` reports the changes in a linked git worktree of a session. If `path` is a linked worktree that `baseRepo` registered, it gives the status. Every other request gets `"isRepo": false`. The daemon runs git on a temporary copy of the git files of the worktree. So the status never rewrites the index of the worktree and never takes `index.lock`.

## Request

```json
{"path": "/repo/.claude/worktrees/w1", "baseRepo": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | The linked worktree. Send an absolute path. Without `path`, the daemon answers for its own working directory. A relative `path` gives `"isRepo": false` or `-32603 exit status 128`. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |
| `baseRepo` | yes | The repository that owns the worktree. |
| `shadowDirOff` | no | `true` turns off, for this request, the daemon's own read of the git config of the repository and [the settings copy](../PROTOCOL.md#the-settings-copy). A git request that starts while such a request runs gets the same. Any other value does nothing and is no error. |

## Response

A clean worktree:

```json
{"isRepo": true, "clean": true}
```

A worktree with changes:

```json
{"isRepo": true, "clean": false, "changes": ["M  a.txt", " M b.txt", "?? new"]}
```

| Member | Meaning |
|---|---|
| `isRepo` | If `path` is a linked worktree of `baseRepo`, `true`. |
| `clean` | If `changes` is empty, `true`. Else `false`. |
| `changes` | The entries of the status, in the order of [The changes](#the-changes). If there is no entry, the member is absent. |

If the request does not name a linked worktree of `baseRepo`, the response is not an error. It has both members:

```json
{"isRepo": false, "clean": false}
```

That holds for a `path` that is the repository itself, a plain folder or a subfolder of the repository. It holds for a nested repository, a missing folder and a regular file. It also holds for a worktree of another repository, and for a right worktree named with the wrong `baseRepo`. [How the daemon finds the worktree](#how-the-daemon-finds-the-worktree) has the rules.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error. A failure changes nothing in the repository or the worktree.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `baseRepo is required` | `baseRepo` is absent or empty. |
| `-32603` | A refusal text | The daemon refuses to run git for `baseRepo`. It does not trust the git directory, it refuses its own git environment, or the listing of the git config fails. The texts are in [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check), [Hardened git calls](../PROTOCOL.md#hardened-git-calls) and [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment). |
| `-32603` | `this repository's git config cannot be used: …` or `a file in this repository's .git directory cannot be used: …` | The daemon read the git config of the repository itself and does not accept it. `"shadowDirOff": true` turns that read off. See [Repository config refusals](../PROTOCOL.md#repository-config-refusals). |
| `-32603` | `Claude could not test git on the remote machine (…)`, `Claude could not prepare its own files on the remote machine: …` or `git could not check this repository on the remote machine: …` | A step of the settings copy failed. See [The settings copy](../PROTOCOL.md#the-settings-copy). |
| `-32603` | The Go error of the git call, for example `exit status 128` | `git status`, `git ls-files` or `git diff-index` failed. The text of git does not show. A relative `path` that matches a registration gets `exit status 128`, because git takes it as sent. |
| `-32603` | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: ` and the start error, for example `chdir <path>: no such file or directory` | `path` passed the test as a cleaned path, but git cannot start in the path as sent. An example is `<dir>/missing/..` outside `baseRepo`. |
| `-32603` | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | A git call ran 2 minutes, and the daemon stopped it. See [Time limits of git calls](../PROTOCOL.md#time-limits-of-git-calls). |
| `-32603` | `<file>: not a regular file`, `<file> is larger than <n> bytes`, or an error of the file system | claustrum's own texts. A file of the worktree registration changed after the test, or the daemon cannot copy it. The texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue). The reference is not measured there. |

The daemon makes the temporary git folder in its own run folder. If it cannot make that folder there, the response is `{"isRepo":false,"clean":false}`. With `"shadowDirOff":true` the folder is in the temporary directory of the system, and a failure there answers `-32603` with the error of the file system.

## How the daemon finds the worktree

The daemon does not ask git inside `path` to decide. It reads the registrations of the repository. A registration is the folder `<common>/worktrees/<name>` that git keeps for each linked worktree. `<common>` is the git directory that `git rev-parse --absolute-git-dir` prints in `baseRepo`.

### `baseRepo`

Each of these gives `"isRepo": false` before any git call:

- `baseRepo` has the components `.claude/worktrees` in it, or is that folder. `<dir>/.claude` alone passes.
- A folder one or two levels above `baseRepo` holds an entry named `.claude-managed-worktrees` and no `.git`. A marker inside `baseRepo` itself does not count.
- `baseRepo` does not resolve. For example, it does not exist, or a `..` after a symbolic link goes up from the target of the link and finds no folder.

Then the [git-directory trust check](../PROTOCOL.md#git-directory-trust-check) runs on `baseRepo`, not on `path`. A refusal answers `-32603`. That is so for a good linked worktree as `path` too. If the check finds no repository, the answer is `"isRepo": false`. A config of `baseRepo` that the daemon does not accept answers `-32603`, as in [Repository config refusals](../PROTOCOL.md#repository-config-refusals).

`baseRepo` can be a symbolic link to the repository, a bare repository, a submodule checkout, or a subfolder of a repository. If git cannot start in `baseRepo` or prints no git directory, the answer is `"isRepo": false`. So is a repository with no `worktrees` folder.

### `path`

Each of these gives `"isRepo": false`:

- `path` is not a folder, or it does not exist.
- `path` lies inside `baseRepo`, and a component below `baseRepo` is a symbolic link. That includes the last component and a link at `.claude`.
- `path` lies inside `baseRepo` and has a `..` component. A `.` component passes. Outside `baseRepo` a `..` passes.

The `.git` file inside `path` plays no part. If it is missing, damaged or names another place, the answer is still the status. A locked worktree answers the status too.

### The registration match

Exactly one registration must name `path`. Two that match give `"isRepo": false`, and so does none.

- A registration is a real folder. A symbolic link under `worktrees` does not count.
- Its `gitdir` file must be a regular file of at most 1 MiB. Its value must end in `.git`. A relative value counts from the registration folder.
- The folder before `.git` must equal `path` as text. Two spellings of `path` match: as sent, and with its symbolic links resolved. For a `path` inside `baseRepo`, the second spelling is the resolved `baseRepo` joined with the rest of `path` as sent.
- On macOS another letter case, the NFD form and `/System/Volumes/Data` do not match.

The matched registration then passes these tests. Each failure gives `"isRepo": false`.

- `commondir` must be a regular file, or a relative symbolic link to a file in the registration folder. Its value must equal `<common>` as text, in the spelling that git printed. On macOS a value under `/tmp` fails, because git prints `/private/tmp`.
- `HEAD` must hold 40 or 64 hex characters, or a `ref:` line under `refs/`. A branch that does not exist and a detached commit pass.
- `index` must be a regular file of at most 1 GiB, or be missing. The `index` and all `sharedindex.*` files together must hold at most 2 GiB.
- `config.worktree` can be missing. If it exists, it must parse and hold only `core.sparseCheckout`, `core.sparseCheckoutCone` and `index.sparse`. `user.name` and `core.bare` give `"isRepo": false`. claustrum refuses every other key too (not measured).
- A repository can have the reftable format. Then a `tables.list` in the `reftable` folder must name at least one table. Each table must be a regular file. If `reftable` is not a real folder, or `tables.list` is a FIFO, the request goes on. git then shows every tracked file as added.

No test waits on a FIFO. A FIFO in place of one of these files fails at once.

## What the status reads

The temporary git folder holds copies of `HEAD`, `index`, `config.worktree` and `info/sparse-checkout` of the registration. It also holds the `sharedindex.*` files with hex names, the reftable files and a `commondir` file. The daemon removes the folder before it replies.

The daemon runs four git commands in `path`, with that folder as the git directory and `path` as sent as the work tree:

- `status --porcelain --untracked-files=all --ignore-submodules=all`
- `ls-files -s -z`
- `rev-parse --verify -q HEAD^{commit}`
- `diff-index --cached --raw -z --ignore-submodules=none HEAD --`

If `HEAD` names no commit, `diff-index` compares with the empty tree.

- The calls ignore the `.gitattributes` files of the repository, so no clean filter runs. A git older than 2.40 has no option for that, and then the attributes apply.
- The calls do not read replace objects (`refs/replace`). The status is that of the real commits.
- `status`, `ls-files` and `diff-index` carry `GIT_OPTIONAL_LOCKS=0`. All calls run as [hardened git calls](../PROTOCOL.md#hardened-git-calls).
- If the user has a global excludes file, the status uses it.

## The changes

`changes` holds three groups, in this order.

1. The lines of `git status --porcelain`, verbatim, minus the line end. Warnings on stderr never appear. The XY column is positional, so the leading space of an unstaged change is data: `"M  a.txt"` is staged and `" M b.txt"` is unstaged. An untracked file in an untracked folder has its own entry, as `"?? sub/u.txt"`.
2. One entry for each gitlink in the index, unless `<name>/.git` does not exist. A gitlink is an index entry of mode `160000`. There is one entry for each index stage, so a gitlink in conflict gives three.
3. One entry for each staged change whose old or new mode is `160000`.

A submodule entry is ` S `, the quoted name and one of three texts:

| Text | Meaning |
|---|---|
| ` (submodule present; contents not inspected)` | `<name>/.git` exists, as a file, a folder or a dangling symbolic link. A gitlink path that is a symbolic link to a folder inside the work tree counts as present. |
| ` (submodule; could not be inspected)` | The daemon cannot look at `<name>/.git`. Examples are a gitlink path that is a regular file, a symbolic link out of the work tree, or a folder of mode 000. |
| ` (submodule change staged; contents not inspected)` | A staged change of a gitlink. |

One gitlink can have entries in all three groups.

The limits:

- A porcelain line over 512 bytes is cut to 512 bytes, and `…` follows. At most 10000 porcelain lines pass, and no entry marks the cut. The submodule entries still follow.
- A submodule name over 200 bytes is cut to 200 bytes, and `…` follows. The name is then quoted with double quotes, `\"`, `\n`, `\t`, and `\xff` for a byte that is not valid UTF-8. A printable character outside ASCII stays.
- At most 100 submodule entries pass. Then one entry gives the count of the rest, for example ` S … (+20 more submodule entries)`.

claustrum's own, not measured: a cut can split a multi-byte character. In a porcelain line, the JSON encoding then sends U+FFFD for each leftover byte. In a submodule name, the quoting writes each leftover byte as `\xNN`.

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| No global excludes file of the user | The status. | The status. The reference answers `-32603 "exit status 128"` ([D16](../DIVERGENCES.md#d16)). |
| A `baseRepo` with a junction before its last component | Does not apply. | Does not resolve: `"isRepo": false`. A `baseRepo` that is a dangling junction gives the same. |
| A `baseRepo` that exists and in which git cannot start, such as a regular file | `"isRepo": false`. | `-32603` with the start error of the listing. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check). |
| A junction below `baseRepo` in `path` | Does not apply. | `"isRepo": false`, as for a symbolic link. |
| A last component of a `path` inside `baseRepo` that ends in a dot or a space, or holds a colon | Not a special case. | `"isRepo": false`. claustrum refuses such a name at any component (not measured). Outside `baseRepo` a trailing dot or space passes. |
| Letter case in the registration match | Exact. | The folder compare ignores letter case. A `gitdir` value that ends in `.GIT` fails. |
| Other spellings of `path` | As sent and resolved only. | A `subst` drive, a junction, a `\\?\` path and 8.3 short names match too. A `path` with short names inside a `baseRepo` in short names finds no registration. |
| `commondir` value | Exact text. | Forward or back slashes pass. Another letter case, a short name, a `\\?\` prefix and a value with no drive letter fail. |
| A `baseRepo` that is a junction to the repository | Does not apply. | The status. The daemon goes on with the git directory that git prints. |
| A gitlink path that is a regular file | ` (submodule; could not be inspected)`. | No entry. A gitlink path that is a junction gives ` (submodule; could not be inspected)`. |

## Differences from the reference

This entry of the divergence catalog applies to this method:

- [D16](../DIVERGENCES.md#d16): on Windows, with no global excludes file of the user, claustrum answers the status where the reference answers `exit status 128`.

The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
