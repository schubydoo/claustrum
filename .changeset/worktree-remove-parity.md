---
default: patch
---

`git.worktree_remove` now deletes the worktree and its registration itself, as the reference does, and refuses a link or a file at the worktree path, a locked worktree (D22) and a junction on the path on Windows (D19). Its registration step, branch step and reply wait for a running add in the same git directory.
