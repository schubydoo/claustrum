---
default: patch
---

`git.worktree_remove` now refuses, as the reference does, a `worktreeRoot` such as `<repo>/.claude` or one that does not exist, and deletes nothing.
