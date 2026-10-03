---
default: patch
---

`git.status` now finds the worktree through the entries of `baseRepo`, as `89cb6289` does. It adds the submodule entries, cuts long entries, and no longer waits on a FIFO in an entry.
