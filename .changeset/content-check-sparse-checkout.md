---
default: patch
---

In `git.worktree_create` the content check leaves out a file that a sparse checkout did not write.
