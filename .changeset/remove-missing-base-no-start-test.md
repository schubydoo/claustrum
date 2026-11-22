---
default: patch
---

A git.worktree_remove of a baseRepo that does not exist, with no repository above it on a network share and no daemon GIT_DIR, now runs no start test and no git version, as the reference does on Windows.
