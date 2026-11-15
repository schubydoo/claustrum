---
default: patch
---

The staged add of `git.worktree_create` gives its copy the modes of the reference on Linux, copies a `gitdir` it cannot read, refuses more than 4096 worktrees, and writes a `HEAD` copy with mode 000, no bytes or an empty folder again.
