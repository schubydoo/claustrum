---
default: patch
---

On Linux and macOS `git.worktree_create` makes a missing shard folder of `<git dir>/lfs/objects` with mode 0777 less the umask, not a fixed 0755.
