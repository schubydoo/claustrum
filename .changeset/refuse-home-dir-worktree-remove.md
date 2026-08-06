---
default: patch
---

`git.worktree_remove` now refuses a `worktreePath` that is or contains the home directory, so a tilde or relative path can no longer reach the recursive delete it performs when git fails — the method previously had no path guard of any kind.
