---
default: minor
---

On Linux and macOS `git.worktree_create` stops a checkout when a Git LFS download leaves under a tenth of the free space of its disk, and answers `worktree_lfs_no_space`.
