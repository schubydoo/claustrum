---
default: patch
---

On Linux and macOS `git.worktree_create` refuses a new worktree whose `.git` file does not name a registration of the repository, as `89cb6289` does. It also refuses a registration that cannot be read or is of another worktree, and it rolls nothing back.
