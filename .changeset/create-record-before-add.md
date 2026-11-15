---
default: patch
---

`git.worktree_create` records the git directory of `baseRepo` as a session folder right after `rev-parse --absolute-git-dir`, so a failed add or a later refusal keeps the record.
