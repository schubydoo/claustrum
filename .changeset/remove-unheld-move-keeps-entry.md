---
default: patch
---

On Windows a git.worktree_remove of a live worktree keeps both registrations when a junction in the path of baseRepo moves to another repository during the remove. That holds only with no held git directory and a git directory below baseRepo.
