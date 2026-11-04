---
default: minor
---

git.worktree_create checks the config files of a new worktree against its commit after the checkout, and refuses and undoes the worktree with a worktree_verify code on a mismatch.
