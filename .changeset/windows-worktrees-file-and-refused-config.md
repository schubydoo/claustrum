---
default: patch
---

On Windows git.worktree_create with a file or a junction at .git\worktrees, git.info on a folder whose stat fails with ERROR_INVALID_NAME and git.worktree_remove with a refused config now answer as the reference does.
