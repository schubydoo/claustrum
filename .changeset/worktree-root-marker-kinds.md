---
default: patch
---

`git.worktree_create` with `worktreeRoot` now refuses a root below a `.claude-managed-worktrees` folder or dangling link, as it already did below a marker file.
