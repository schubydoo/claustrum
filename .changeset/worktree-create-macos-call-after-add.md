---
default: patch
---

On macOS in the state `shadow`, git.worktree_create asks git about the repository after the add. Two answers that name another git directory fail the create and roll it back.
