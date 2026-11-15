---
default: patch
---

On Linux and macOS `git.worktree_create` succeeds when the folder of the daemon has mode 0300, and its `.s<n>` folder stays.
