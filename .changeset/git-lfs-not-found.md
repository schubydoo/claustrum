---
default: minor
---

On Linux, git.worktree_create now answers `git_lfs_not_found` when the git of the login shell's PATH runs and the checkout fails for a git-lfs that only that PATH holds.
