---
default: patch
---

git.worktree_create now accepts an `existingBranch` param that attaches the new worktree to an already-existing local branch, adds a `branch` field to its success result naming the checked-out branch, and advertises the `git.worktree_create.existingBranch` capability, matching reference build `19f30c46`.
