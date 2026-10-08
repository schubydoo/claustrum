# git.worktree_create

`git.worktree_create` makes a linked git worktree for a session. It creates a new branch for the worktree, or it attaches the worktree to a branch that exists. Then it checks out the tracked files and copies some ignored files into the new worktree.

If a step fails after git made the worktree, the daemon removes what it made.

## Request

```json
{"baseRepo": "/repo", "branchName": "feature", "worktreePath": "/repo/.claude/worktrees/w1"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `baseRepo` | yes | The repository. |
| `branchName` | yes | The branch to create. It is required with `existingBranch` too. |
| `worktreePath` | yes | The directory of the new worktree. It must not exist. |
| `sourceBranch` | no | The branch that the new branch starts from. The default is the current branch. |
| `existingBranch` | no | A local branch to attach to. If it names no local branch, the daemon creates `branchName`. |
| `worktreeRoot` | no | A location outside the repository. Linux and macOS only. |
| `timeoutMs` | no | A deadline over the add, the checkout and the copy step. The checks before the add are not under it. With no value or `0` there is no deadline. |

Where the worktree can go:

- Without `worktreeRoot`, `worktreePath` must be an absolute path inside `baseRepo`. The usual place is `<baseRepo>/.claude/worktrees/<name>`.
- With `worktreeRoot`, `worktreePath` must be `<worktreeRoot>/<directory>/<name>`. The root must belong to the user of the daemon, and other users must have no write access to it. If the group of the root is the private group of that user, the group can have write access. The daemon judges the directories above the root too, and none of them can be inside a git checkout. The [measurement record](../record/git-worktree-create.md) has the exact rules.

## Response

Success:

```json
{"success": true, "path": "/repo/.claude/worktrees/w1", "sourceBranch": "main", "branch": "feature"}
```

`path` is `worktreePath` as sent. `branch` is the branch of the worktree. If the daemon attached to `existingBranch`, it is that branch. If not, it is `branchName`. On a detached HEAD with no `sourceBranch`, the response has no `sourceBranch`.

Failure:

```json
{"success": false, "error": "<text>", "errorCode": "<code>"}
```

Decide by `errorCode`, not by the text.

| `errorCode` | Meaning |
|---|---|
| `unsafe_path` | The daemon refuses a path. For example, `worktreePath` is relative, has a `..` component, is outside `baseRepo`, or exists. A `worktreeRoot` that is not safe gives this code too. So does a worktree that git registered in an unexpected place. |
| `symlinked_component` | A directory between `baseRepo` and the worktree is a symbolic link, for example a symlinked `.claude` or `.claude/worktrees`. |
| `not_a_repo` | `baseRepo` is not a git repository. |
| `nested_base_repo` | `baseRepo` is inside a directory of managed worktrees, or the daemon does not accept it as a trust root. |
| `mkdir_failed` | The daemon cannot create the parent directory of `worktreePath`. |
| `worktree_add_failed` | `git worktree add` or the checkout failed. The text holds the message of git. If the daemon did not let git start, the text holds the refusal of the daemon. |
| `timeout` | The deadline of `timeoutMs` expired. |

These seven are all the codes of this method.

A request with no `branchName` gets the JSON-RPC error `-32602`.

The exact texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).

## After a failure

What stays on disk depends on where the request failed.

| Failure | What the daemon leaves |
|---|---|
| A refusal of the request or of a path (`not_a_repo`, `nested_base_repo`, `symlinked_component`, and `unsafe_path` for a path) | Nothing. The daemon changed nothing on disk. |
| The daemon cannot create a directory (`mkdir_failed`) | No worktree and no branch. A directory that the daemon made above the worktree stays. With `worktreeRoot`, the marker file `.claude-managed-worktrees` at the `<directory>` level stays too. On Linux and macOS, a stale registration of this worktree path in `<baseRepo>/.git/worktrees` can be gone. |
| `git worktree add` fails (`worktree_add_failed`) | If the worktree directory is empty, the daemon removes it. Files that the failed add wrote stay. A registration and a branch that the add made stay too. |
| A test of the new registration fails after the add (`unsafe_path`, Linux and macOS) | Everything. The daemon rolls nothing back. The worktree directory, the registration and the branch stay. |
| The checkout fails, or the deadline expires (`worktree_add_failed`, `timeout`) | Nothing, in the usual case. The daemon removes the worktree directory and its registration. If this request created the branch, the daemon removes the branch too. |

If the worktree directory stays, a second request with the same `worktreePath` gets `unsafe_path`, because the path exists.

Two more cases apply to a rollback:

- If the branch holds commits that no other ref reaches, the daemon keeps the branch. The response then has `"branchKept": true`.
- If the daemon cannot remove something, the error text ends with a clause that says what remains.

## The copy of ignored files

After the checkout, the daemon copies two sets of files from `baseRepo` into the new worktree:

- The ignored files that the file `.worktreeinclude` at the root of the repository names. That file uses the syntax of `.gitignore`.
- The ignored files under `.claude/`, apart from `.claude/worktrees`.

A failed copy does not fail the request.

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| `worktreeRoot` | Supported. | Refused with `unsafe_path`. |
| A junction between `baseRepo` and the worktree | Does not apply. | Refused with `mkdir_failed`. |
| Tests of the new registration after `git worktree add` | They run. A failure gives `unsafe_path`. | They do not run. |

## Differences from the reference

claustrum is built to answer as the reference daemon does. These entries of the divergence catalog apply to this method:

- [D2](../DIVERGENCES.md#d2): claustrum never removes the home directory of the user.
- [D5](../DIVERGENCES.md#d5): an optional deadline for each git call. It is off by default.
- [D24](../DIVERGENCES.md#d24): a rollback keeps a registration directory that something replaced during the request.

## More detail

The order of the internal checks, the rules for unusual states of a repository and the measurements against the reference are in the [measurement record](../record/git-worktree-create.md). The tests of the repository pin those cases. Most readers do not need that page.
