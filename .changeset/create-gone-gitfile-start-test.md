---
default: patch
---

On Windows a git.worktree_create on a `.git` file that names a missing path now runs the start test before it answers, as the reference does.
