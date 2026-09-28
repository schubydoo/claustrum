---
default: patch
---

`git.worktree_remove` now deletes the worktree and its entry itself and refuses symlink or file leaves, matching the measured `f6010b97` cases. Create refuses a junctioned `.claude` or `.claude\worktrees`, and remove refuses it too (D19).
