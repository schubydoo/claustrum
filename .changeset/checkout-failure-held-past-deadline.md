---
default: patch
---

On Linux and macOS git.worktree_create answers a checkout that failed before timeoutMs with its failure, not timeout. A child that holds the output of git past the deadline does not change that.
