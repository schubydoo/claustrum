# git.worktree_create

`git.worktree_create` makes a linked git worktree for a session. It creates a new branch, or attaches the worktree to one that exists. Then it checks out the tracked files and copies some ignored files into it.

## Request

```json
{"baseRepo": "/repo", "branchName": "feature", "worktreePath": "/repo/.claude/worktrees/w1"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `baseRepo` | yes | The repository. |
| `branchName` | yes | The branch to create. It is required with `existingBranch` too. An attach that succeeds does not use it. |
| `worktreePath` | yes | The directory of the new worktree. It must not exist. |
| `sourceBranch` | no | The name that the new branch starts from. The default is the current branch. An attach does not use it. |
| `existingBranch` | no | A local branch to attach to. If it names no local branch, or the attach fails, the daemon creates `branchName` instead. |
| `worktreeRoot` | no | A location outside the repository. Windows refuses it with `unsafe_path`. |
| `timeoutMs` | no | A deadline that starts at the add. It stops the read-tree call. The daemon tests it after the add and the copy step. No value or `0` sets none. |
| `shadowDirOff` | no | `true` turns off, for this request, the filter rule of the checkout, the daemon's own read of the repository's git config, and the settings copy. Git requests that start meanwhile get the same. A command that starts meanwhile gets no copy (measured on Linux). Other values do nothing. |

Where the worktree can go:

- Without `worktreeRoot`, `worktreePath` must be an absolute path inside `baseRepo`. The usual place is `<baseRepo>/.claude/worktrees/<name>`.
- With `worktreeRoot`, `worktreePath` must be `<worktreeRoot>/<directory>/<name>`. A root that exists must belong to the user of the daemon. Its mode must have no write bit for others, or for a group other than the private group of that user.
- Neither a component of the location nor the `<directory>` can be `.git` or `.claude-managed-worktrees`, in any letter case. The location must not resolve to `/`. It must not lie in a folder with a `.git` entry, the git directory of the repository, or a session folder. The `<directory>` level must not hold the repository or be writable by every user.
- Not measured on the reference: `.claude-managed-worktrees` in the location, a reserved `<name>`, and the owner and group tests of the `<directory>` level. claustrum refuses all of them.

## Response

Success:

```json
{"success": true, "path": "/repo/.claude/worktrees/w1", "sourceBranch": "main", "branch": "feature"}
```

`path` is `worktreePath` as sent, after the `~` expansion. `branch` is `existingBranch` after an attach, and `branchName` otherwise.

If the daemon created a branch, `sourceBranch` is the name that the start commit came from. The daemon reads that name as two refs: the local branch `<name>` and the remote-tracking ref `origin/<name>`. It fetches nothing. If both exist, it selects `origin/<name>` in most states. The response holds the name as sent. If the name matches neither ref, that is not an error. The branch then starts at HEAD, and `sourceBranch` is the current branch. On a detached HEAD the response then has no `sourceBranch`.

Failure:

```json
{"success": false, "error": "<text>", "errorCode": "<code>"}
```

Decide by `errorCode` first.

| `errorCode` | Meaning |
|---|---|
| `unsafe_path` | The daemon refuses a path. For example, `worktreePath` is relative, has a `..` component, is outside `baseRepo`, or exists. A `worktreeRoot` that is not safe gives this code too. So does a worktree that git registered in an unexpected place. |
| `symlinked_component` | A directory between `baseRepo` and the worktree is a symbolic link. |
| `not_a_repo` | `baseRepo` is not a git repository, or git itself does not accept it. A bare `baseRepo` gets this code too while the daemon works on its settings copy. |
| `nested_base_repo` | `baseRepo` is inside a directory of managed worktrees, or the daemon does not accept it as a trust root. |
| `mkdir_failed` | The daemon cannot create a directory for the worktree. Or, with `worktreeRoot`, a git call or a `.git` test failed first. |
| `worktree_add_failed` | `git worktree add`, the checkout or the `config.worktree` step failed. The text holds the message of git, or a refusal of the daemon before the add. A `git worktree add` that runs 2 minutes is stopped. |
| `repository_only_filter` | The checkout needs a git filter that only the git config of the repository defines. See [Git filters](#git-filters). |
| `timeout` | The deadline of `timeoutMs` expired. |

A git config of the repository that the daemon does not accept gives `worktree_add_failed`, and nothing is made. The texts are in [Repository config refusals](../PROTOCOL.md#repository-config-refusals). Three failures of the settings copy do the same: a failed test of the host git, a failed call about the repository, and no folder for the copy.

A request with no `branchName` gets the JSON-RPC error `-32602`. The exact texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).

## After a failure

A request can create four things outside the worktree, and no failure removes them:

- directories above the worktree
- the registrations directory `<git dir>/worktrees`
- with `worktreeRoot`, the marker file `.claude-managed-worktrees` at the `<directory>` level
- with `worktreeRoot`, the folder of a gone worktree that holds the root

Before the add, a request can remove one stale registration in the common git directory of `baseRepo`. A stale registration is one whose `gitdir` record names the `.git` of the new worktree. If it is the one stale entry and has no `locked` file, it goes. Otherwise none goes. Nothing behind a symbolic link or a junction at `worktrees` goes (measured on Windows with `"shadowDirOff": true`, claustrum's own elsewhere). Two creates through a repository and its linked worktree add in turn.

| Failure | What stays of the worktree |
|---|---|
| A refusal of the request or of a path (`not_a_repo`, `nested_base_repo`, `symlinked_component`, and `unsafe_path` for a path) | Nothing, except the folder of a gone worktree above. |
| A refusal before `git worktree add` (`worktree_add_failed`), or `mkdir_failed` | Nothing. |
| `git worktree add` runs and fails (`worktree_add_failed`) | If the worktree directory is empty, the daemon removes it. Files that the failed add wrote stay. A registration and a branch that the add made stay too. |
| A test of the new worktree fails after the add. On Linux and macOS the tests come before the checkout and give `unsafe_path`. A worktree directory replaced during the checkout gives `worktree_add_failed`. So does a failed test on Windows, except a `.git` file that names another staged entry. That gives `unsafe_path`. | Everything, except an empty worktree directory. On Windows, a `.git` file that names another staged entry leaves nothing: the worktree directory and the branch that the request created go. |
| The checkout or the `config.worktree` step fails, the filter rule refuses, or the deadline expires (`worktree_add_failed`, `repository_only_filter`, `timeout`). Or the text holds `; retry` before any clause of the rollback (Linux and macOS). | The daemon removes the worktree directory and its registration. If this request created the branch, the daemon removes the branch too. |

A rollback leaves something in these cases:

- If the branch holds commits that no other ref reaches, the daemon keeps the branch. The response then has `"branchKept": true`.
- After a failed `config.worktree` step, a refused second config read keeps the branch without `branchKept` (Linux and macOS).
- If a delete fails, the error text ends with a clause that says what remains. If the files of the worktree cannot be deleted, the registration and the branch stay. On Linux and macOS, and on Windows for a moved registration (not measured), a registration that cannot be deleted keeps the branch too. On Linux and macOS in the state `shadow`, after an empty answer of git about the git directory, a registration that cannot be deleted adds no clause, and the branch step runs (claustrum's own). On Windows a registration that stays adds no clause, unless the create moved it.
- If something replaced the worktree directory, the rollback keeps the replacement and adds no clause. It also keeps a replaced registration directory ([D24](../DIVERGENCES.md#d24)).

## Git filters

In the state `shadow`, a checkout file that uses a filter that only the repository config defines fails the request:

```json
{"success": false, "error": "Claude doesn't run the \"<name>\" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter \"<name>\"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.", "errorCode": "repository_only_filter"}
```

In the state `fallback-git` git runs the filters, and this section does not apply. In the state `shadow`:

- A filter is defined where its `smudge` or `process` key has a value. A file that the repository config includes counts as that config, also through `includeIf`, whatever its condition. With a settings copy, a last `smudge` or `process` line with no `=` in .git/config is a definition too.
- A filter of the user or system config or of the daemon environment runs with its values, also if the repository defines it. With a settings copy, a user `process = cat` is no outside definition, so a repository `smudge` command is refused.
- With a user `lfs.storage` the checkout gets no `lfs.storage` item of the daemon (Linux, macOS and Windows). A failing user `smudge` fails the request where the repository adds only `required = true`, `yes` or `1`.
- Three repository values pass: `smudge = cat` under each name, and `git-lfs smudge -- %f` and `git-lfs filter-process` under `lfs`. With a settings copy `git-lfs smudge --skip %f` passes under `lfs` too. A filter whose values are all `cat` gets no item.
- With a settings copy, a `smudge` value that does not pass refuses the filter, in .git/config or in an `includeIf` file, in either order. In .git/config only the last value counts. A plain include file and `config.worktree` count the same way (not measured).
- With a settings copy, a refused name that is not valid UTF-8 gets success on Windows. On Linux it gets the checkout text of git less those bytes.
- With a settings copy, a repository-only `lfs` filter with the `git lfs install` lines gets those lines as items. A git-lfs program key of the repository refuses nothing, and its program does not run. With no settings copy such a key turns the filter `lfs` off (claustrum's own).
- A failed checkout text with `smudge filter lfs failed` gets a note about ssh, credential and proxy settings after it.

The [error-string catalog](../PROTOCOL.md#error-string-catalogue) has the other refusals of the rule.

## The settings copy

The git calls of the daemon read its own copy of the repository's git settings. [The settings copy](../PROTOCOL.md#the-settings-copy) has the states, the texts and the folder. For this method:

- A missing `<git dir>/worktrees` is made before the add, with mode 0777 less the umask. It stays after a failed add. On Linux and macOS a last `core.sharedrepository` line of the copy other than `umask` sets its mode. macOS is not measured.
- With `extensions.worktreeConfig` the new worktree gets the `config.worktree` file of `<git dir>`, or of a linked `baseRepo`, less `core.bare` and `core.worktree`.
- A bare `baseRepo` gets `not_a_repo`.
- While a `"shadowDirOff": true` request runs, the checkout has its index in `claude-ssh-index-<n>` of the temporary folder.
- In the state `shadow`, a failed `rev-parse --absolute-git-dir` stops the create with `git worktree add failed: cannot locate the repository's git directory: exit status 128`. macOS is not measured.
- On Linux and macOS, a linked worktree as `baseRepo` whose registration entry is a symbolic link gets the trust refusal after the prefix `git worktree add failed: cannot locate the repository's git directory: `. The code is `worktree_add_failed`. This refusal comes before the path tests, so a path that fails them, or that exists, gets this text too (claustrum's own). Without `worktreeRoot`, the create makes the parent folders of the worktree, such as `.claude/worktrees`, before it answers. It makes them only for a `worktreePath` that passes the path tests and does not exist (claustrum's own, not measured). This is measured on Linux only, with `extensions.worktreeConfig` and a `config.worktree` file. Other forms are not measured.
- On Linux and macOS a symbolic link, a file or a folder that cannot be opened at `worktrees` gives `git worktree add failed: cannot prepare the repository's worktrees directory: <cause>`, and no `worktree add` runs. On Windows any failed step before the add does the same.
- On Windows git writes the registration into a short-lived folder of the daemon. The daemon copies it into `<git dir>\.claude-new-worktree-<16 hex>` and renames that folder to the first free name of `<name>`, `<name>1` to `<name>99` in `<git dir>\worktrees`. A held folder gets 2 s of retries, also past `timeoutMs`. A rename that still fails gives `worktree_add_failed`, and the rollback removes the staging folder. A failed write of the `.git` file of the worktree, and a rollback, remove the moved registration by its name. `config.worktree` and the index are new files there.
- On Windows, before that step, a create removes old staging entries of the git directory. Their name starts with `.claude-new-worktree-`, and their write time is 1 hour or more. A junction goes as a junction, and its target stays.

