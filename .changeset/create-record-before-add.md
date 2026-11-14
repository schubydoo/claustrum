---
default: patch
---

`git.worktree_create` records the git directory of `baseRepo` as a session folder before the add, so a failed add keeps the record.
