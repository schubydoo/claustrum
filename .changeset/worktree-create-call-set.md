---
default: patch
---

On Windows `git.worktree_create` now also removes an old registration of the path that differs only in letter case, and keeps it when the path that its record names exists (D27).
