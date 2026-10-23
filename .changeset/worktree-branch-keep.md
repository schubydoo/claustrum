---
default: minor
---

`git.worktree_remove` and a `git.worktree_create` rollback now keep a branch that holds commits no other branch or remote-tracking ref reaches, and answer `"branchKept":true`.
