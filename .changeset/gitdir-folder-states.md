---
default: patch
---

On Linux and macOS, an existing worktree's remove deletes nothing behind a linked `.git/worktrees`, and a gone one there loses its branch. A create refuses a linked or unlistable one, and goes on when a real copy replaces `.git` mid-add.
