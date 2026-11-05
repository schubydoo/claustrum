---
default: patch
---

On Windows git.worktree_create makes a worktree through a junction at baseRepo or above it, and git.worktree_remove removes a live worktree there, as the reference does.
