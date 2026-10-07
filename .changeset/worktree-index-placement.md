---
default: patch
---

On Linux and macOS, `git.worktree_create` now writes the index of a new worktree as a new file, and a failed placement fails the create, as on `89cb6289`.
