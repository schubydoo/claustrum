---
default: patch
---

The staged add of `git.worktree_create` gives its copy the modes of the reference on Linux, copies no entry `HEAD` over 4096 bytes, writes an empty `gitdir` for one it cannot read and refuses more than 4096 worktrees, and the settings copy writes a `HEAD` copy with mode 000, no bytes or an empty folder again.
