---
default: patch
---

On Linux and macOS with git 2.56, git.info, git.status, git.list_branches, git.worktree_create and git.worktree_remove with worktreeRoot now answer a `.git` file to a missing git directory as with an older git.
