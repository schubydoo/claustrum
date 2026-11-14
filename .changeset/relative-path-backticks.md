---
default: patch
---

The relative worktreePath refusal of git.worktree_create and git.worktree_remove puts the path in backticks, and a remove of a gone worktree puts a trust refusal after a colon with no retry.
