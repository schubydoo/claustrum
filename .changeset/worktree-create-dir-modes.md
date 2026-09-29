---
default: patch
---

`git.worktree_create` now uses the reference directory modes and parent-step texts on Linux and macOS. It keeps an existing marker and refuses a worktree location inside a git checkout or a symlink loop.
