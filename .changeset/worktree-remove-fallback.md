---
default: patch
---

`git.worktree_remove` now removes the worktree directory itself when git refuses (a locked worktree, for example) and reports `success:false` with an `error` when that cleanup also fails, instead of answering `success:true` while leaving the directory in place.
