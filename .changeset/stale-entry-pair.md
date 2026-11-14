---
default: patch
---

In the state `shadow` on Linux and macOS, a `git.worktree_create` that removed a stale registration of its leaf runs the `rev-parse --show-toplevel` pair before the add.
