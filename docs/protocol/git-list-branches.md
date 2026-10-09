# git.list_branches

`git.list_branches` lists the local branches of a git repository.

## Request

```json
{"path": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | A directory of the repository. A bare repository is accepted. Without `path`, the daemon answers for its own working directory. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |

## Response

```json
{"isRepo": true, "branches": ["feature", "main"]}
```

| Member | Meaning |
|---|---|
| `isRepo` | If the daemon accepts `path` as a repository, `true`. |
| `branches` | The short names of the local branches, in the byte order of the names. |

Each response has both members. The list has the local branches only. It has no remote-tracking branch and no tag, and no member says which branch is checked out. A repository with no commit gives `"isRepo": true` and an empty list.

If the daemon finds no repository, the response is not an error:

```json
{"isRepo": false, "branches": []}
```

`"isRepo": false` does not always mean that `path` holds no repository. The daemon gives this response in more cases than that. The main cases are these:

- `path` is not inside a git repository.
- `path` does not resolve. For example, it does not exist. That is not an error.
- `path` is inside a directory of managed worktrees. That is a path under `.claude/worktrees`, or under a directory that holds the marker file `.claude-managed-worktrees`. So the worktree of a session gives `"isRepo": false`. Send the path of the base repository to list its branches. A `baseRepo` member inside such a directory gives the same response.
- The daemon cannot find git on its `PATH`.
- Only with `-git-timeout`: the deadline stopped the git call that tests for a repository.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | A refusal text | The daemon refuses to run git for `path`. Three examples follow. It does not trust the git directory or the git configuration. It refuses its own git environment. git is on the `PATH`, and `git version` fails. The texts are in [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check), [Hardened git calls](../PROTOCOL.md#hardened-git-calls) and [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment). |
| `-32603` | The Go error of the git call, for example `exit status 128` | git cannot list the branches. For example, the file `packed-refs` is corrupt. |
| `-32603` | `signal: killed` | Only with `-git-timeout`: the deadline stopped the git call that lists the branches. This is the text of Linux and macOS. If the deadline stops the first git call of the method, the text is a refusal text that ends in `signal: killed`. |

A warning of git does not become a branch name. If git warns about a broken ref and still lists the other branches, the response holds those branches.

## Differences by system

On Windows, a `path` with a junction before its last component does not resolve, so the response is `"isRepo": false`. On Windows, a `path` that exists and in which git cannot start, for example a regular file, gets a `-32603` refusal. On Linux and macOS that path gives `"isRepo": false`.

## Differences from the reference

claustrum is built to answer as the reference daemon does. These entries of the divergence catalog apply to this method:

- [D5](../DIVERGENCES.md#d5): an optional deadline on the git calls. It is off by default.
- [D9](../DIVERGENCES.md#d9): a member of another `git.*` method with the wrong type gets `-32602`, although this method does not use it.

## More detail

The order of the internal checks and the measurements against the reference are in the [measurement record](../record/git-list-branches.md). Most readers do not need that page.
