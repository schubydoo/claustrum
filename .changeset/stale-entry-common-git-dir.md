---
default: patch
---

`git.worktree_create` now looks for a stale registration in the common git directory, so a create with a separate git directory or in a submodule succeeds over a stale entry. On Windows a locked entry stays, and so do two stale entries.
