---
default: minor
---

On Linux, git.worktree_create now answers `git_lfs_not_found` when the git of the login shell's PATH runs and the checkout fails for a git-lfs on that PATH but neither beside that git nor on the daemon's PATH, and its stderr holds `git-lfs: not found` (a host with dash as /bin/sh).
