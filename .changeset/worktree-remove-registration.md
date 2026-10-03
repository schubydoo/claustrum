---
default: patch
---

`git.worktree_remove` now deletes or keeps the same registration as `89cb6289` with a daemon `GIT_DIR` and `GIT_COMMON_DIR`, for a missing `baseRepo`, and through a Windows `subst` drive or junction. It refuses a worktree locked in the `.git` folder of `baseRepo` (D22).
