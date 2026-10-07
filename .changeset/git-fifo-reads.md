---
default: patch
---

`git.worktree_create` and `git.worktree_remove` no longer wait on a FIFO in place of a `.git` file, a `commondir` file or a `gitdir` record. They answer as `89cb6289` answers, with one exception: the remove of a locked worktree whose `gitdir` record is a FIFO is refused (D22).
