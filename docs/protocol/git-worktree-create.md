# git.worktree_create

`git.worktree_create` makes a linked git worktree for a session. It creates a new branch for the worktree, or it attaches the worktree to a branch that exists. Then it checks out the tracked files and copies some ignored files into the new worktree.

A failed request can leave files on disk. The section [After a failure](#after-a-failure) says what stays.

## Request

```json
{"baseRepo": "/repo", "branchName": "feature", "worktreePath": "/repo/.claude/worktrees/w1"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `baseRepo` | yes | The repository. |
| `branchName` | yes | The branch to create. It is required with `existingBranch` too. An attach that succeeds does not use it. |
| `worktreePath` | yes | The directory of the new worktree. It must not exist. |
| `sourceBranch` | no | The name that the new branch starts from. The daemon reads it as a local branch and as `origin/<name>`. The default is the current branch. An attach to `existingBranch` does not use it. A name that matches no branch is not an error: see [Response](#response). |
| `existingBranch` | no | A local branch to attach to. If it names no local branch, the daemon creates `branchName` instead. If the attach fails, the daemon does the same. For example, git refuses an attach to a branch that is checked out in `baseRepo`. The request can then still succeed. `branch` in the response says which branch the worktree is on: see [Response](#response). |
| `worktreeRoot` | no | A location outside the repository. Linux and macOS only. |
| `timeoutMs` | no | A deadline that starts at the add. It stops only the checkout. The daemon tests it again after the add and after the copy step. With no value or `0` there is no deadline. |
| `shadowDirOff` | no | `true` turns off three things for this request: the filter rule of the checkout, the daemon's own read of the git config of the repository, and the settings copy. Any other value does nothing and is no error. See [Git filters](#git-filters), [Repository config refusals](../PROTOCOL.md#repository-config-refusals) and [The settings copy](#the-settings-copy). |

Where the worktree can go:

- Without `worktreeRoot`, `worktreePath` must be an absolute path inside `baseRepo`. The usual place is `<baseRepo>/.claude/worktrees/<name>`.
- With `worktreeRoot`, `worktreePath` must be `<worktreeRoot>/<directory>/<name>`. A root that exists must belong to the user of the daemon. Its mode must have no write bit for the group or for others. If the group of the root is the private group of that user, the group can have the write bit. The daemon judges the directories above the root by a looser rule. Neither the root nor a directory above it can hold a `.git` entry. The [measurement record](../record/git-worktree-create.md) has the exact rules.

## Response

Success:

```json
{"success": true, "path": "/repo/.claude/worktrees/w1", "sourceBranch": "main", "branch": "feature"}
```

`path` is `worktreePath` as sent, after the `~` expansion. `branch` is the branch of the worktree. If the daemon attached to `existingBranch`, it is that branch. If not, it is `branchName`.

If the daemon created a branch, `sourceBranch` is the name that the start commit came from. The daemon reads that name as two refs: the local branch `<name>` and the remote-tracking ref `origin/<name>`. It fetches nothing. If both exist, the daemon selects one of them. It selects `origin/<name>` in most states. For example, a local branch that is behind `origin/<name>` or equal to it gives `origin/<name>`. The response holds the name as sent in each case. So the response does not say which of the two refs the new branch started from. The [measurement record](../record/git-worktree-create.md) has the rules.

If the value as sent named neither a local branch nor a branch of `origin`, that is not an error. The new branch then starts at HEAD, and the response holds the current branch, not the value as sent. Compare the value that you sent with the value in the response. On a detached HEAD, the response then has no `sourceBranch`. That applies to a request with a `sourceBranch` and to a request without one.

If the daemon attached to `existingBranch`, it did not use `sourceBranch`. The worktree is on the attached branch. The response can still have a `sourceBranch`, and it then says nothing about the worktree.

Failure:

```json
{"success": false, "error": "<text>", "errorCode": "<code>"}
```

Decide by `errorCode` first.

| `errorCode` | Meaning |
|---|---|
| `unsafe_path` | The daemon refuses a path. For example, `worktreePath` is relative, has a `..` component, is outside `baseRepo`, or exists. A `worktreeRoot` that is not safe gives this code too. So does a worktree that git registered in an unexpected place. |
| `symlinked_component` | A directory between `baseRepo` and the worktree is a symbolic link, for example a symlinked `.claude` or `.claude/worktrees`. |
| `not_a_repo` | `baseRepo` is not a git repository, or git itself does not accept it. A bare `baseRepo` gets this code too while the daemon works on its settings copy. |
| `nested_base_repo` | `baseRepo` is inside a directory of managed worktrees, or the daemon does not accept it as a trust root. |
| `mkdir_failed` | The daemon cannot create a directory for the worktree. |
| `worktree_add_failed` | `git worktree add` or the checkout failed. The text holds the message of git, or a refusal of the daemon that came before the add. |
| `repository_only_filter` | The checkout needs a git filter that only the git config of the repository defines. See [Git filters](#git-filters). |
| `timeout` | The deadline of `timeoutMs` expired. |

These eight are all the codes of this method.

If the daemon does not accept the git config of the repository, the code is `worktree_add_failed` and nothing is made. For example, the format version of the repository is not `0` or `1`. The texts are in [Repository config refusals](../PROTOCOL.md#repository-config-refusals).

Three failures of the settings copy give `worktree_add_failed` too, and nothing is made. The test of the git of the host failed. The call that asks git about the repository failed. Or the daemon cannot make the folder of the copy. See [The settings copy](#the-settings-copy).

A request with no `branchName` gets the JSON-RPC error `-32602`.

The exact texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).

## After a failure

What stays on disk depends on where the request failed.

A request can create three things outside the worktree, and no failure removes them:

- directories above the worktree
- the registrations directory `<git dir>/worktrees`
- with `worktreeRoot`, the marker file `.claude-managed-worktrees` at the `<directory>` level

On Windows a fourth thing can stay. If a step after a good add fails, a folder `.claude-new-worktree-<16 hex>` can stay in the git directory. A later create removes it once it is 1 hour old.

A request can also remove a stale registration of this worktree path before the add.

The table says what stays of the worktree itself: its directory, its registration and its branch.

| Failure | What stays of the worktree |
|---|---|
| A refusal of the request or of a path (`not_a_repo`, `nested_base_repo`, `symlinked_component`, and `unsafe_path` for a path) | Nothing. The daemon changed nothing on disk. |
| The daemon refuses before it runs `git worktree add` (`worktree_add_failed`) | Nothing. The daemon changed nothing on disk. |
| The daemon cannot create a directory (`mkdir_failed`) | Nothing. |
| `git worktree add` runs and fails (`worktree_add_failed`) | If the worktree directory is empty, the daemon removes it. Files that the failed add wrote stay. A registration and a branch that the add made stay too. |
| A test of the new worktree fails after the add. On Linux and macOS a test of its registration gives `unsafe_path`. If something replaced the worktree directory during the request, the code is `worktree_add_failed`. | Everything. The daemon rolls nothing back. The worktree directory, the registration and the branch stay. |
| The checkout fails, the filter rule refuses, or the deadline expires (`worktree_add_failed`, `repository_only_filter`, `timeout`) | The daemon removes the worktree directory and its registration. If this request created the branch, the daemon removes the branch too. The cases below the table are exceptions. |

`worktree_add_failed` and `unsafe_path` each have more than one row. The code alone does not say which row applies. The error text does, and so does the disk.

If the worktree directory stays, a second request with the same `worktreePath` gets `unsafe_path`, because the path exists.

A rollback leaves something in these cases:

- If the branch holds commits that no other ref reaches, the daemon keeps the branch. The response then has `"branchKept": true`.
- If a delete fails, the error text ends with a clause that says what remains. On Windows a registration that stays adds no clause.
- If something replaced the worktree directory, the rollback keeps the replacement and adds no clause. On Linux and macOS it also keeps a replaced registration directory ([D24](../DIVERGENCES.md#d24)).

## Git filters

A git filter is a program that git runs for a file of the checkout. If a file of the checkout uses a filter that only the git config of the repository defines, the request fails and the daemon rolls back:

```json
{"success": false, "error": "Claude doesn't run the \"<name>\" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter \"<name>\"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.", "errorCode": "repository_only_filter"}
```

A filter counts as defined where a `smudge` or a `process` key has a value. A filter that the user config, the system config or the environment of the daemon defines runs. If the repository defines that filter too, the values from outside the repository apply. If the repository adds a `process` key to a filter that has none outside it, the request fails with `worktree_add_failed`. With a git before 2.26, the daemon refuses a filter that a conditional include (`includeIf`) of the user config defines.

Three values of the repository pass: `smudge = cat` under each name, and `git-lfs smudge -- %f` and `git-lfs filter-process` under the name `lfs`. With `smudge = cat` the daemon starts no program. If the config of the repository itself names a program for git-lfs, the filter `lfs` does not run, wherever it is defined. A file under that filter then fails the request. A filter name with `=` in it, or one that is not valid UTF-8, fails the request with `worktree_add_failed`. So does a refused name of more than 200 bytes.

`"shadowDirOff": true` turns the rule off for one request. The daemon then runs the filter of the repository.

claustrum follows the reference build `5fd08069` here, with the differences that the record lists. The [measurement record](../record/git-worktree-create.md#git-filters-of-the-checkout) has the cells.

## The settings copy

The daemon keeps its own copy of the git settings of the repository, and its git calls read that copy. [The settings copy](../PROTOCOL.md#the-settings-copy) in the protocol reference has the states, the texts and the folder. These rules are for this method:

- If the directory `<git dir>/worktrees` is missing, the daemon makes it before the add. The directory stays after an add that fails. The new directory gets mode 0777 less the umask of the daemon. On Linux and macOS, with the value `group`, `true`, `all`, `world`, `everybody` or an octal number that git accepts, the daemon then sets the mode by that value. The [record](../record/git-settings-copy.md) has the table.
- If the repository has `extensions.worktreeConfig` and a file `<git dir>/config.worktree`, the new worktree gets its own `config.worktree` file. It holds the text of that file without `core.bare` and `core.worktree`.
- A bare `baseRepo` gets `not_a_repo`.
- On Windows the add writes the registration of the new worktree into a short-lived folder of the daemon. The daemon then copies it into `<git dir>\.claude-new-worktree-<16 hex>` and renames that folder to `<git dir>\worktrees\<name>`.
- On Windows, before that step, a create removes old entries of the git directory. Such an entry has a name that starts with `.claude-new-worktree-` and a write time of 1 hour or more. The request did not make those entries. A junction goes as a junction, and its target stays.

`"shadowDirOff": true` turns the copy off for one request, and none of these steps runs. The same holds on a host whose git does not work on such a copy. The [record of the copy](../record/git-settings-copy.md) has the cells.

## The copy of ignored files

After the checkout, the daemon copies two sets of files from `baseRepo` into the new worktree:

- The ignored files that the file `.worktreeinclude` at the root of the repository names. That file uses the syntax of `.gitignore`.
- The ignored files under `.claude/`.

Neither set includes `.claude/worktrees` or the session state of Claude under `.claude/`, for example `checkpoints`, `mailbox` and `scheduled_tasks.json`. A new worktree does not inherit the session state of the repository. The [measurement record](../record/git-worktree-create.md) lists each name.

A copy does not keep the mode of its source. The daemon creates each file with mode `0666` less its umask. So an executable file arrives without its execute bit, and a `0400` file gets a wider mode. The daemon also skips symbolic links. Name configuration files in `.worktreeinclude`, not secrets or scripts.

A copy that fails does not fail the request. If the deadline of `timeoutMs` expires during the copy, the request fails with `timeout`.

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| `worktreeRoot` | Supported. | Refused with `unsafe_path`. |
| A junction between `baseRepo` and the worktree | Does not apply. | Refused with `mkdir_failed`. |
| Tests of the new registration after `git worktree add` | They run. A failure gives `unsafe_path`. | They do not run. |
| A stale registration of the worktree path before the add | If it is the one stale entry and is not locked, the daemon removes it. | The daemon removes it, with no such test. |
| Where the add writes the registration | Through the link `worktrees` of the settings copy, into `<git dir>/worktrees`. | Into a short-lived folder of the daemon. The daemon moves it into the git directory. |
| Old `.claude-new-worktree-*` entries of the git directory | They stay. | A create removes each one that is 1 hour old or more. |

## Differences from the reference

claustrum is built to answer as the reference daemon does. The pinned build `89cb6289` runs a git filter that only the repository defines. claustrum follows `5fd08069` and refuses it, with the differences that the record lists: see [Git filters](#git-filters). claustrum also follows `5fd08069` in the refusal of a repository config: see [Repository config refusals](../PROTOCOL.md#repository-config-refusals). It follows `5fd08069` in the settings copy too, with the differences that the [record of the copy](../record/git-settings-copy.md#claustrum-and-5fd08069) lists. These entries of the divergence catalog apply to this method:

- [D2](../DIVERGENCES.md#d2): a delete refuses a path that is the home directory or holds it. That covers the Windows removal of old `.claude-new-worktree-*` entries.
- [D5](../DIVERGENCES.md#d5): an optional deadline on the git calls. It is off by default.
- [D24](../DIVERGENCES.md#d24): on Linux and macOS, a rollback keeps a registration directory that something replaced after the add.

## More detail

The order of the internal checks, the rules for unusual states of a repository and the measurements against the reference are in the [measurement record](../record/git-worktree-create.md). The tests of the repository pin those cases. Most readers do not need that page.
