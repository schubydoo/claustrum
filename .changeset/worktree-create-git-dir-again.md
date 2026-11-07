---
default: patch
---

In the state `shadow`, when git names another git directory for `baseRepo`, git.worktree_create asks once more. A right answer goes on, and a wrong one fails the create and leaves no worktree folder.
