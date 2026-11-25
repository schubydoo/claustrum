---
default: patch
---

On Windows git.worktree_remove without worktreeRoot, on a folder with no repository whose listing passes, now runs the listing and the rev-parse one time, not two times, as the reference does.
