---
default: patch
---

On Linux and macOS `git.worktree_remove` now refuses an unreadable or unsearchable folder from `worktreeRoot` down, or below `baseRepo`, and deletes nothing there.
