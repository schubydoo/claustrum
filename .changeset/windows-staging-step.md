---
default: patch
---

On Windows `git.worktree_create` now checks the staged registration and copies its whole tree, as `5fd08069` does. It takes the next free name and makes `config.worktree` and the index as new files.
