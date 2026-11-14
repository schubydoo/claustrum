---
default: patch
---

On Linux and macOS the checkout of `git.worktree_create` in the state `shadow` keeps no downloaded Git LFS object whose bytes do not match its oid.
