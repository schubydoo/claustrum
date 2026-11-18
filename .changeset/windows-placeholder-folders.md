---
default: patch
---

On Windows a .git folder with the ProjFS reparse tag works as a plain folder in git.info, git.status, git.list_branches and git.worktree_remove, and a worktree folder with it in git.status. A path below a folder that cannot be looked into is refused.
