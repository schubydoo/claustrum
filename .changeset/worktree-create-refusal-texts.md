---
default: patch
---

git.worktree_create matches the reference with no git on PATH. Under the off switch it also matches for a git-directory call that exits 1, a replaced leaf, and a missing worktrees folder on Linux and macOS.
