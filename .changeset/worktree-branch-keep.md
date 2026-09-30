---
default: minor
---

`git.worktree_remove` and the `git.worktree_create` rollbacks now keep a branch that holds commits no other branch or remote-tracking ref reaches, and answer `"branchKept":true`, as `89cb6289` does.
