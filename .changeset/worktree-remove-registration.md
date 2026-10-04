---
default: patch
---

`git.worktree_remove` now deletes or keeps the same registration as `89cb6289` in more cases. It refuses a worktree locked in the `.git` folder of `baseRepo` (D22).
