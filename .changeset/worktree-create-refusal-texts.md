---
default: patch
---

git.worktree_create matches the reference with no git on PATH on Linux and macOS, and on Windows for a missing baseRepo. Under the off switch it also matches for a git-directory call that exits 1, a replaced leaf, and a missing worktrees folder on Linux and macOS.
