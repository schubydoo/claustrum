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
| `sourceBranch` | no | The name that the new branch starts from. The default is the current branch. An attach to `existingBranch` does not use it. |
| `existingBranch` | no | A local branch to attach to. If it names no local branch, the daemon creates `branchName` instead. If the attach fails, the daemon does the same. |
| `worktreeRoot` | no | A location outside the repository. Windows refuses it with `unsafe_path`. |
| `timeoutMs` | no | A deadline that starts at the add. It stops the read-tree call. The daemon tests it after the add and the copy step. No value or `0` sets none. |
| `shadowDirOff` | no | `true` turns off, for this request, the filter rule of the checkout, the daemon's own read of the repository's git config, and the settings copy. Git requests that start meanwhile get the same. A command that starts meanwhile gets no copy (Linux VM). Other values do nothing. See [Repository config refusals](../PROTOCOL.md#repository-config-refusals). |

Where the worktree can go:

- Without `worktreeRoot`, `worktreePath` must be an absolute path inside `baseRepo`. The usual place is `<baseRepo>/.claude/worktrees/<name>`.
- With `worktreeRoot`, `worktreePath` must be `<worktreeRoot>/<directory>/<name>`. A root that exists must belong to the user of the daemon. Its mode must have no write bit for others, or for a group other than the private group of that user.
- Neither a component of the location nor the `<directory>` can be `.git` or `.claude-managed-worktrees`, in any letter case. The location must not resolve to `/`. It must not lie in a folder with a `.git` entry, the git directory of the repository, or a session folder. The `<directory>` level must not hold the repository or be writable by every user. `5fd08069` refuses so (rounds `tier0-20` and `tier0-22`, Linux VM). Not measured: `.claude-managed-worktrees` in the location, a reserved `<name>`, and the owner and group tests of the `<directory>` level. claustrum refuses all of them.

## Response

Success:

```json
{"success": true, "path": "/repo/.claude/worktrees/w1", "sourceBranch": "main", "branch": "feature"}
```

`path` is `worktreePath` as sent, after the `~` expansion. `branch` is the branch of the worktree. If the daemon attached to `existingBranch`, it is that branch. If not, it is `branchName`.

If the daemon created a branch, `sourceBranch` is the name that the start commit came from. The daemon reads that name as two refs: the local branch `<name>` and the remote-tracking ref `origin/<name>`. It fetches nothing. If both exist, it selects `origin/<name>` in most states. The response holds the name as sent in each case.

If the value as sent named neither a local branch nor a branch of `origin`, that is not an error. The new branch then starts at HEAD, and the response holds the current branch, not the value as sent. On a detached HEAD, the response then has no `sourceBranch`.

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
| `worktree_add_failed` | `git worktree add`, the checkout or the `config.worktree` step failed. The text holds the message of git, or a refusal of the daemon that came before the add. A `git worktree add` that runs 2 minutes is stopped. |
| `repository_only_filter` | The checkout needs a git filter that only the git config of the repository defines. See [Git filters](#git-filters). |
| `timeout` | The deadline of `timeoutMs` expired. |

If the daemon does not accept the git config of the repository, the code is `worktree_add_failed` and nothing is made. The texts are in [Repository config refusals](../PROTOCOL.md#repository-config-refusals).

Three failures of the settings copy give `worktree_add_failed` too, and nothing is made: a failed test of the host git, a failed call about the repository, or no folder for the copy.

A request with no `branchName` gets the JSON-RPC error `-32602`.

The exact texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).

## After a failure

A request can create four things outside the worktree, and no failure removes them:

- directories above the worktree
- the registrations directory `<git dir>/worktrees`
- with `worktreeRoot`, the marker file `.claude-managed-worktrees` at the `<directory>` level
- with `worktreeRoot`, the folder of a gone worktree that holds the root (`5fd08069`, cell C-W8 of round `tier0-20`, Linux VM)

A request can also remove a stale registration of this worktree path before the add. On Linux and macOS that covers the main repository of a linked `baseRepo`. Two creates through a repository and its linked worktree add in turn (`5fd08069`, cell T1 of round `tier0-11`, Linux VM).

| Failure | What stays of the worktree |
|---|---|
| A refusal of the request or of a path (`not_a_repo`, `nested_base_repo`, `symlinked_component`, and `unsafe_path` for a path) | Nothing, except the folder of a gone worktree above. |
| The daemon refuses before it runs `git worktree add` (`worktree_add_failed`) | Nothing. |
| The daemon cannot create a directory (`mkdir_failed`) | Nothing. |
| `git worktree add` runs and fails (`worktree_add_failed`) | If the worktree directory is empty, the daemon removes it. Files that the failed add wrote stay. A registration and a branch that the add made stay too. |
| A test of the new worktree fails after the add. The tests come before the checkout and give `unsafe_path`. A worktree directory replaced during the checkout, or on Windows, gives `worktree_add_failed`. | Everything, except an empty worktree directory. In cell S2 on Windows nothing stays. |
| The checkout or the `config.worktree` step fails, the filter rule refuses, or the deadline expires (`worktree_add_failed`, `repository_only_filter`, `timeout`). Or the text ends with `; retry`. | The daemon removes the worktree directory and its registration. If this request created the branch, the daemon removes the branch too. |

A rollback leaves something in these cases:

- If the branch holds commits that no other ref reaches, the daemon keeps the branch. The response then has `"branchKept": true`.
- After a failed `config.worktree` step, a refused second config read keeps the branch without `branchKept` (round `task007-01`, Linux and macOS VMs).
- If a delete fails, the error text ends with a clause that says what remains. On Windows a registration that stays adds no clause, unless the create moved it.
- If something replaced the worktree directory, the rollback keeps the replacement and adds no clause. It also keeps a replaced registration directory ([D24](../DIVERGENCES.md#d24)).

## Git filters

In the state `shadow`, if a file of the checkout uses a filter that only the repository config defines, the request fails:

```json
{"success": false, "error": "Claude doesn't run the \"<name>\" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter \"<name>\"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.", "errorCode": "repository_only_filter"}
```

In the state `fallback-git` git runs the filters, with no rule of this section (cells R-Q4f and R-Q4g of round `task084-01`, Linux VM). Files that the repository config includes count as it, also through `includeIf`, whatever its condition (cells F1 and F1b of round `task084-02`, F1c and F1d of `task084-04`). A filter counts as defined where its `smudge` or `process` key has a value. With a settings copy a last `smudge` or `process` line with no `=` in .git/config counts too, whatever an includeIf file says (cells F5 and F6 of round `task073-02`, Linux VM, G5a and G5b of `task073-07`, macOS VM). There a user `process = cat` is no outside definition, so a repository `smudge` command is refused (cell F4 of round `task073-02`, Linux VM). A filter of the user or system config or the daemon environment runs with its values, also if the repository defines it, except in that `process = cat` case. With a user `lfs.storage` the checkout gets no `lfs.storage` item of the daemon (round `task008-01`, Linux, macOS and Windows VMs). A failing user `smudge` fails the request where the repository adds only `required = true`, `yes` or `1`.

Three values of the repository pass: `smudge = cat` under each name, and `git-lfs smudge -- %f` and `git-lfs filter-process` under the name `lfs`. With a settings copy a `smudge` value that does not pass refuses the filter, in .git/config or in an `includeIf` file, in either order. In .git/config only the last value counts. A filter whose values are all `cat` gets no item. The cells are F1e to F1i and Q8a and Q8b of rounds `task084-05` to `task084-07` (Linux VM). A plain include file and `config.worktree` count the same way (not measured). With a settings copy a refused name that is not valid UTF-8 gets git's checkout text less those bytes on Linux, and success on Windows (cells F12 and F13 of rounds `task073-02` and `task073-06`, Linux and Windows VMs). The [error-string catalog](../PROTOCOL.md#error-string-catalogue) has the other refusals of the rule.

With a settings copy `git-lfs smudge --skip %f` passes under `lfs` too. A repository-only `lfs` filter with the `git lfs install` lines gets those lines as items. A git-lfs program key of the repository refuses nothing, and its program does not run. A failed checkout text with `smudge filter lfs failed` gets a note about ssh, credential and proxy settings after it. The cells are K1 to K8 and L1 to L4 of round `task081-01` (Linux VM). With no settings copy such a key turns the filter `lfs` off (claustrum's own).

## The settings copy

The daemon keeps its own copy of the repository's git settings, and its git calls read it. [The settings copy](../PROTOCOL.md#the-settings-copy) in the protocol reference has the states, the texts and the folder. These rules are for this method:

- If the directory `<git dir>/worktrees` is missing, the daemon makes it before the add, with mode 0777 less its umask. It stays after a failed add. On Linux and macOS, the last `core.sharedrepository` line of the copy then sets its mode, unless that line is `umask` (rounds `tier0-14` to `tier0-16`, Linux VM). macOS is not measured.
- With `extensions.worktreeConfig` the new worktree gets the `config.worktree` file of `<git dir>`, or of a linked `baseRepo`, less `core.bare` and `core.worktree`.
- A bare `baseRepo` gets `not_a_repo`.
- Under an off-switch hold the checkout has its index in `claude-ssh-index-<n>` of the temporary folder, after one listing (Linux VM, Windows and macOS C1 and C2, `task105-01` to `task105-07`, `task105-10`).
- On Windows git writes the registration into a short-lived folder of the daemon, which copies it into `<git dir>\.claude-new-worktree-<16 hex>` and renames that folder to `<git dir>\worktrees\<name>`. A held folder gets 2 s of retries, also past `timeoutMs`. A rename that still fails gets `worktree_add_failed`, and the rollback removes the folder too (`5fd08069`, round `task009-01`). The name is the first free one of `<name>`, `<name>1` to `<name>99`. A failed `.git` write (cell S8v) and a rollback (cells S14a and, in round `task087-03`, B6w) remove the moved registration by its name, and `config.worktree` and the index are new files there (`5fd08069`, round `task087-01`).
- On Windows, before that step, a create removes old entries of the git directory. Their name starts with `.claude-new-worktree-`, and their write time is 1 hour or more. A junction goes as a junction, and its target stays.
- In the state `shadow`, a failed `rev-parse --absolute-git-dir` stops the create with `git worktree add failed: cannot locate the repository's git directory: exit status 128` (`5fd08069`, cell V12 of round `tier0-10`, Linux VM). macOS is not measured.
- On Windows, a step before the add that fails gives `git worktree add failed: cannot prepare the repository's worktrees directory: <cause>`, and no `worktree add` runs. On Linux and macOS a symbolic link, a file or a folder that cannot be opened at `worktrees` does the same. `5fd08069` answers so (rounds `tier0-10`, `tier0-24` and `tier0-25m`, Linux and macOS VMs).
- On Linux and macOS a linked `baseRepo` whose entry in `<git dir>/worktrees` is a symbolic link gets the trust refusal as a failed add. Without `worktreeRoot` its `.claude/worktrees` is made (`5fd08069`, cell C7 of round `tier0-24`, Linux VM, with `extensions.worktreeConfig` and a `config.worktree` file). Other forms are not measured.

`"shadowDirOff": true` turns off the copy and the steps that need it. With it a link or file at `worktrees` refuses the add after its listing (`5fd08069`, rounds `tier0-24`, `tier0-25m` and `tier3-02`, Linux, macOS and Windows VMs). A file there on Windows is not measured.

## The held git directory

In the state `shadow` a create holds the git directory of `baseRepo` open from its gate to its reply.

- The git directory must be a real directory at the gate. On Windows a junction or a directory symbolic link there is refused, on every git method without `"shadowDirOff": true`. With that member both link kinds pass, and git works in the folder behind the link, as on `5fd08069` (Windows VM). On Linux and macOS a `.git` that is a symbolic link before the request is resolved first, and the daemon holds its target. A change of that link is a change of the path too.
- The daemon's own deletes, renames, new folders and file writes inside the git directory go through the held folder. A create whose gate ran under a hold takes the folder at a later gate before its add. That is claustrum's own: in cells C4 and C5 of round `task105-04` (Linux VM) `5fd08069` names the copy after it. A folder that is put at the path while the create runs gets none of them, except the directory that the hold moves to.
- The hold stops no rename of the git directory by another process.
- The steps of the create test that the path still names the held folder. A swap after `rev-parse --absolute-git-dir` and before the add is refused before the add.
- A swap after the read-tree call fails the create, and the registration gets no index. On Windows a swap after the first listing of the checkout fails it too. The text starts with `git worktree add failed (checkout): `. The registration and the branch then stay, and the text says so.
- On Linux and macOS two steps move the hold to a copy of the git directory, and `5fd08069` goes on in it. A link to the copy is put there after the first call or after `rev-parse --abbrev-ref HEAD` (Linux and macOS VMs). Or a real copy is put there while `git worktree add` runs (cell C6 of rounds `tier0-24` and `tier0-25m`, Linux and macOS VMs).
- On Linux and macOS, a `.git` that became a file with `gitdir: <another git directory>` after `rev-parse --absolute-git-dir` gives `git worktree add failed: cannot locate the repository's git directory: lstat <baseRepo>/.git/commondir: not a directory`.
- On Linux and macOS, a `.git` that is a symbolic link to a copy of the git directory right after a good `worktree add` gives `refusing to create worktree: <leaf> was not populated by git worktree add` with `unsafe_path`. Nothing is rolled back, and the `.git` file of the new worktree keeps the text that git wrote.
- By the path stay: git itself, a request with `"shadowDirOff": true` or one that starts while such a request runs, until a later gate before its first good add, and a host in the state `fallback-git` or with no run folder. So does the step for an old registration when its `worktrees` folder is not in the held folder, or before that gate.

## The copy of ignored files

After the checkout, the daemon copies two sets of files from `baseRepo` into the new worktree:

- The ignored files that the root file `.worktreeinclude` names. That file uses the syntax of `.gitignore`.
- The ignored files under `.claude/`.

Neither set includes `.claude/worktrees` or the session state of Claude under `.claude/`, for example `checkpoints`, `mailbox` and `scheduled_tasks.json`.

A copy does not keep the mode of its source. The daemon creates each file with mode `0666` less its umask. The daemon also skips symbolic links. A copy does not replace a file or a link that the checkout made.

A copy that fails does not fail the request. If the deadline of `timeoutMs` expires during the copy, the request fails with `timeout`. A scan of ignored files stops at 10 minutes, and nothing is copied ([Time limits of git calls](../PROTOCOL.md#time-limits-of-git-calls)).

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| A junction between `baseRepo` and the worktree | Does not apply. | Refused with `mkdir_failed`. |
| A stale registration of the worktree path before the add | If it is the one stale entry and is not locked, the daemon removes it. | The daemon removes it with no such test, and ignores letter case (`5fd08069`, cells R-Q4i and R-Q4i2 of round `task084-01`). It keeps an entry whose record path exists (D27). Without a held git directory this is not measured. |

## Differences from the reference

These entries of the divergence catalog apply to this method:

- [D2](../DIVERGENCES.md#d2): a delete refuses a path that is the home directory or holds it.
- [D24](../DIVERGENCES.md#d24): a rollback and the index placement leave a registration directory that something replaced. A Windows rollback runs no git call after a `.git` file swap (X1, X1b and X1c).
