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
| `timeoutMs` | no | A deadline for the whole request. With no value or `0` there is no deadline. |

Where the worktree can go:

- Without `worktreeRoot`, `worktreePath` must be an absolute path inside `baseRepo`. The usual place is `<baseRepo>/.claude/worktrees/<name>`.
- With `worktreeRoot`, `worktreePath` must be `<worktreeRoot>/<directory>/<name>`. The root must belong to the user of the daemon. Its group and other users must have no write access to it.

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
| `not_a_repo` | `baseRepo` is not a git repository. |
| `nested_base_repo` | `baseRepo` is inside a directory of managed worktrees. |
| `mkdir_failed` | The daemon cannot create the parent directory of `worktreePath`. |
| `worktree_add_failed` | `git worktree add` or the checkout failed. The text holds the message of git. |
| `timeout` | The deadline of `timeoutMs` expired. |

A request with no `branchName` gets the JSON-RPC error `-32602`.

The exact texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).

## After a failure

If the failure comes after `git worktree add`, the daemon removes the worktree directory and its registration. If this request created the branch, the daemon removes the branch too. Two cases differ:

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
