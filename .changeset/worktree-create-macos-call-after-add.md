---
default: patch
---

On Linux and macOS in the state `shadow`, git.worktree_create asks git about the repository after the add. After the add of a commit it asks before the content check. Answers that name another git directory, or a "not a git repository" answer, fail the create and roll it back.
