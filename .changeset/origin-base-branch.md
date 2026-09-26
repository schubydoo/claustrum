---
default: minor
---

`git.worktree_create` now matches reference build `f6010b97`: it can start from `origin/<sourceBranch>`, rolls back on a failed checkout or a timeout, and quotes git errors the same way.
