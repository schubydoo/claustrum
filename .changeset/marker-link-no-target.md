---
default: patch
---

A `.claude-managed-worktrees` link with no target above `baseRepo` or `path` now counts as a marker in `git.worktree_create`, `git.worktree_remove` and `git.list_branches`.
