---
default: patch
---

A git.worktree_remove of a baseRepo that does not exist off a network share now runs no start test and no git version, as the reference does on Windows.
