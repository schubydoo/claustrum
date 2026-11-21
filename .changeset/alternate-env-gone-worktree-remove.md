---
default: patch
---

On Windows a git.worktree_remove of a gone worktree folder now refuses a daemon GIT_ALTERNATE_OBJECT_DIRECTORIES and keeps its registration and branch, as the reference does.
