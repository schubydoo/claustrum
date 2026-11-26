---
default: patch
---

On Windows a git.worktree_remove with no shadowDirOff and no daemon GIT_COMMON_DIR, whose `.git` file of baseRepo becomes a link during the delete, now keeps the branch of the second repository and refuses as 87375cc7 does.
