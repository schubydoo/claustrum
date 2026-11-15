---
default: patch
---

On Linux and macOS, with a settings copy, the reply of a failed Git LFS checkout of `git.worktree_create` ends before the `Errors logged to` line of git-lfs, and Windows keeps the line. On Linux and macOS a missing object on a git-lfs server gets the note of a missing file.