With `"shadowDirOff": true`, a link or a file at `worktrees` refuses the add after its listing (Linux, macOS and Windows). A file there on Windows is not measured.

## The held git directory

In the state `shadow` a create holds the git directory of `baseRepo` open from its gate to its reply. The gate is the trust check of the git directory and the daemon's own read of its config.

- The git directory must be a real directory at the gate (the test is claustrum's own). On Windows a junction or a directory symbolic link there is refused, on every git method without `"shadowDirOff": true`. With that member both link kinds pass, and git works in the folder behind the link. On Linux and macOS a `.git` that is a symbolic link before the request is resolved, and the daemon holds its target. A change of that link counts as a swap of the path.
- The daemon's own deletes, renames, new folders and file writes inside the git directory go through the held folder. A folder that is put at the path while the create runs gets none of them, except the directory that the hold moves to. A create whose gate ran while a `"shadowDirOff": true` request ran takes the folder at a later gate before its add (claustrum's own).
- The hold stops no rename of the git directory by another process (not measured on Linux and macOS).
- A swap after `rev-parse --absolute-git-dir` and before the add is refused before the add.
- A swap after the read-tree call fails the create, and the registration gets no index. On Windows a swap after the first listing of the checkout fails it too. The text starts with `git worktree add failed (checkout): `. The registration and the branch stay, and the text says so.
- On Linux and macOS two swaps move the hold to a copy of the git directory, and the create goes on in the copy. One is a link to the copy at `.git` after the first call or after `rev-parse --abbrev-ref HEAD`. The other is a real copy put there while `git worktree add` runs.
- On Linux and macOS, a `.git` that became a file with `gitdir: <another git directory>` after `rev-parse --absolute-git-dir` gives `git worktree add failed: cannot locate the repository's git directory: lstat <baseRepo>/.git/commondir: not a directory`.
- On Linux and macOS, a `.git` that became a symbolic link to a copy right after a good `worktree add` gives `refusing to create worktree: <leaf> was not populated by git worktree add` with `unsafe_path`. Nothing is rolled back, and the `.git` file of the new worktree keeps the text of git.
- These go by the path: git itself, a host in the state `fallback-git` or with no run folder, and a request with `"shadowDirOff": true`. So does a request that starts while such a request runs, until a later gate before its first good add. Before that gate, or with a `worktrees` folder outside the held folder, the stale-registration step goes by the path too.

