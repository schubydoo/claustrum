---
default: patch
---

If its first listing of the configuration fails, `git.worktree_remove` with no `worktreeRoot` and an existing worktree folder now lists it again. If the second listing fails too, the reply quotes its text after the words of the lock check.
