# git.list_branches

`git.list_branches` lists the local branches of a git repository. `"isRepo": false` does not always mean that the path holds no repository.

An untagged rule on this page matches build `5fd08069` or an earlier build. Request, Response and the packed-refs and broken-ref rules have no recorded system. Linked Errors rows carry the system marks of their section.

## Request

```json
{"path": "/repo"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | A directory of the repository. A bare repository is accepted. Without `path`, the daemon answers for its own working directory. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |
| `shadowDirOff` | no | `true` turns off the daemon's read of the repository's git config and its settings copy for this request ([Turning the read off](git-rules-config.md#turning-the-read-off)). |

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

`"isRepo": false` has more causes than a missing repository. The main ones:

- `path` is not inside a git repository.
- `path` does not resolve. For example, it does not exist. That is not an error.
- `path` is inside a directory of managed worktrees. That is a path under `.claude/worktrees`, or under a directory that holds the marker file `.claude-managed-worktrees`. So the worktree of a session gives `"isRepo": false`. Send the path of the base repository to list its branches. `git.list_branches` ignores a `baseRepo` member (measured on Linux). The daemon makes that test on its own working directory too.
- The daemon cannot find git on its `PATH`, and `path` holds no repository (measured on Linux). A request with `"shadowDirOff":true` gets that answer from the failed listing too (from the code, not measured). Else a repository gets the failure of the daemon's first start test (from the code, not measured).
- git itself does not accept the repository. For example, the repository belongs to another user and git has no `safe.directory` line for it.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

### Shared refusals

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. [The rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `the repository's git directory could not be trusted; git not run: …` | The daemon does not trust the git directory. [Git-directory trust check](git-rules-trust.md#git-directory-trust-check). |
| `-32603` | `config-defined hooks could not be pinned off; git not run: …` | A config listing failed or passed a limit of the hook pins, the daemon refuses its own git environment, or a daemon `GIT_DIR` names a gone worktree entry. A listing stopped at 30 seconds goes on with `listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again`. [Refusal texts](git-rules-calls.md#refusal-texts) and [The daemon's own git environment](git-rules-calls.md#the-daemons-own-git-environment). |
| `-32603` | `git cannot run on this host; git not run: …` | A config listing, the read of the test repository's config or the call about the repository failed, and then `git version` failed too. [A failed configuration listing](git-rules-trust.md#a-failed-configuration-listing), [The start test](git-rules-settings-copy.md#the-start-test) and [The call about the repository](git-rules-settings-copy.md#the-call-about-the-repository). |
| `-32603` | `this repository's git config cannot be used: …` or `a file in this repository's .git directory cannot be used: …` | The daemon read the git config of the repository, or a file of its git directory, and does not accept it. [Repository config refusals](git-rules-config.md#repository-config-refusals). |
| `-32603` | `Claude could not test git on the remote machine (…). Check free disk space and quota there, then try again.` | The start test of the settings copy failed. The daemon keeps the failure for 10 seconds. [The start test](git-rules-settings-copy.md#the-start-test). |
| `-32603` | `git could not check this repository on the remote machine: …` or `git and Claude disagree about which repository holds this folder: …` | The call that asks git about the repository failed, did not end in 30 seconds, or named another repository. [The call about the repository](git-rules-settings-copy.md#the-call-about-the-repository). |
| `-32603` | `Claude could not prepare its own files on the remote machine: …` | The daemon cannot prepare the folder of the settings copy, or `.probe`. [The folders](git-rules-settings-copy.md#the-folders). |

### Git failures

| Code | Text | Cause |
|---|---|---|
| `-32603` | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | A git call ran 2 minutes, and the daemon stopped it. While a `"shadowDirOff":true` request runs, that one included, the limit is 30 minutes, and the text says so (claustrum's own, not measured). [Time limits of git calls](git-rules-calls.md#time-limits-of-git-calls). |
| `-32603` | The Go error of the git call, for example `exit status 128` | git cannot list the branches. For example, the file `packed-refs` is corrupt. |

A daemon `GIT_COMMON_DIR` skips the trust check.

A warning of git does not become a branch name. If git warns about a broken ref and still lists the other branches, the response holds those branches.

## Differences by system

On Windows, a `path` with a junction before its last component does not resolve, so the response is `"isRepo": false`. On Windows, a `path` that exists and in which git cannot start, for example a regular file, gets a `-32603` refusal. On Linux and macOS that path gives `"isRepo": false`.

## Differences from the reference

No divergence entry applies.
