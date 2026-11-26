---
default: patch
---

On Windows a git.worktree_create in the state fallback-git now runs the rev-parse --show-toplevel pair before the add, as the reference does.
