---
default: patch
---

The Git LFS seed of `git.worktree_create` now stops at (`timeoutMs` + s) / 2 from the request start, with s the delay before the seed.
