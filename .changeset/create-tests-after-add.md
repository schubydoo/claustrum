---
default: patch
---

On Linux and macOS `git.worktree_create` refuses more states of a new worktree before the checkout, removes a stale registration in the main repository of a linked `baseRepo`, and answers a `; retry` frame after a rollback.
