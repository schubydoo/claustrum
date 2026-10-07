---
default: patch
---

On Linux and macOS, `git.worktree_create` and `git.worktree_remove` now refuse a `worktreeRoot` that is the file system root, with the measured text of `89cb6289`.
