# git.list_branches

`git.list_branches` lists the local branches of a git repository.

## Request

```json
{"path": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | A directory of the repository. A bare repository is accepted. Without `path`, the daemon answers for its own working directory. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |
| `shadowDirOff` | no | `true` turns off the daemon's own read of the git config of the repository for this request, and its settings copy. Any other value does nothing and is no error. See [Repository config refusals](../PROTOCOL.md#repository-config-refusals) and [The settings copy](../PROTOCOL.md#the-settings-copy). |

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
- `path` is inside a directory of managed worktrees. That is a path under `.claude/worktrees`, or under a directory that holds the marker file `.claude-managed-worktrees`. So the worktree of a session gives `"isRepo": false`. Send the path of the base repository to list its branches. `git.list_branches` ignores a `baseRepo` member (cells LB1 and LB2 of round `task015-02`, Linux VM). The daemon makes that test on its own working directory too.
- The daemon cannot find git on its `PATH`.
- git itself does not accept the repository. For example, the repository belongs to another user and git has no `safe.directory` line for it.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | A refusal text | The daemon refuses to run git for `path`. Three examples follow. It does not trust the git directory or the git configuration. It refuses its own git environment. git is on the `PATH`, and `git version` fails. The texts are in [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check), [Hardened git calls](../PROTOCOL.md#hardened-git-calls) and [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment). |
| `-32603` | `this repository's git config cannot be used: …` or `a file in this repository's .git directory cannot be used: …` | The daemon read the git configuration of the repository itself and does not accept it. For example, the format version of the repository is not `0` or `1`. The second text is for a file of the git directory. `"shadowDirOff": true` in the request turns that read off. The protocol reference has [the texts](../PROTOCOL.md#repository-config-refusals). |
| `-32603` | `Claude could not test git on the remote machine (…). Check free disk space and quota there, then try again.` | The daemon tests once whether the git of the host works on its settings copy. That test failed. If its call 7 failed and `git version` fails too, the text is `git cannot run on this host; git not run: …` instead. The daemon keeps the failure for 10 seconds. See [The settings copy](../PROTOCOL.md#the-settings-copy). |
| `-32603` | `git could not check this repository on the remote machine: …`, or `git and Claude disagree about which repository holds this folder: …` | Before its other git calls the daemon asks git about the repository. That call failed, did not end in 30 seconds, or named another repository. After an exit 128 with a broken config and a failed `git version`, the text is `git cannot run on this host; git not run: …` (cell E8m of round `task083-02`, Linux VM). |
| `-32603` | `Claude could not prepare its own files on the remote machine: …` | The daemon cannot make the folder of its settings copy for this repository. |
| `-32603` | The Go error of the git call, for example `exit status 128` | git cannot list the branches. For example, the file `packed-refs` is corrupt. |
| `-32603` | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | A git call ran 2 minutes, and the daemon stopped it, as `5fd08069` does. See [Time limits of git calls](../PROTOCOL.md#time-limits-of-git-calls). |

A warning of git does not become a branch name. If git warns about a broken ref and still lists the other branches, the response holds those branches.

## Differences by system

On Windows, a `path` with a junction before its last component does not resolve, so the response is `"isRepo": false`. On Windows, a `path` that exists and in which git cannot start, for example a regular file, gets a `-32603` refusal. On Linux and macOS that path gives `"isRepo": false`.

## Differences from the reference

claustrum is built to answer as the reference daemon does. The pinned build `89cb6289` has no settings copy, and it answers a request with a failed test normally. claustrum follows `5fd08069` there. No entry of the divergence catalog applies to this method.
