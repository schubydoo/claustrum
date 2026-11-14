---
default: minor
---

On Linux and macOS `git.worktree_create` answers `worktree_lfs_not_kept` when its `timeoutMs` expires during the checkout after long Git LFS copies, and the seed of those copies stops at half of `timeoutMs`.
