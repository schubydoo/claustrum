---
default: patch
---

git.worktree_remove and the git.worktree_create rollbacks refuse a worktree folder that is or holds the home folder by folder identity, not only by path text.
