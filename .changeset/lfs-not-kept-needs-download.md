---
default: patch
---

A `timeoutMs` deadline of `git.worktree_create` answers `timeout` when the private Git LFS store has no `incomplete` entry, also when a downloaded file cannot be kept.
