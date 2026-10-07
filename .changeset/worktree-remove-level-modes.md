---
default: patch
---

On Linux and macOS, `git.worktree_remove` now refuses an unreadable or unsearchable folder from `worktreeRoot` down, or below `baseRepo`, with the measured texts of `89cb6289`. It deletes nothing there.
