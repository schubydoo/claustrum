---
default: patch
---

On Linux and macOS, git.worktree_remove deletes an existing worktree folder and succeeds under a daemon GIT_DIR at a missing path, also beside an unlocked .git folder.
