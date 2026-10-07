---
default: patch
---

On Linux and macOS `git.worktree_create` now follows the stale registration step and the rollback of `89cb6289`, apart from D24. The temporary index folder now starts with `claude-ssh-index-`.
