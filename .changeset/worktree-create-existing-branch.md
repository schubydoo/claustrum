---
default: minor
---

git.worktree_create now accepts an `existingBranch` param that attaches the new worktree to a local branch that already exists. Its success result gains a `branch` field that names the checked-out branch, and the `git.worktree_create.existingBranch` capability is advertised. This matches reference build `19f30c46`.
