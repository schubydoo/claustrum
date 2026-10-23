# git.info

`git.info` describes the git repository that holds a folder. It answers the top folder of the repository, the current branch, the GitHub name of the `origin` remote and the default branch of `origin`. It also reports the state of the daemon's [settings copy](../PROTOCOL.md#the-settings-copy). The method changes nothing in the repository and fetches nothing.

## Request

```json
{"path": "/repo/src"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | A folder of the repository. A subfolder gives the same `root`. Without `path`, the daemon answers for its own working directory. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |
| `shadowDirOff` | no | `true` turns off, for this request, the daemon's own read of the repository's git config and the settings copy. A git request that starts while such a request runs gets the same. Only the JSON value `true` counts. `"true"`, `1`, `null` and `false` do nothing and are no error. |

## Response

For a repository:

```json
{"isRepo": true, "repo": "repo", "branch": "main", "root": "/repo", "repoSlug": "acme/gizmo", "defaultBranch": "main", "settingsControl": "shadow"}
```

| Member | Meaning |
|---|---|
| `isRepo` | `true`. |
| `repo` | The last name of `root`. If that name starts with `-` or `+`, the member is left out, and the other members keep their order. |
| `branch` | The current branch. See [The branch](#the-branch). The member can be absent. |
| `root` | The absolute top folder of the repository. See [The root](#the-root). |
| `repoSlug` | `owner/repo` of a GitHub `origin`, or `""`. See [repoSlug](#reposlug). |
| `defaultBranch` | The branch that `refs/remotes/origin/HEAD` names, or `""`. See [defaultBranch](#defaultbranch). |
| `settingsControl` | The state of the settings copy. See [settingsControl](#settingscontrol). The member can be absent. |

The members always come in this order. `repoSlug` and `defaultBranch` are always present, also as empty strings.

If the daemon finds no repository, the response is not an error:

```json
{"isRepo": false, "repoSlug": "", "defaultBranch": "", "settingsControl": "shadow"}
```

The daemon gives this response in these cases:

- `path` is in no git repository.
- `path` does not exist. That is not an error.
- The repository has no work tree. Examples are a bare repository and a `path` inside a `.git` folder.
- The request has no `"shadowDirOff":true`, and the daemon finds no `git` on its `PATH`. It then starts no test of the settings copy.
- On Linux and macOS, `path` is a regular file. A file inside a repository gets the settings copy of that repository, but the answer stays the "no repository" body.
- git itself does not accept the repository.

## The branch

- The daemon reads the branch name from git, so an unborn branch has its name. An empty repository gives the name of its initial branch, for example `master`.
- A detached HEAD gives `"branch":"detached:<short-sha>"`.
- If git cannot name the branch, the member is absent. An example is a HEAD that git fails to resolve. git's error text never appears in the member.

## The root

`root` is the folder that holds the `.git` folder or `.git` file of the repository. For a linked worktree it is the folder of the worktree.

On Linux and macOS:

- `root` is `path` with its symbolic links resolved, then walked up to that folder.
- A component that is not a symbolic link keeps its letter case and Unicode form as sent.
- A symbolic link resolves before a later `..` removes it.
- `core.worktree`, and `GIT_DIR` and `GIT_WORK_TREE` in the daemon's environment, do not move `root`.

On Windows the daemon finds the folder the same way, and then asks git to confirm it. If git names the same folder, `root` is the answer of git as printed. Else it is the folder that the daemon found. In both cases the separators are forward slashes. A folder symbolic link resolves. A junction does not resolve. The comparison of the two folders is by file identity (claustrum's own, not measured).

## repoSlug

`repoSlug` is `owner/repo` from the URL of the `origin` remote, after any `url.<base>.insteadOf` rewrite of git. The daemon fills it only for a canonical `github.com` remote:

- The scheme must be `https`, `http`, `ssh`, `git`, or absent. An absent scheme is the scp-like form `[user@]host:owner/repo`. `git+ssh://` and `file://` give `""`.
- The host must equal `github.com`, in any letter case. `www.github.com`, `github.com.`, a port such as `github.com:443`, GitLab, Bitbucket and a self-hosted GitHub Enterprise give `""`. The daemon removes the user part.
- After one optional trailing `/` and one optional `.git`, the path must be exactly two segments that are not empty.
- The owner has letters and digits, with hyphens only inside. `ac-me` and `ac--me` pass. `-acme`, `acme-`, `acme_corp` and `acme.co` do not.
- The repo has letters, digits, `.`, `_` and `-`. It must not start with `-`, must not be `.` or `..`, and must not end in a lower-case `.wiki`. So `GIZMO.WIKI` and a repo named `wiki` pass.

