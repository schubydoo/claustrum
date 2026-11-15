---
default: patch
---

In the state `shadow` on Linux and macOS, a `git.worktree_create` that removed a stale registration of its leaf, or kept one for its `locked` file, runs the `rev-parse --show-toplevel` pair before the add, or before the locked refusal.
