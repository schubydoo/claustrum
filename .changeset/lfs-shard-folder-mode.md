---
default: patch
---

`git.worktree_create` makes a missing shard folder of the Git LFS store with mode 0777 less the umask, not a fixed 0755.