## The copy of ignored files

After the checkout, the daemon copies two sets of files from `baseRepo` into the new worktree:

- The ignored files that the root file `.worktreeinclude` names. That file uses the syntax of `.gitignore`.
- The ignored files under `.claude/`.

Neither set includes `.claude/worktrees` or the session state of Claude under `.claude/`, for example `checkpoints`, `mailbox` and `scheduled_tasks.json`.

Each copy is a new file with mode `0666` less the umask, not the mode of its source. The daemon skips symbolic links. A copy does not replace a file or a link that the checkout made.

A copy that fails does not fail the request. If the deadline of `timeoutMs` expires during the copy, the request fails with `timeout`. A scan of ignored files stops at 10 minutes ([Time limits of git calls](../PROTOCOL.md#time-limits-of-git-calls)). Then nothing is copied. The skip of the `.claude/` files is claustrum's own.

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| `worktreeRoot` | Allowed. | Refused with `unsafe_path`. |
| A junction between `baseRepo` and the worktree | Does not apply. | Refused with `mkdir_failed`. |
| The stale registration | Removed by the rule of [After a failure](#after-a-failure). The compare keeps letter case. | The same rule, after `rev-parse --absolute-git-dir`. The compare ignores letter case, measured for a create that holds its git directory. An entry whose record names a path that exists stays ([D27](../DIVERGENCES.md#d27)). |

## Differences from the reference

These entries of the divergence catalog apply to this method:

- [D2](../DIVERGENCES.md#d2): a delete refuses a path that is the home directory or holds it.
- [D24](../DIVERGENCES.md#d24): a rollback and the index placement leave a registration directory that something replaced. On Windows a rollback after a swap of the `.git` file of the worktree runs no git call.
- [D27](../DIVERGENCES.md#d27): on Windows the stale step keeps an entry whose record names a path that exists.

Most rules follow a measured reference build. The marks "claustrum's own" and "not measured" flag the known exceptions. The measured cells are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
