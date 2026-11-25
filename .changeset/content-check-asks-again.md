---
default: patch
---

The content check of git.worktree_create now asks git about the repository again when its kept answer is 5 s old, as the reference does on Linux.
