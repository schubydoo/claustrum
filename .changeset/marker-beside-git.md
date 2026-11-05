---
default: patch
---

A .claude-managed-worktrees marker in a folder that also holds a .git no longer refuses a baseRepo below it on git.worktree_create and git.worktree_remove, and git.list_branches lists the branches of a path below it.
