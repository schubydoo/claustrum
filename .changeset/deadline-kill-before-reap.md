---
default: patch
---

On Linux and macOS the deadline stop of the git.worktree_create checkout sends its group kill only before git is reaped.
