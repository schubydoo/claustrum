---
default: patch
---

`git.worktree_remove` with `worktreeRoot` refuses and deletes nothing when the configuration listing before its git-directory call fails for a reason other than a missing repository.
