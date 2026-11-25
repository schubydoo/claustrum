---
default: patch
---

On Windows a git.worktree_remove of a live worktree keeps both registrations when baseRepo's folder is replaced before its registration is deleted. That holds only with no held git directory and a git directory reached through baseRepo's path.
