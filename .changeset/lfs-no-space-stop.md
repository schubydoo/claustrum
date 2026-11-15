---
default: minor
---

On Linux and macOS `git.worktree_create` stops a checkout whose Git LFS download fills the disk of its private store, and answers `worktree_lfs_no_space`.
