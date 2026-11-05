---
default: patch
---

On Linux and macOS, `git.worktree_create` with `timeoutMs` answers a failed smudge filter in the checkout at once while a child of git holds only its stdout.
