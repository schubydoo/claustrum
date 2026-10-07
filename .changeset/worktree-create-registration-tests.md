---
default: patch
---

`git.worktree_create` refuses a new worktree whose `.git` file does not name a registration of the repository, as `89cb6289` does on Linux and macOS, and rolls nothing back.
