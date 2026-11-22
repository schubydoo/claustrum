# Windows link and share paths of the git methods

On Windows the git methods test the links, the shares and the reparse tags of a repository path before git runs on the repository. The object sources and the chain test are on [the trust page](git-rules-trust.md#the-object-sources-on-windows). A rule here names its build, or it has the build of its section.

## A link to a network or device path

On Windows `b3081b51` refuses a repository path through a symlink or junction to a network or device path (measured on Windows). The targets are a UNC path, `\\.\NUL`, `\\?\Volume{guid}` and `\\?\GLOBALROOT`. The last two are refused too when they lead to a local folder. The path is `baseRepo` of `git.status` and `git.worktree_create`, and `path` of `git.info`. No git call runs on it, and the text is:

`` Claude doesn't follow `<path>` on a Windows host: it goes through a symlink or junction to a network or device path, so git wasn't run. Replace that link with a folder on a local drive. Or open the folder by its network path. ``

`git.status` and `git.info` answer `-32603`, and a create `"errorCode":"worktree_add_failed"` with no folder or branch. A SUBST or mapped drive and a UNC path with no link on a share that answers pass (measured on Windows). Claustrum's own, not measured: each path name is tested, a local link's target is not followed, and `git.status` tests `baseRepo` alone. Also claustrum's own, not measured: the `\\?\`, two-separator and relative targets, a refused `"shadowDirOff":true` request, the place after the chain test and the cleaned path.

## A folder with a reparse tag that is no link

These rows are measured on Windows on `4c111e86`. They set raw tags with no ProjFS provider.

- A `.git` folder with `IO_REPARSE_TAG_PROJFS` is plain for `git.info`, `git.status`, `git.list_branches` and a remove. So is a worktree folder with it below `baseRepo` in `git.status`. A create in `shadow` makes a worktree there. Claustrum holds such a `.git` as a folder ([the hold](git-worktree-create.md#the-held-git-directory), from the code).
- With no filter for `IO_REPARSE_TAG_WIM`, a look below such a folder fails. A repository there gets ``Claude couldn't check that `<path>` stays on a local drive, so git wasn't run. Try again.`` on `git.info`, `git.status` and `git.list_branches`, with no git call on it. A `.git` with that tag gets it on all five methods, naming `<path>\.git\objects`. A remove puts `failed to remove worktree: could not check whether <worktreePath> is locked: ` before it. The log gets `[GitHandler] network-path check of "<path>": could not determine where the path's links point: The file cannot be accessed by the system.`, with `<path>` Go-quoted.

Claustrum's own, not measured: four link tests and the hold of a create take every other tag that is no name surrogate as no link. They test the git directory, a `git.status` worktree, the config includes and a share path. An include below an `IO_REPARSE_TAG_CLOUD` folder is measured: it is followed ([`b3081b51`, measured on Windows](git-rules-config.md#class-1-the-git-config)). The other link tests still count such a folder as a link (claustrum's own, not measured).

Also claustrum's own, not measured:

- Only `ERROR_CANT_ACCESS_FILE` refuses, with one log line per request.
- A remove and a create below an `IO_REPARSE_TAG_WIM` folder refuse the same way.
- The test looks at each name of the path, then at `.git\objects` of a `.git` folder. A failed look at the `.git` itself passes.
- The cleaned path in the text and the test's place in each method.

A refused remove deletes nothing (from the code).

## UNC refusals

On Windows these refuse the repository path of all five methods with no git call on it: `"success":false` on a remove, `worktree_add_failed` on a create, else `-32603`.

- `\\?\UNC\…`: `` Claude can't check where `<path>` leads because it doesn't start with a drive letter (C:\...) or a share (\\server\share\...), so git wasn't run. Open the folder again using a path that does. ``
- Missing share (measured), also any other failed look (claustrum's own): `` Claude couldn't look at `<folder>` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again. ``, and the log gets `[GitHandler] looking for links in "<path>": "<error text>"`
- Denied share: `` Claude couldn't check `<path>` for links because Windows didn't let your account read `<folder>`, so git wasn't run. Ask for read access to that folder. ``
- Directory link on a share: `` Claude doesn't follow links on a network share, and `<link>` is a symlink or junction, so git wasn't run. Open the folder it points to instead, or replace the link with a real folder. ``

`b3081b51` gives the first two texts, `4c111e86` the rest and the log line. `<folder>` is the first folder below the share root, or the path, whose look fails; the log quotes its error (`4c111e86`, measured where every look on the share fails). A missing folder on a share that answers, a missing drive and an unresolved host name pass (measured). A `git.status` refused for a junction on a share as the last name makes no excludes read, and the next `git.info` makes it (`4c111e86`, measured on Windows). Claustrum's own, not measured: other `\\?\` and `\\.\` forms, a share root, an unreachable host and a path missing below a link pass, a junction or link above the last name counts, but not one below a name that fails its look, a refused `"shadowDirOff":true` request, the test placed first, a forward slash as a separator, `UNC` in any case, the excludes read of `git.status` at a symbolic link, at a link above the last name and at the other refusals and `ERROR_NETWORK_ACCESS_DENIED` as denied.

## A missing folder in a repository on a share

On Windows a missing path below a repository on a share gets [the call about the repository](git-rules-settings-copy.md#the-call-about-the-repository) in that repository (`4c111e86`, measured on Windows). The rows sent `<repo>\missing\deeper` of a repository that git answers as no repository. `git.info` and a create ask once, and a remove asks twice. The replies stay those of a missing path. `git.status` and `git.list_branches` make no call (measured on Windows). Claustrum's own, not measured: `<repo>` is the nearest folder above the path with a `.git` folder, and a `.git` file or link, another failed look there, or a `<repo>` that the trust check does not trust makes no call. For `git.info` and a create none of these runs the start test. A remove on an untrusted `<repo>` runs the start test in its gate first. The reply stays that of a missing path. A remove asks again only after a bad answer. Before the call the [start test](git-rules-settings-copy.md#the-start-test) runs, and so does the preparation of `.probe`, which removes its fixed leftover names after the home guard. After a good answer `git.info` and a create keep the old steps, and a remove takes those of a missing `baseRepo` off a share, with no listing and no `git version`. A failed call or start test answers its text.
