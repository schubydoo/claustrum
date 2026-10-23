---
default: patch
---

On Linux and macOS `git.worktree_create` now rolls back and answers a text that ends in `; retry` when the new registration names another path and git cannot report the top folder of the worktree.
