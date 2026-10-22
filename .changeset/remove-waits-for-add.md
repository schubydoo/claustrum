---
default: patch
---

`git.worktree_remove` deletes its worktree folder at once while a `git.worktree_create` in the same git directory runs `git worktree add`. The registration, the branch and the reply now wait for that add, as on `5fd08069` (Linux and macOS VMs). Windows is not measured.
