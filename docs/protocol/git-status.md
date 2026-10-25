# git.status

`git.status` reports the changes in a linked git worktree of a session. If `path` is a linked worktree that `baseRepo` registered, the reply is the status. Every other request that does not fail gets `"isRepo": false`. The daemon runs git on a temporary copy of the git files of the worktree. So the status never rewrites the index and never takes `index.lock`.

An untagged rule on this page matches build `5fd08069` or an earlier build, measured on Linux, macOS and Windows.

## Request

```json
{"path": "/repo/.claude/worktrees/w1", "baseRepo": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | The linked worktree. Send an absolute path. Without `path`, the daemon looks for a registration of its own working directory (not measured). Send `path`. A relative `path` gives `"isRepo": false` or `-32603 exit status 128`. A `.` works like no `path` (from the code). [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |
| `baseRepo` | yes | The repository that owns the worktree. A linked worktree as `baseRepo` gives `"isRepo": false`. Send the main repository (from the code) (not measured). |
| `shadowDirOff` | no | `true` turns off the daemon's own read of the repository's git config and its settings copy for this request. [Turning the read off](../PROTOCOL.md#turning-the-read-off). |

## Response

```json
{"isRepo": true, "clean": true}
```

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

That holds for:

- a `path` that is the repository itself, a plain folder or a subfolder of the repository
- a nested repository, a missing folder or a regular file
- a worktree of another repository, or a right worktree named with the wrong `baseRepo`

[How the daemon finds the worktree](#how-the-daemon-finds-the-worktree) has the rules.

## Errors

A failure is a JSON-RPC error with no `errorCode` member. It changes nothing in the repository or the worktree.

### Shared refusals

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | No `params` member, or a member that it reads has the wrong type. [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `baseRepo is required` | `baseRepo` is absent, or empty (from the code). |
| `-32603` | `the repository's git directory could not be trusted; git not run: …` | The daemon does not trust the git directory. [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check). |
| `-32603` | `config-defined hooks could not be pinned off; git not run: …` | A config listing failed or passed a limit of the hook pins. Or the daemon refuses its own git environment, or a daemon `GIT_DIR` names a gone worktree entry. A listing stopped at 30 seconds has its own text in [the error catalogue](../PROTOCOL.md#error-string-catalogue). [Refusal texts](../PROTOCOL.md#refusal-texts) and [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment). |
| `-32603` | `git cannot run on this host; git not run: …` | A config listing, the read of the test repository's config or the call about the repository failed, and then `git version` failed too. [the error catalogue](../PROTOCOL.md#error-string-catalogue). |
| `-32603` | `this repository's git config cannot be used: …` or `a file in this repository's .git directory cannot be used: …` | The daemon does not accept the git config or a git directory file. [Repository config refusals](../PROTOCOL.md#repository-config-refusals). |
| `-32603` | `Claude could not test git on the remote machine (…). Check free disk space and quota there, then try again.` | The start test of the settings copy failed. The daemon keeps the failure for 10 seconds. [The start test](../PROTOCOL.md#the-start-test). |
| `-32603` | `git could not check this repository on the remote machine: …` or `git and Claude disagree about which repository holds this folder: …` | The call that asks git about the repository failed, ran over 30 seconds, or named another repository. [The call about the repository](../PROTOCOL.md#the-call-about-the-repository). |
| `-32603` | `Claude could not prepare its own files on the remote machine: …` | The daemon cannot prepare the folder of the settings copy. [The folders](../PROTOCOL.md#the-folders). |

### Git and copy failures

| Code | Text | Cause |
|---|---|---|
| `-32603` | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | A git call ran 2 minutes, and the daemon stopped it. While a `"shadowDirOff":true` request runs, that one included, the limit is 30 minutes, and the text says so (claustrum's own) (not measured). [Time limits of git calls](../PROTOCOL.md#time-limits-of-git-calls). |
| `-32603` | The Go error of the git call, for example `exit status 128` | `git status`, `git ls-files` or `git diff-index` failed. The text of git does not show. A relative `path` that matches a registration gets `exit status 128`, because git takes it as sent. |
| `-32603` | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: ` and the start error, for example `chdir <path>: no such file or directory` | `path` passed the test as a cleaned path, but git cannot start in the path as sent. An example is `<dir>/missing/..` outside `baseRepo` (measured on Linux and macOS only). |
| `-32603` | `<file>: not a regular file` | From the copy into the temporary git folder after the gate passed. An `info/sparse-checkout` folder or FIFO gets it (claustrum's own) (not measured). |
| `-32603` | `<file> is larger than <n> bytes` | A copied file is over its bound, also one that grew after the gate. `<n>` is 1073741824 for `index`, a `sharedindex.<40 or 64 hex>` file, `info/sparse-checkout` and a reftable file, and 1048576 for `HEAD` and `config.worktree` (claustrum's own) (not measured). |
| `-32603` | An error of the file system, for example `openat index: permission denied` or `stat <dir>: no such file or directory` | The raw Go error. The daemon cannot copy a file of the registration or make a file of the temporary git folder (not measured). |

A failed config listing and a failed temporary folder have notes under [Rare states](#rare-states).

## How the daemon finds the worktree

The daemon does not ask git inside `path`. It reads the registrations of the repository. A registration is the folder `<common>/worktrees/<name>` that git keeps for each linked worktree. `<common>` is the git directory that `git rev-parse --absolute-git-dir` prints in `baseRepo`.

### `baseRepo`

Each of these gives `"isRepo": false` before any git call:

- `baseRepo` has the components `.claude/worktrees` in it, or is that folder. `<dir>/.claude` alone passes.
- A folder above `baseRepo` holds an entry named `.claude-managed-worktrees` and no `.git`. A marker inside `baseRepo` itself does not count. A marker one or two levels above counts (measured on Linux only). Further up the test is claustrum's own (not measured).
- `baseRepo` does not resolve. For example, it does not exist, or a `..` after a symbolic link goes up from the target of the link and finds no folder.

Then the [git-directory trust check](../PROTOCOL.md#git-directory-trust-check) runs on `baseRepo`, not on `path`. A refusal answers `-32603`, also for a good linked worktree as `path`. If the check finds no repository, the answer is `"isRepo": false`, with [exceptions](#rare-states). A config of `baseRepo` that the daemon does not accept answers `-32603`, as in [Repository config refusals](../PROTOCOL.md#repository-config-refusals).

`baseRepo` can be a symbolic link to the repository, a bare repository, a submodule checkout or a subfolder of a repository. If git prints no git directory, the answer is `"isRepo": false`. So is a repository with no `worktrees` folder.

### `path`

Each of these gives `"isRepo": false`:

- `path` is not a folder, or it does not exist.
- `path` lies inside `baseRepo`, and a component below `baseRepo` is a symbolic link. That includes a link at `.claude`, and a link at the last component (measured on Linux only).
- `path` lies inside `baseRepo` and has a `..` component. A `.` component passes (measured on Linux only). Outside `baseRepo` a `..` passes.

The `.git` file inside `path` plays no part. If it is missing, damaged or names another place, the answer is still the status. So is a locked worktree.

### The registration match

Exactly one registration must name `path`. Two that match give `"isRepo": false`, and so does none. A relative `gitdir` or `commondir` value counts from the registration folder.

- A registration is a real folder. A symbolic link under `worktrees` does not count.
- Its `gitdir` file must be a regular file of at most 1 MiB. The last component of its value must be `.git`.
- The folder before `.git` must equal `path` as text. Two spellings of `path` match: as sent, and with its symbolic links resolved. For a `path` inside `baseRepo`, the second spelling is the resolved `baseRepo`. The rest of `path` follows as sent (claustrum's own) (not measured below a symbolic link).

The matched registration must pass these tests. Each failure gives `"isRepo": false`.

- `commondir` must be a regular file, or a relative symbolic link to a file in the registration folder. Its value must equal `<common>` as text, in the spelling that git printed.
- `HEAD` must hold 40 or 64 hex characters, or a `ref:` line under `refs/` (measured on Linux only). A branch that does not exist and a detached commit pass.
- `index` must be a regular file of at most 1 GiB, or be missing (measured on Linux and Windows only). A symbolic link to a regular file in the registration folder passes too (measured on Linux only). A `sharedindex.<hex>` file that is a regular file has the same 1 GiB bound. The `index` and all `sharedindex.*` files together must hold at most 2 GiB (measured on Linux only).
- `config.worktree` can be missing. If it exists, it must parse and hold only `core.sparseCheckout`, `core.sparseCheckoutCone` and `index.sparse`. `user.name` gives `"isRepo": false`. So does `core.bare`, and `core.sparseCheckoutCone` passes (measured on Linux only). claustrum refuses every other key too (not measured).

## What the status reads

The daemon makes a temporary git folder in its own folder under `git-shadow`. With `"shadowDirOff":true`, or with no run folder, it is in the temporary directory of the system. It holds copies of `HEAD`, `index`, `config.worktree`, `info/sparse-checkout`, the `sharedindex.*` files with hex names and the reftable files of the registration. It also holds a `commondir` file. The daemon removes the folder before it replies.

The daemon runs four git commands in `path`. The git directory is that folder, and the work tree is `path` as sent:

```text
status --porcelain --untracked-files=all --ignore-submodules=all
ls-files -s -z
rev-parse --verify -q HEAD^{commit}
diff-index --cached --raw -z --ignore-submodules=none HEAD --
```

If `HEAD` names no commit, `diff-index` compares the staged changes with the empty tree.

- The calls ignore the `.gitattributes` files of the repository, so no clean filter runs. A git older than 2.40 has no option for that, and then the attributes apply.
- The calls do not read replace objects (`refs/replace`), so the status is that of the real commits.
- `status`, `ls-files` and `diff-index` carry `GIT_OPTIONAL_LOCKS=0`. All calls run as [hardened git calls](../PROTOCOL.md#hardened-git-calls).
- If the user has a global excludes file, the status uses it.

## The changes

`changes` holds three groups, in this order.

1. The lines of `git status --porcelain`, verbatim, minus the line end. Warnings on stderr never appear. The XY column is positional, so the leading space of an unstaged change is data: `"M  a.txt"` is staged and `" M b.txt"` is unstaged. An untracked file in an untracked folder has its own entry, as `"?? sub/u.txt"`.
2. One entry for each gitlink in the index, unless `<name>/.git` does not exist. A gitlink is an index entry of mode `160000`. There is one entry for each index stage, so a gitlink in conflict gives three.
3. One entry for each staged change whose old or new mode is `160000`.

Groups 2 and 3 are measured on Linux only, and one gitlink can have entries in all three.

### Submodule entries and limits

Every rule in this subsection is measured on Linux only.

A submodule entry is ` S `, the quoted name and one of three texts:

| Text | Meaning |
|---|---|
| ` (submodule present; contents not inspected)` | `<name>/.git` exists, as a file, a folder or a dangling symbolic link. A gitlink path that is a symbolic link to a folder inside the work tree counts as present. |
| ` (submodule; could not be inspected)` | The daemon cannot look at `<name>/.git`. Examples are a gitlink path that is a regular file, a symbolic link out of the work tree, or a folder of mode 000. |
| ` (submodule change staged; contents not inspected)` | A staged change of a gitlink. |

- A porcelain line over 512 bytes is cut to 512 bytes, and `…` follows. At most 10000 porcelain lines pass, and no entry marks the cut. The submodule entries still follow.
- A submodule name over 200 bytes is cut to 200 bytes, and `…` follows. The name is then quoted with double quotes, `\"`, `\n`, `\t`, and `\xff` for a byte that is not valid UTF-8. A printable character outside ASCII stays.
- At most 100 submodule entries pass. Then one entry counts the rest, for example ` S … (+20 more submodule entries)`.

## Differences by system

An untagged rule in a tab is measured on that system.

=== "Linux and macOS"

    - With no global excludes file of the user, the status runs.
    - Junction rules do not apply. That covers a `baseRepo` with a junction before its last component, a junction below `baseRepo` in `path`, and a `baseRepo` that is a junction.
    - A `baseRepo` that exists and in which git cannot start, such as a regular file, gives `"isRepo": false`.
    - A last component of a `path` inside `baseRepo` that ends in a dot or a space, or holds a colon, is not a special case.
    - Letter case in the registration match is exact.
    - `path` matches as sent and as resolved, and in no other spelling.
    - The `commondir` value must be exact text.
    - A gitlink path that is a regular file gives ` (submodule; could not be inspected)` (measured on Linux only).

=== "macOS"

    - Another letter case, the NFD form and `/System/Volumes/Data` do not match in the registration match.
    - A `commondir` value under `/tmp` fails, because git prints `/private/tmp`.

=== "Windows"

    - With no global excludes file of the user, the status runs. The reference differs ([D16](../DIVERGENCES.md#d16)).
    - A `baseRepo` with a junction before its last component, or a dangling junction, does not resolve: `"isRepo": false`.
    - A `baseRepo` that exists and in which git cannot start, such as a regular file, gives `-32603` with the start error of the listing. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check).
    - A junction below `baseRepo` in `path` gives `"isRepo": false`, as a symbolic link does.
    - A last component of a `path` inside `baseRepo` that ends in a dot or a space, or holds a colon, gives `"isRepo": false`. claustrum refuses such a name at any component (not measured). Outside `baseRepo` a trailing dot or space passes.
    - The folder compare of the registration match ignores letter case. A `gitdir` value that ends in `.GIT` fails.
    - A subst drive, a junction, a `\\?\` path and 8.3 short names match `path` too. A `path` with short names inside a `baseRepo` in short names finds no registration.
    - A `commondir` value with forward or back slashes passes. Another letter case, a short name, a `\\?\` prefix and a value with no drive letter fail.
    - A `baseRepo` that is a junction to the repository gives the status. The daemon goes on with the git directory that git prints.
    - A gitlink path that is a regular file gives no entry. A junction there gives ` (submodule; could not be inspected)`.

## Rare states

??? note "A check that finds no repository"
    A config listing that fails gives the hooks refusal. If `git version` fails too, it gives the "cannot run" text of [Hardened git calls](../PROTOCOL.md#hardened-git-calls).

    A `.git` file can name a gone git directory that is no registration. If git 2.56 or newer says so, the answer is `git could not check this repository on the remote machine: at "<root>": …`. With `"shadowDirOff":true` it is the hooks refusal. An older git gives `"isRepo": false`. All three rules are measured on Linux only.

??? note "A failed config listing"
    The first config listing refuses by [these classes](../PROTOCOL.md#a-failed-configuration-listing). [Hook pins](../PROTOCOL.md#hook-pins) has the rules for later listings and for a hook-pin limit. The empty-tree listing never refuses (from the code). A stopped listing of `config.worktree` gives `{"isRepo":false,"clean":false}` (measured on Linux only).

??? note "No temporary git folder"
    If the daemon cannot make its folder under `git-shadow`, the response is `{"isRepo":false,"clean":false}` (measured on Linux and Windows only). When the folder is in the temporary directory of the system, a failure answers `-32603` with the error of the file system (not measured).

??? note "Reftable and FIFO files"
    A repository can have the reftable format. If the `reftable` folder then holds a `tables.list` file, it must name at least one table. Each table must be a regular file. If `reftable` is not a real folder, or `tables.list` is a FIFO, the request goes on. git then shows every tracked file as added. All of this is measured on macOS only.

    No test waits on a FIFO. A FIFO in place of `gitdir`, `commondir` or `config.worktree` fails at once (measured on Linux and macOS only). A FIFO at `index` fails at once too (measured on Linux only), and so does one at `HEAD` (from the code).

??? note "A cut inside a multi-byte character"
    In a porcelain line, the JSON encoding sends U+FFFD for each leftover byte. In a submodule name, the quoting writes each leftover byte as `\xNN` (claustrum's own) (not measured).

## Differences from the reference

A rule that names a system is measured there only. A linked rule keeps the marks of its own section.

This entry of the divergence catalog applies:

- [D16](../DIVERGENCES.md#d16): on Windows, with no global excludes file of the user, claustrum answers the status where the reference answers `-32603 "exit status 128"`.

The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).

*[measured on Linux only]: Not measured elsewhere.
*[measured on Linux and macOS only]: Not measured on Windows.
*[measured on Linux and Windows only]: Not measured on macOS.
*[measured on macOS only]: Not measured elsewhere.
*[not measured]: No test measured this.
*[from the code]: Read from the code. Not measured.
*[claustrum's own]: A rule of claustrum, with no reference behavior.
