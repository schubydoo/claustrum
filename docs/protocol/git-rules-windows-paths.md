# Windows link and share paths of the git methods

On Windows the git methods test the links, the shares and the reparse tags of a repository path before git runs on the repository. The object sources and the chain test are on [the trust page](git-rules-trust.md#the-object-sources-on-windows).

## A link to a network or device path

On Windows `b3081b51` refuses a repository path through a symlink or junction to a network or device path (measured on Windows). The targets are a UNC path, `\\.\NUL`, `\\?\Volume{guid}` and `\\?\GLOBALROOT`. The last two are refused too when they lead to a local folder. The path is `baseRepo` of `git.status` and `git.worktree_create`, and `path` of `git.info`. No git call runs on it, and the text is:

`` Claude doesn't follow `<path>` on a Windows host: it goes through a symlink or junction to a network or device path, so git wasn't run. Replace that link with a folder on a local drive. Or open the folder by its network path. ``

`git.status` and `git.info` answer `-32603`, and a create `"errorCode":"worktree_add_failed"` with no folder or branch. A SUBST or mapped drive and a UNC path with no link on a share that answers pass (measured on Windows). Claustrum's own, not measured: each path name is tested, a local link's target is not followed, and `git.status` tests `baseRepo` alone. Also claustrum's own, not measured: the `\\?\`, two-separator and relative targets, a refused `"shadowDirOff":true` request, the place after the chain test and the cleaned path.

## A folder with a reparse tag that is no link

These rows are measured on Windows on `4c111e86`. They set the raw tags with no ProjFS provider.

- A `.git` folder with `IO_REPARSE_TAG_PROJFS` is plain for `git.info`, `git.status`, `git.list_branches` and a remove. So is a worktree folder with it below `baseRepo` in `git.status`. A create in `shadow` makes a worktree there, where claustrum refuses. Its text is that of a config behind a link, because the held git directory takes a plain folder only (from the code).
- With no filter for `IO_REPARSE_TAG_WIM`, a look below such a folder fails. A repository below it gets ``Claude couldn't check that `<path>` stays on a local drive, so git wasn't run. Try again.`` on `git.info`, `git.status` and `git.list_branches`, with no git call on the repository. A `.git` with that tag gets it on all five methods, naming `<path>\.git\objects`. A remove puts `failed to remove worktree: could not check whether <worktreePath> is locked: ` before it. The log gets `[GitHandler] network-path check of "<path>": could not determine where the path's links point: The file cannot be accessed by the system.`, with `<path>` Go-quoted.

Claustrum's own, not measured: every other tag that is no name surrogate is no link, and only `ERROR_CANT_ACCESS_FILE` refuses. Also claustrum's own, not measured: one log line per request, the cleaned path in the text and the place of the remove test. The test looks at each name of the path, then at the `objects` folder of a `.git` folder there (claustrum's own, not measured).

## UNC refusals

On Windows these refuse the repository path of all five methods with no git call: `"success":false` on a remove, `worktree_add_failed` on a create, else `-32603`.

- `\\?\UNC\…`: `` Claude can't check where `<path>` leads because it doesn't start with a drive letter (C:\...) or a share (\\server\share\...), so git wasn't run. Open the folder again using a path that does. ``
- Missing share, also any other failed look (claustrum's own, not measured): `` Claude couldn't look at `<path>` to check it for links, so git wasn't run. Check that the network share is reachable and that your account can open that folder, then try again. ``, and the log gets `[GitHandler] looking for links in "<path>": "CreateFile <path>: The network name cannot be found."`
- Share denying every account: `` Claude couldn't check `<path>` for links because Windows didn't let your account read `<folder>`, so git wasn't run. Ask for read access to that folder. ``
- Directory link on a share: `` Claude doesn't follow links on a network share, and `<link>` is a symlink or junction, so git wasn't run. Open the folder it points to instead, or replace the link with a real folder. ``

`b3081b51` gives the first two texts, `4c111e86` the rest and the log line. `<folder>` is the parent of `<path>` (claustrum's own for a deeper path). A missing folder on a share that answers, a missing drive and an unresolved host name pass (measured on Windows). Claustrum's own, not measured: other `\\?\` and `\\.\` forms, a share root, an unreachable host and a path missing below a link pass. A junction or link above the last name counts, but not one below a name that fails its look (claustrum's own, not measured). Also claustrum's own, not measured: a refused `"shadowDirOff":true` request, the place of the test first, a forward slash as a separator, `UNC` in any letter case and `ERROR_NETWORK_ACCESS_DENIED` as denied.
