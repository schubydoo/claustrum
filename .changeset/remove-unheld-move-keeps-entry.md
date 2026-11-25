---
default: patch
---

On Windows a git.worktree_remove of a live worktree keeps both registrations when baseRepo leads to another folder at its registration delete. That holds only with no held git directory and a git directory reached through baseRepo's path.
