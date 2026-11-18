---
default: patch
---

A `git.worktree_create` on a `baseRepo` that does not exist now answers `not_a_repo` with no git start test and no `git version` call.
