---
default: patch
---

On Linux and macOS, git.worktree_remove of a gone worktree answers success with the branch kept in four more cases, as the reference does. The other git methods answer "no repository" in two of them.
