---
default: patch
---

When call 7 of the start test of the settings copy fails, the git methods answer the measured texts of `5fd08069`: the `git cannot run on this host` text when `git version` fails too, and the `Claude could not test git` text with the cause `this repository's git config cannot be used: <git text>` when it works. After a shadow verdict, a call about the repository that exits 128 with a broken config answers the first text too when `git version` fails, and so does `git.worktree_create` after a failed first checkout listing. `git.info` on a folder whose `.git` file names a missing path outside `<git dir>/worktrees` of an existing git directory now runs the start test, and its reply has the `settingsControl` member.
