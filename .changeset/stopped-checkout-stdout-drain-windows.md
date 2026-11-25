---
default: patch
---

On Windows a git.worktree_create whose timeoutMs stops the checkout no longer waits for a process that holds only the standard output of git, as the reference does.
