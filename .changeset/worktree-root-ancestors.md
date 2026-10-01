---
default: patch
---

`git.worktree_create` now refuses, as the reference does, a `worktreeRoot` below a directory that another non-root user owns. It also refuses one below a shared-writable directory without the sticky bit.
