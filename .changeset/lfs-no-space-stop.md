---
default: minor
---

In the state `shadow` on Linux and macOS, `git.worktree_create` stops a checkout when a Git LFS download leaves under a tenth of the free space that its disk had at the start, and answers `worktree_lfs_no_space`.
