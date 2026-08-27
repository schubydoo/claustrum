---
default: minor
---

`git.worktree_create`/`git.worktree_remove` by default confine worktrees to inside the repository, and `git.worktree_remove` refuses a locked worktree and prunes the registration on removal.
