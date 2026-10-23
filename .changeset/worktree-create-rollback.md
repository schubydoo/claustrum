---
default: patch
---

On Linux and macOS `git.worktree_create` now checks the new worktree before the checkout. On every system it copies `config.worktree`, writes the index as a new file, rolls back more failures, and removes a stale registration only when it is the one unlocked stale entry. It keeps a replaced registration folder (D24).
