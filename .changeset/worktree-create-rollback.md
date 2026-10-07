---
default: patch
---

On Linux and macOS `git.worktree_create` removes a stale registration before the add and rolls back the registration as `89cb6289` does. The temporary index folder now starts with `claude-ssh-index-`.
