# git.worktree_remove

`git.worktree_remove` deletes a linked git worktree that a session made. It deletes the worktree directory and then the registration of the worktree. If the request names a branch, it runs [the branch step](git-rules-calls.md#the-branch-step) last. The daemon deletes the files itself. It runs no `git worktree remove` and no `git worktree prune`.

Treat `worktreePath` as a path that you ask the daemon to delete, not as a filter. Without `worktreeRoot`, a plain directory inside the repository is deleted too, and the reply is success.

An untagged rule on this page matches the reference as measured on `5fd08069` or on an earlier build.

## Request

```json
{"baseRepo": "/repo", "worktreePath": "/repo/.claude/worktrees/w1", "branchName": "w1"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `baseRepo` | yes | The repository. |
| `worktreePath` | yes | The worktree to delete. See [Where the paths can be](#where-the-paths-can-be). |
| `branchName` | no | The branch that the branch step deletes after the worktree. |
| `worktreeRoot` | no | A location outside the repository, as on [git.worktree_create](git-worktree-create.md#request). |
| `shadowDirOff` | no | `true` turns off, for this request, the daemon's own read of the repository's git config and [the settings copy](git-rules-settings-copy.md#the-settings-copy). A git request that starts while such a request runs gets the same. |

## Response

Success:

```json
{"success": true}
```

If the branch step keeps the branch, the reply is `{"success":true,"branchKept":true}`. It keeps the branch too if it cannot read the branches, for example for a `baseRepo` that holds no repository or does not exist. Then `branchKept` comes even for a missing branch. The member is always `true`. A deleted branch, a branch that does not exist, an empty `branchName`, and a name that starts with `-` or `+` give a plain success.

Failure:

```json
{"success": false, "error": "<text>"}
```

A failure has no `errorCode`. Unless a section below says otherwise, a failure deletes nothing: the worktree directory, its registration and its branch stay. The exact texts are in [Error texts](#error-texts). Most of them start with one of these phrases:

| Start of the text | Meaning |
|---|---|
| `refusing to remove worktree:` | The daemon refuses this path or this worktree. The text says what to change, for example a lock to remove or a path to fix. |
| `failed to remove worktree: could not check whether <p> is locked` | The daemon could not read the repository safely. A text in brackets ends with `; retry`. Only a passing cause, such as a file that changed during the request, gives a second try a chance. A repository that git cannot use fails again in the same way. A text after a colon has no `; retry`. |
| `failed to remove worktree:` with another text | A check or a delete step failed. The sections below say what stays. |

In the texts, `<p>` is `worktreePath` as sent. Three texts start in another way: the home refusal `worktreePath must not be or contain the home directory: "<p>"`, the managed-worktrees refusal `baseRepo is inside a managed worktrees directory …`, and the registration text of step 3 below.

## Error texts

The shared texts are in the [error-string catalogue](errors.md#error-string-catalogue). A row with no mark holds a text of the reference, measured on Linux. A mark such as "measured on macOS" limits its sentence to that system. A sentence that names systems without "measured" holds on each one. `<repo>` is `baseRepo` as sent in every row. A row in the first two groups below deletes nothing unless it says so.

### Path and location

| Text | Cause |
|---|---|
| `refusing to remove worktree: <p> {is a relative path / contains a ".." component / has a component Windows reads as a different name (trailing dot or space, or a colon) [Windows] / is not inside the repository <repo>; …}` | The spelling refusal is Windows only and comes before containment. In the `is not inside` text `<p>` is in backticks and `<repo>` is as sent (`be1c1ca5`, measured on Linux). An absent or empty `baseRepo` gives the empty string for `<repo>`. With `worktreeRoot`, the first two texts also refuse a relative, absent or `..` `baseRepo`, and `<p>` is then `baseRepo` as sent. An empty `worktreePath` gets the first text there |
| `failed to remove worktree: "" does not name a directory` | (empty `worktreePath`, without `worktreeRoot`) |
| `refusing to remove worktree: <root> is a filesystem root; choose the worktree location by its absolute path, without "..", beneath the filesystem root` | Linux and macOS, with `worktreeRoot`. The rules of the [create row](git-worktree-create.md#error-texts) hold. claustrum tests a relative `worktreePath` first, as on create (not measured on remove). No git call runs |
| `refusing to remove worktree: <root> is the repository <repo> or inside it; a worktree location must be outside the repository` | With `worktreeRoot`, Linux and macOS. The test cleans the root and `baseRepo` and compares whole components. A root that is `baseRepo` or lies beneath it is refused, and `<B>/Tx` beside `<B>/T` passes. `<root>` is `worktreeRoot` as sent, so a trailing slash stays. `<repo>` keeps `<T>/` and `<T>/.` as sent. No git call runs |
| `refusing to remove worktree: <root> leads into the repository <repo> (at <at>); a worktree location must be outside the repository` | With `worktreeRoot`, Linux and macOS. The daemon resolves the symlinks of the longest existing part of the root and appends the missing rest. `<at>` is the outermost path from there up to `/` that is the same file as one of three folders. These are the git top level of `baseRepo`, the main checkout and the git directory, also a separate one. The main checkout is the first entry of `worktree list`. So `<at>` is a prefix of the resolved root, for example `/private/tmp/…` for a root sent as `/tmp/…` on macOS. A bind mount (Linux), a firmlink spelling and a case variant (macOS) match too, and `<at>` keeps their spelling. That holds for the main checkout of a linked `baseRepo` too. If both checkouts hold the root, the main checkout is named. A folder nearer to `/` that the row below matches gives that text (from the code) |
| `refusing to remove worktree: <root> leads into <wt>, a worktree of the repository <repo> (at <wt>); a worktree location must be outside the repository's checkouts` | With `worktreeRoot`, Linux and macOS. `<wt>` is a linked worktree in `worktree list` that is the resolved root or a parent of it by whole components. The test compares `<wt>` as listed, as a string: no symlink resolve, no case fold, no file identity. It skips a listed worktree whose path does not exist, also a locked one. Another lstat error keeps the entry (not measured). Of two nested worktrees, the outer one is named. A root that holds a linked worktree passes |
| `failed to remove worktree: the worktree location <root> is not reachable (<reason>); nothing was removed — retry once it is available, or remove the worktree by hand` | With `worktreeRoot`, Linux and macOS. The root passes the three tests above but does not exist or does not resolve. `<root>` is `worktreeRoot` as sent. For a root that does not exist, `<reason>` is `<path> does not exist`, with the links of its existing part followed and a final slash dropped. A root below a regular file gets `<file>: not a directory`. For a symlink that does not resolve, `<reason>` is the error of the resolve, for example `lstat <target>: no such file or directory`. For `permission denied` the text is `failed to remove worktree: lstat <root>: permission denied` (Linux) |
| `refusing to remove worktree: <c> is a symbolic link; a symlinked .claude or .claude/worktrees …` | It keeps the delete off a planted link (not measured on `be1c1ca5`) |
| `refusing to remove worktree: <t> {is a symbolic link, not a worktree directory / is not a directory}` | A leaf that is a symbolic link or not a directory. Nothing is deleted (measured on macOS) |
| `refusing to remove worktree: <t> changed while the removal was checking it` | claustrum's own text for a leaf that another directory replaced between two looks. Nothing is deleted. On Linux and macOS, with `worktreeRoot`, a `<directory>` that turns into a symlink out of the root between two looks answers `failed to remove worktree: openat <directory>: path escapes from parent` (claustrum's own) |
| `failed to remove worktree: openat <path from baseRepo>: path escapes from parent` | [D19](../DIVERGENCES.md#d19), Windows, when a directory between `baseRepo` and the leaf is a junction. `<path from baseRepo>` is the folder that holds the leaf, relative to `baseRepo`, for example `.claude\worktrees`. claustrum's own text. Nothing is deleted, and the branch stays |
| `worktreePath must not be or contain the home directory: …` | [D2](../DIVERGENCES.md#d2). Without `worktreeRoot` the containment answers first, so it fires only if a repo is an ancestor of home. With `worktreeRoot` it is the active home guard |

### Locks and the repository

| Text | Cause |
|---|---|
| ``refusing to remove worktree: `<p>` is locked (git worktree lock); unlock it to remove it`` | For a LOCKED worktree (`success:false`). Measured for a lock with no reason and the reason `x`. claustrum also refuses some locks where the reference deletes the folder or answers success ([D22](../DIVERGENCES.md#d22)) |
| ``refusing to remove worktree: `<p>` is gone but its registration is locked (git worktree lock); unlock it to remove the registration and branch`` | For a gone worktree with a locked registration. Nothing is deleted (measured on macOS). On Linux and macOS a gone worktree whose locked entry sits behind a linked `<git dir>/worktrees` gets success, and its branch goes |
| ``refusing to remove worktree: `<p>` was left locked by a create that did not finish; unlock it first (git worktree unlock '<p>', then remove it again)`` | The lock reason is `initializing`. Windows quotes the hint path with double quotes. Nothing is deleted. Build `be1c1ca5`, measured on Linux, macOS and Windows |
| ``refusing to remove worktree: `<p>` is gone but a create that did not finish left its registration locked; unlock it first (git worktree unlock '<p>', then remove it again) to drop the registration and branch`` | The same for a gone worktree. The registration stays. Build `be1c1ca5`, measured on Linux, macOS and Windows |
| `failed to remove worktree: could not check whether <p> is locked (<reason>); retry` | For a gone worktree. Without `worktreeRoot`, `<reason>` is the hooks refusal or the trust refusal (measured on macOS), or the `git cannot run` text. It is also the refusal of the daemon's `GIT_CONFIG_COUNT` (Linux, macOS and Windows). claustrum puts a limit refusal of the listing there too, not measured on remove. With `worktreeRoot` and a missing absolute `baseRepo` without a `..` component, `<reason>` is the refusal of the daemon's `GIT_CONFIG_COUNT` (Linux and macOS) |
| `failed to remove worktree: could not check whether <p> is locked: <text>` | Without `worktreeRoot`. `<text>` is the text of the refused step |
| `failed to remove worktree: could not check whether <p> is locked (its registrations could not be examined); retry` | The lock-check refusal, for a worktree directory that exists. Without `worktreeRoot`: the listing of `baseRepo` cannot be used, `baseRepo` holds no repository, or the trust check refuses its git directory. A refused `GIT_CONFIG_COUNT` of the daemon gets it too, also for a `baseRepo` such as `<T>/missing/..`. In both modes: the worktree registry exists but cannot be read, or, on Linux and macOS, is a symbolic link |
| `failed to remove worktree: could not check whether <p> is locked (the repository at <baseRepo> could not be read); retry` | With `worktreeRoot`. An absolute `baseRepo` without a `..` component is missing, and the worktree directory exists. The daemon's `GIT_CONFIG_COUNT` is refused (Linux and macOS). With no `git` on `PATH` (Linux), it answers a worktree directory that holds a file, is empty, or has a `.git` file naming its entry |
| `failed to remove worktree: cannot determine the repository's work tree: <reason>` | With `worktreeRoot` only. `<reason>` is a trust refusal text, `exit status 128`, the hooks refusal or the refusal of the daemon's `GIT_CONFIG_COUNT`. It is also `git finds no repository here: <git text>`, or with git 2.56 the text of a failed call about the repository. With no `git` on `PATH` it is the exec error alone, where `baseRepo` exists. claustrum also puts the `git cannot run` text and a limit refusal of the listing there (not measured with `worktreeRoot`) |
| `failed to remove worktree: cannot list the repository's worktrees: <exec error>` | With `worktreeRoot` only. `git worktree list --porcelain -z` and then `git worktree list --porcelain` both fail in `baseRepo`, for example with `exit status 128` |
| `failed to remove worktree: statat .claude: permission denied` / `failed to remove worktree: open <baseRepo>: <error>` | Without `worktreeRoot` only. `baseRepo` cannot be searched (mode 0600), or cannot be opened (mode 0000, or a regular file). On Linux and macOS the first text names the first component of `worktreePath` below `baseRepo`, for example `statat wt` |
| `failed to remove worktree: open <root>: permission denied` / `failed to remove worktree: openat <name>: permission denied` / `failed to remove worktree: statat .: permission denied` | Linux and macOS, for a folder that the daemon cannot read or search. See [Platform notes](#platform-notes) |

These rules are of build `be1c1ca5`. In the four lock texts that start with `refusing`, `<p>` is `worktreePath` in backticks (measured on Linux, macOS and Windows). In the plain lock text a path over 300 bytes shows its first 300 bytes, with no ellipsis (measured on Linux). The other texts cut it the same way (not measured). A path with a backtick is quoted as in Go (from the code). A `locked` file of 1048577 bytes gives the plain text (measured on Linux). The hint path is whole, and a `$` keeps the quoted hint (from the code).

### Delete failures

| Text | Cause |
|---|---|
| `failed to remove worktree: RemoveAll <entry>: <errno text>` | When the delete of the worktree fails part-way. If an entry other than `.git` fails, `.git` stays. The registration of the worktree and the branch stay in every case (measured on macOS) |
| `removed the worktree but could not drop its registration (RemoveAll <name>: <errno text>)` | `success:false`, from [step 3](#what-the-delete-removes) |

## Where the paths can be

Without `worktreeRoot`:

- `worktreePath` must be absolute, carry no `..` component, and sit strictly under `baseRepo`. Otherwise the text is `refusing to remove worktree: <p> …`, with the same reasons as on `git.worktree_create`. A relative path and the home directory are refused this way. If the repository holds the home directory, the home refusal answers instead ([D2](../DIVERGENCES.md#d2)).
- A symbolic link at any directory between `baseRepo` and the worktree, such as `.claude`, is refused.

With `worktreeRoot`:

- `worktreePath` must be `<worktreeRoot>/<directory>/<name>`, as on `git.worktree_create`. An empty `worktreePath` gets the text for a relative path.
- `baseRepo` must be absolute and carry no `..` component.
- A symbolic link at the `<directory>` level gives `refusing to remove worktree: <dir> is a symbolic link; the directory under the worktree location must be a real directory`.
- A root that is `/`, that is or leads into the repository or one of its worktrees, or that cannot be reached, is refused. The location refusals also include [a reserved name](errors.md#error-string-catalogue) (`.git` and `.claude-managed-worktrees`, in any letter case) and [a link loop](errors.md#error-string-catalogue). The remove runs no ancestor test, so some roots that the create refuses pass here.
- The containment in the repository does not apply here. So the home guard refuses a `worktreePath` that is the home directory or holds it, with the home refusal above ([D2](../DIVERGENCES.md#d2)).

On both:

- The home guard ([D2](../DIVERGENCES.md#d2)) runs before the delete, with and without `worktreeRoot`.
- A `baseRepo` inside a directory of managed worktrees is refused with the managed-worktrees text. So is a `baseRepo` that is not missing and does not resolve, for example `<T>/a.txt/..`.
- If the last component of `worktreePath` is a symbolic link, the text is `refusing to remove worktree: <t> is a symbolic link, not a worktree directory`. If it is not a directory, the text is `refusing to remove worktree: <t> is not a directory`. `<t>` is the path with the symbolic links of `baseRepo`, or of the root, resolved. On macOS a path under `/tmp` therefore reads `/private/tmp/…`.
- On Windows, a junction at any directory between `baseRepo` and the worktree is refused ([D19](../DIVERGENCES.md#d19)).

## The repository checks

Before it deletes anything, the daemon checks the repository. Each of these refuses, and nothing is deleted.

- The [git-directory trust check](git-rules-trust.md#git-directory-trust-check) runs on `baseRepo`. Without `worktreeRoot`, if it refuses the git directory, a worktree directory that exists gets the lock-check refusal, and a gone one gets `… is locked (<trust refusal text>); retry`.
- Without `worktreeRoot`, a git config of `baseRepo` that the daemon does not accept gives `… is locked: <text>`. See [Repository config refusals](git-rules-config.md#repository-config-refusals). `"shadowDirOff":true` turns this check off. A remove that starts while a `"shadowDirOff":true` request runs gets no such refusal either. It then deletes on a config that only the daemon refuses.
- Without `worktreeRoot`, a failure of the settings copy gives the same form. A failed start test of the settings copy refuses the remove before any delete. So do a bad answer of git about the repository and a copy folder that cannot be made.
- Without `worktreeRoot`, for a worktree directory that exists, a failed listing of the configuration runs once more. If the second try fails too, the text is `… is locked: <text of the failed listing>`. For a gone worktree directory there is no second try, and the text is `… is locked (<text>); retry`.
- With `worktreeRoot`, the daemon must find the work tree of the repository. If it cannot, the `cannot determine the repository's work tree` text answers. The reason: git cannot start, the trust check refuses or finds no repository, or the git config or the settings copy is refused. Or the configuration listing fails, `baseRepo` is itself a git directory, or `git rev-parse` fails. A `baseRepo` that does not exist skips this check.
- A worktree that is locked (`git worktree lock`) is refused. A lock in the `.git` folder of `baseRepo` is refused too, also where the reference deletes the worktree ([D22](../DIVERGENCES.md#d22)).

Without `worktreeRoot`, a `baseRepo` whose `refs` has mode 0600 holds no repository (build `52d82f0a`, Linux and macOS), and so does one with no `refs` (`c8e2f083` and `52d82f0a`, Linux). Blanks, a tab, CR LF or a NUL after `../..` in the registration's `commondir` refuse nothing (`52d82f0a`, Linux, macOS and Windows). If the listing fails in a plain folder, the text is `… is locked: <text of the failed listing>`. If the worktree directory is gone and the listing passes, the request is a success, and no registration is deleted. With `worktreeRoot`, no repository gives the work-tree refusal, also for a gone worktree.

If `<git dir>/worktrees` exists but cannot be read, a worktree that exists gets the lock-check refusal, and nothing is deleted. A gone worktree then gets success, and its registration stays.

## What the delete removes

The registration is the folder `<git dir>/worktrees/<name>` that git keeps for each linked worktree. `<git dir>` is the git directory of the main repository, also for a linked worktree as `baseRepo`. A daemon that sets `GIT_DIR` and `GIT_COMMON_DIR` uses the repository that `GIT_DIR` names.

The daemon finds the registration from the `.git` file of the worktree. The registration must name the worktree back. Without such a registration, it deletes the one registration whose record names `worktreePath`. If two match, it deletes none. If a registration that matches is locked, the request is refused first, as above. Without `worktreeRoot`, if the path compare misses the registration (another letter case on macOS, an 8.3 short name on Windows), the worktree directory still goes and its registration stays.

The steps, in order:

| Step | Deletes | If it fails |
|---|---|---|
| 1 | The files of the worktree directory, except `.git`, in the order of the directory read. | The delete stops at the first failure with the `RemoveAll <entry>` text. That entry, the entries after it and `.git` stay. |
| 2 | `.git` and the worktree directory. | If the directory itself cannot be deleted, it stays empty. |
| 3 | The registration. | If a verified registration cannot be deleted, the reply has `success:false` and the text `removed the worktree but could not drop its registration (RemoveAll <name>: <error>)`. The branch step still runs. If a registration found by its record cannot be deleted, the reply is success all the same. |
| 4 | If `branchName` names a branch, the branch step. | After the failure text of step 3, a kept branch adds `"branchKept":true` after `error`. |

No delete follows a symbolic link out of the worktree directory or out of its parent.

Without `worktreeRoot`, a `baseRepo` that does not exist, such as `<T>/missing/..`, still lets the worktree directory go. Its registration stays. With no `git` on `PATH`, a worktree directory that exists gets the lock-check refusal, and nothing is deleted. A gone one gets success, with `branchKept` when the request names a branch, and its registration stays.

With `worktreeRoot`, a directory without a verified registration goes in two cases only: it is empty, or it is a stale worktree of `baseRepo` whose registration is gone. Any other directory is refused and stays: `refusing to remove worktree: <p> is not a worktree of <repo> (<reason>), so it is left in place; remove it by hand if it is a leftover`. A read error there gives `failed to remove worktree: could not verify that <p> is a worktree of <repo> (<detail>); retry`, with the cleaned path as `<p>`. A `baseRepo` that does not exist with no `git` on `PATH` gives the `the repository at <repo> could not be read` text.

A gone worktree directory is no error by itself. After the repository checks, the daemon looks up the registration by path. A locked registration gives one of the two `is gone but` lock texts. Otherwise the one registration that names the worktree goes, the branch step runs, and the reply is success. The [trust check](git-rules-trust.md#git-directory-trust-check) rules read a `.git` file of `baseRepo`. A NUL after the path, or a second blank after `gitdir:`, names the repository whose registration and branch go. Without `worktreeRoot`, a blank after the path: only the branch goes (build `c8e2f083`, measured on Linux).

## Platform notes

=== "Linux and macOS"

    - `worktreeRoot` is allowed.
    - A junction rule does not apply.
    - The path compare of a registration is exact. On macOS a path in another letter case does not match.
    - If `<git dir>/worktrees` is a symbolic link, a worktree that exists gets the lock-check refusal, and nothing is deleted. A gone worktree gets success, and the branch step runs. Its registration stays.
    - A folder that the daemon cannot read or search is refused. Without `worktreeRoot` that holds for the folders between `baseRepo` and the worktree. With it, it holds from the root down. Each text follows `failed to remove worktree: `:

    | The folder | Text |
    |---|---|
    | The root, with no read bit (mode 0300, 0100, 0000 or 0200) | `open <root>: permission denied` |
    | A folder below the root or below `baseRepo`, with no read bit | `openat <name>: permission denied` |
    | A folder below the root or below `baseRepo`, or the root, with the read bit and no execute bit (mode 0600 or 0400) | `statat .: permission denied` |
    | The folder above the root, with mode 0600 | `lstat <root>: permission denied` |
    | `baseRepo` itself, without `worktreeRoot`, mode 0600 | `statat <first component>: permission denied` |
    | `baseRepo` itself, without `worktreeRoot`, mode 0000 | `open <baseRepo>: permission denied` |

    `<root>` is the root with its symbolic links resolved. `<name>` is one path component. If two levels are restricted, the higher one gives the text. A gone worktree under such a folder gets the text of that folder. A daemon that runs as root deletes the worktree under a root of mode 0300.

=== "Windows"

    - Windows refuses `worktreeRoot`: `refusing to remove worktree: <root> cannot be used: a custom worktree location is not supported on Windows hosts yet`.
    - A folder that the daemon cannot read is not measured.
    - If `<git dir>/worktrees` is a symbolic link, there is no refusal. The registration behind the link that names the worktree goes (from the code).
    - The path compare of a registration ignores letter case and slash direction. An 8.3 short name does not match a worktree that exists. A `baseRepo` through a `subst` drive or a junction to the repository matches, and the registration goes.

## Rare states

??? note "No git directory is held"
    The remove holds no git directory (from the code). On Linux and macOS a swap of `.git` during a remove leads the delete of the registration into the folder at the path. Windows is not measured.

## Differences from the reference

These entries of the divergence catalog apply to this method:

- [D2](../DIVERGENCES.md#d2): the home guard refuses a `worktreePath` that is the home directory or holds it.
- [D19](../DIVERGENCES.md#d19): Windows refuses a junction between `baseRepo` and the worktree. At a junction at `.claude` or `.claude\worktrees`, build `89cb6289` answers success and deletes the branch, for a branch that another ref reaches. At a junction above `.claude`, it answers success with no leaf and no branch there.
- [D22](../DIVERGENCES.md#d22): a worktree that is locked in the `.git` folder of `baseRepo` is refused.

The config refusals, the settings copy and the second listing are measured on `5fd08069`, the branch step and the refusal of a root that leads into a checkout on `89cb6289`, and most other rules on `f6010b97`. The cells are in the tests and the [reference build history](../REFERENCE-BUILDS.md).
