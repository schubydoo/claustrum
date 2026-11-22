# git.list_branches

`git.list_branches` lists the local branches of a git repository.

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
| `branches` | The short names of the local branches, in byte order. |

Each response has both members. The list has no remote-tracking branch or tag, and no member names the checked-out branch. A repository with no commit gives `"isRepo": true` and an empty list.

### No repository

With no repository the response is no error:

```json
{"isRepo": false, "branches": []}
```

The main causes of `"isRepo": false`:

- `path` is not inside a git repository.
- `refs` of mode 0600 on Linux and macOS (`52d82f0a`).
- `path` is missing or does not resolve.
- `path` is inside a directory of managed worktrees: a path under `.claude/worktrees`, or under a directory with the marker file or folder `.claude-managed-worktrees` (`b3081b51`, measured on Linux). A marker beside a `.git` folder, also the repository's own, does not count, and the walk goes up (`b3081b51`, measured on Linux, macOS and Windows). A dangling `.git` link does the same (`4c111e86`, measured by a create on Linux, by a create on macOS with `worktreeRoot` and by a `worktreeRoot` remove on macOS). So do a `.git` file and looping, live or unreachable links (claustrum's own, not measured). A marker link counts, dangling (`4c111e86`, measured on Linux by a create) or live, looping or unreachable (claustrum's own, not measured). Windows, this method, a marker link on macOS and a remove on Linux or with no `worktreeRoot` are claustrum's own, not measured. A marker above the path as sent counts too (`b3081b51`, measured on Linux). That walk tests the marker only, not `.claude/worktrees`, and folds `..` by name. These two walk rules are claustrum's own, not measured. Send the base repository path to list branches. `git.list_branches` ignores a `baseRepo` member (measured on Linux). The daemon makes that test on its own working directory too.
- The daemon cannot find git on its `PATH`, and none that it uses on the login shell's PATH ([the git of the login shell's PATH](git-rules-calls.md#the-git-of-the-login-shells-path)), and `path` holds no repository (measured on Linux). A repository gets it with no test of the settings copy (`4c111e86`, measured on Linux, the reply on macOS). Windows is claustrum's own, not measured. The daemon still makes its folder, with the lock file only (`4c111e86`, measured on Linux). A request with `"shadowDirOff":true` gets it too (`4c111e86`, measured on Linux).
- A `.git` file names a gone git directory that is no registration (`4c111e86`, measured on Linux with git 2.43). git 2.56 gives the same, with and without `"shadowDirOff":true` (`4c111e86`, measured on Linux, macOS and Windows with git 2.56.1).
- git does not accept the repository, as for one of another user with no `safe.directory` line.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

### Shared refusals

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. [`params` rules](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `the repository's git directory could not be trusted; git not run: …` | The daemon does not trust the git directory. [Git-directory trust check](git-rules-trust.md#git-directory-trust-check). |
| `-32603` | `config-defined hooks could not be pinned off; git not run: …` | A config listing failed or passed a limit of the hook pins, the daemon refuses its own git environment, or a daemon `GIT_DIR` names a gone worktree entry. A listing stopped at 30 seconds goes on with `listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again`. [Refusal texts](git-rules-calls.md#refusal-texts) and [Git environment](git-rules-calls.md#the-daemons-own-git-environment). |
| `-32603` | `git cannot run on this host; git not run: …` | A config listing, a call of the start test or the call about the repository failed, and then `git version` failed too. [Failed listing](git-rules-trust.md#a-failed-configuration-listing), [The start test](git-rules-settings-copy.md#the-start-test) and [The call about the repository](git-rules-settings-copy.md#the-call-about-the-repository). |
| `-32603` | `this repository's git config cannot be used: …` or `a file in this repository's .git directory cannot be used: …` | The daemon read the git config of the repository, or a file of its git directory, and does not accept it. [Repository config refusals](git-rules-config.md#repository-config-refusals). |
| `-32603` | `Claude could not test git on the remote machine (…). Check free disk space and quota there, then try again.` | The start test of the settings copy failed. The daemon keeps the failure for 10 seconds. [The start test](git-rules-settings-copy.md#the-start-test). |
| `-32603` | `git could not check this repository on the remote machine: …` or `git and Claude disagree about which repository holds this folder: …` | The call that asks git about the repository failed, did not end in 30 seconds, or named another repository. [The call about the repository](git-rules-settings-copy.md#the-call-about-the-repository). |
| `-32603` | `Claude could not prepare its own files on the remote machine: …` | The daemon cannot prepare the folder of the settings copy, or `.probe`. [The folders](git-rules-settings-copy.md#the-folders). |

### Git failures

| Code | Text | Cause |
|---|---|---|
| `-32603` | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | A git call ran 2 minutes, and the daemon stopped it. While a `"shadowDirOff":true` request runs, that one included, the limit is 30 minutes, and the text says so (claustrum's own, not measured). [Time limits](git-rules-calls.md#time-limits-of-git-calls). |
| `-32603` | The Go error of the git call, for example `exit status 128` | git cannot list the branches. For example, the file `packed-refs` is corrupt. |

A daemon `GIT_COMMON_DIR` skips the trust check.

A warning of git does not become a branch name. If git warns about a broken ref and still lists the other branches, the response holds those branches.

## Differences by system

On Windows a junction before the last component of `path` passes, and git lists the branches (`b3081b51`, measured on Windows). [A junction `path`](git-rules-trust.md#how-the-daemon-finds-the-git-directory) has its own rule. On Windows `..` drops the name before it untested, so `<T>\missing\..` and `<T>\t.txt\..` list the branches of `<T>` (`b3081b51`, measured on Windows). On Windows, an existing `path` where git cannot start, such as a regular file, gets a `-32603` refusal. On Linux and macOS that path gives `"isRepo": false`. On Windows a `\\?\UNC\` `path` or a [refused share](git-rules-windows-paths.md#unc-refusals) gets a `-32603` refusal. A `path` below an [`IO_REPARSE_TAG_WIM` folder](git-rules-windows-paths.md#a-folder-with-a-reparse-tag-that-is-no-link) gets one too (`4c111e86`). So does a daemon [`GIT_ALTERNATE_OBJECT_DIRECTORIES`](git-rules-trust.md#the-object-sources-on-windows), unless the request sends `"shadowDirOff":true` (`4c111e86`). It comes after the trust check, the count check and the answer for a `path` with no repository, so such a `path` runs git and gets `"branches":[]`. It comes before the answer with no git on `PATH`, so a daemon with both gets the refusal (both claustrum's own, not measured).

## Differences from the reference

No divergence entry applies.
