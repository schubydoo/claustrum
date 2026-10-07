---
default: patch
---

`git.worktree_remove` now refuses a folder level that the daemon cannot read or search with the texts of `89cb6289` on Linux and macOS, and it deletes nothing there.