With no `origin`, `repoSlug` is `""`.

## defaultBranch

`defaultBranch` is the branch that `refs/remotes/origin/HEAD` points to. The daemon tests the name:

- If `refs/remotes/origin/HEAD` is not set, the value is `""`.
- A name that starts with `-` gives `""`.
- Else the name must name a commit at `refs/remotes/origin/<name>`. A dangling `origin/HEAD`, and one that names a blob, give `""`.

The daemon fetches nothing for this member, also in a partial clone.

## settingsControl

`settingsControl` is always the last member. Its values are `"shadow"`, `"fallback-git"` and `"off-switch"`. No other method sends it.

- Before a start test of the daemon gave a state, the member is absent. So the first `git.info` of a daemon on a path that does not exist has no member.
- After the test, the "no repository" body has the member too.
- A request with `"shadowDirOff":true` gets `"off-switch"`, also as the first request of a daemon.
- An error reply has no member.

The section [The states and `settingsControl`](../PROTOCOL.md#the-states-and-settingscontrol) has the effect of each state and the time of the next test.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error. The texts are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).

| Code | Start of the text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or `path` has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | `the repository's git directory could not be trusted; git not run: ` | The daemon does not trust the git directory. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check). A `GIT_COMMON_DIR` in the daemon's environment turns this check off for this method. |
| `-32603` | `config-defined hooks could not be pinned off; git not run: ` | The daemon cannot list the git config, or it refuses its own `GIT_CONFIG_COUNT`. A config listing that runs 30 seconds is stopped, and the text goes on with `listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again`. A daemon `GIT_DIR` that names a worktree entry that is gone gets `the worktree entry this folder's .git names no longer exists` after the prefix. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check). This also comes for a folder that holds no repository. See [Hardened git calls](../PROTOCOL.md#hardened-git-calls) and [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment). |
| `-32603` | `this repository's git config cannot be used: ` or `a file in this repository's .git directory cannot be used: ` | The daemon read the git config of the repository and does not accept it. `"shadowDirOff":true` turns that read off. See [Repository config refusals](../PROTOCOL.md#repository-config-refusals). |
| `-32603` | `Claude could not test git on the remote machine (` | The start test of the settings copy failed. The daemon keeps the failure for 10 seconds. |
| `-32603` | `git could not check this repository on the remote machine: ` or `git and Claude disagree about which repository holds this folder: ` | The call that asks git about the repository failed, did not end in 30 seconds, or named another repository. |
| `-32603` | `git cannot run on this host; git not run: ` | A config listing, the start test or the call about the repository failed, and then a `git version` call failed too. |
| `-32603` | `Claude could not prepare its own files on the remote machine: ` | The daemon cannot make the folder of the settings copy for this repository. |
| `-32603` | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | A git call ran too long, and the daemon stopped it. While a request with `"shadowDirOff":true` runs, the text says `after 30 minutes` (claustrum's own, not measured). See [Time limits of git calls](../PROTOCOL.md#time-limits-of-git-calls). |

The section [The settings copy](../PROTOCOL.md#the-settings-copy) has the causes of the four texts of the copy.

## What changes on disk

The method changes nothing in the repository. The settings copy can add files to the folder `git-shadow` beside the socket: the folder of the daemon, and a copy of the settings of the repository. [The folders](../PROTOCOL.md#the-folders) has the names.

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| `root` | The folder found by the walk, in the native form. | Confirmed by git, with forward slashes. A junction in `path` does not resolve. |
| `path` is a regular file | The "no repository" body. | `-32603` with `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` |
| A junction from repository P into repository R | Does not apply. | With no `GIT_DIR` or `GIT_COMMON_DIR` in the daemon's environment, `root`, `repo` and `repoSlug` come from P. `branch` comes from R, and `defaultBranch` is `""`. |

## Differences from the reference

claustrum is built to answer as the reference daemon does, and it follows the pinned build `5fd08069`. No entry of the divergence catalog applies to this method. The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
