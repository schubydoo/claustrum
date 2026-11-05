---
default: patch
---

On Windows the stale registration step of git.worktree_create goes by the path, as the reference does after a junction takes the place of the git directory right before git names it.
