---
default: patch
---

With git 2.56, git.info, git.status, git.list_branches, git.worktree_create and git.worktree_remove (with worktreeRoot on Linux and macOS, of a gone worktree on Windows) now answer a `.git` file to a missing git directory as with an older git.
