---
default: patch
---

On Linux and macOS a remove of an existing worktree deletes nothing behind a linked `.git/worktrees`, and the remove of a gone one there deletes its branch. A create refuses a linked or unlistable `.git/worktrees`.
