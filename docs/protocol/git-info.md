# git.info

`git.info` describes the git repository that holds a folder. It answers the top folder of the repository, the current branch, the GitHub name of the `origin` remote and the default branch of `origin`. It also reports the state of the daemon's [settings copy](git-rules-settings-copy.md#the-settings-copy). The method changes nothing in the repository and fetches nothing.

An untagged rule on this page matches build `5fd08069` or an earlier build. The rules of Request, Response, What changes on disk, The branch, repoSlug and defaultBranch, and the Linux and macOS part of The root, are measured, but the system was not recorded. A linked Errors row carries the system marks of its section. The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).

## Request

```json
{"path": "/repo/src"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `path` | no | A folder of the repository. A subfolder gives the same `root`. Without `path`, the daemon answers for its own working directory. [Path handling](../PROTOCOL.md#path-handling) has the rules for a leading `~`. |
| `shadowDirOff` | no | `true` turns off the daemon's own read of the repository's git config and its settings copy for this request. See [Turning the read off](git-rules-config.md#turning-the-read-off). |

## Response

For a repository:

```json
{"isRepo": true, "repo": "repo", "branch": "main", "root": "/repo", "repoSlug": "acme/gizmo", "defaultBranch": "main", "settingsControl": "shadow"}
```

| Member | Meaning |
|---|---|
| `isRepo` | `true`. |
| `repo` | The last name of `root`. If that name starts with `-` or `+`, the member is left out, and the other members keep their order (measured on Linux and macOS). |
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
- The repository has no work tree. Examples are a bare repository and a `path` inside a `.git` folder (measured on Linux and macOS).
- The request has no `"shadowDirOff":true`, and the daemon finds no `git` on its `PATH`. It then starts no test of the settings copy (measured on Linux).
- On Linux and macOS, `path` is a regular file. A file inside a repository goes through the checks and the settings copy of that repository. If those pass, the answer is the "no repository" body.
- git itself does not accept the repository.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error. The texts are in the [error-string catalog](errors.md#error-string-catalogue).

### Shared refusals

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or `path` has the wrong type. [The rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
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

A `GIT_COMMON_DIR` in the daemon's environment turns the git-directory trust check off for this method.

## The branch

- The daemon reads the branch name from git, so an unborn branch has its name. An empty repository gives the name of its initial branch, for example `master`.
- A detached HEAD gives `"branch":"detached:<short-sha>"`.
- If git cannot name the branch, the member is absent. An example is a HEAD that git fails to resolve (measured on Linux). On a detached HEAD whose short name git cannot give, the member is absent too (measured on Windows). git's error text never appears in the member (measured on Linux and Windows).

## The root

`root` is the folder that holds the `.git` folder or `.git` file of the repository. For a linked worktree it is the folder of the worktree.

On Linux and macOS:

- `root` is `path` with its symbolic links resolved, then walked up to that folder.
- A component that is not a symbolic link keeps its letter case and Unicode form as sent (measured on macOS).
- A symbolic link resolves before a later `..` removes it.
- `core.worktree`, and `GIT_DIR` and `GIT_WORK_TREE` in the daemon's environment, do not move `root`.

On Windows:

- The daemon finds the folder the same way, and then asks git to confirm it (measured on Windows). If git names the same folder, `root` is the answer of git as printed (measured on Windows). Else it is the folder that the daemon found (measured on Windows). In both cases the separators are forward slashes (measured on Windows).
- The walk resolves a folder symbolic link and no junction, so a `..` after a junction is taken by text (measured on Windows). When git confirms the folder, `root` is git's spelling, which can name the target of a junction (measured on Windows).
- The comparison of the two folders is by file identity (claustrum's own, not measured).

## repoSlug

`repoSlug` is `owner/repo` from the URL of the `origin` remote, after any `url.<base>.insteadOf` rewrite of git. The daemon fills it only for a canonical `github.com` remote:

- The scheme must be `https`, `http`, `ssh`, `git`, or absent. An absent scheme is the scp-like form `[user@]host:owner/repo`. An absent scheme also takes `host/owner/repo` (claustrum's own, not measured). `git+ssh://` and `file://` give `""`.
- The host must equal `github.com`, in any letter case. `www.github.com`, `github.com.`, GitLab, Bitbucket and a self-hosted GitHub Enterprise give `""`. The daemon removes the user part. A port such as `github.com:443` lands in the path, so `github.com:443/owner/repo` gives three segments and `""`.
- After any trailing `/` and one optional `.git`, the path must be exactly two segments that are not empty. More than one trailing `/` is claustrum's own (not measured).
- The owner has letters and digits, with hyphens only inside. `ac-me` and `ac--me` pass. `-acme`, `acme-`, `acme_corp` and `acme.co` do not.
- The repo has letters, digits, `.`, `_` and `-`. It must not start with `-`, must not be `.` or `..`, and must not end in a lower-case `.wiki`. So `GIZMO.WIKI` and a repo named `wiki` pass.

With no `origin`, `repoSlug` is `""`.

## defaultBranch

`defaultBranch` is the branch that `refs/remotes/origin/HEAD` points to. The daemon tests the name:

- If `refs/remotes/origin/HEAD` is not set, the value is `""`.
- A name that starts with `-` gives `""`.
- Else the name must name a commit at `refs/remotes/origin/<name>`. A dangling `origin/HEAD`, and one that names a blob, give `""` (measured on Linux and macOS).

The daemon fetches nothing for this member, also in a partial clone (measured on Linux).

## settingsControl

`settingsControl` is always the last member. Its values are `"shadow"`, `"fallback-git"` and `"off-switch"`. No other method sends it.

- Before a start test of the daemon gave a state, the member is absent. So the first `git.info` of a daemon on a path that does not exist has no member (measured on Linux).
- After a start test that passed, the "no repository" body has the member too (measured on Linux).
- A request with `"shadowDirOff":true` gets `"off-switch"`, also as the first request of a daemon (measured on Linux).
- An error reply has no member.

The section [The states and `settingsControl`](git-rules-settings-copy.md#the-states-and-settingscontrol) has the effect of each state and the time of the next test.

## What changes on disk

The settings copy can add files to the folder `git-shadow` beside the socket: the folder of the daemon, and a copy of the settings of the repository. [The folders](git-rules-settings-copy.md#the-folders) has the names.

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| `root` | The folder found by the walk, in the native form. | Confirmed by git, with forward slashes. The walk does not resolve a junction. |
| `path` is a regular file | The "no repository" body. | `-32603` with `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` |
| A junction from repository P into repository R | Does not apply. | With no `GIT_DIR` or `GIT_COMMON_DIR` in the daemon's environment, `root`, `repo` and `repoSlug` come from P. `branch` comes from R, and `defaultBranch` can be `""` (measured on Windows). |

## Differences from the reference

No entry of the divergence catalog applies to this method.
