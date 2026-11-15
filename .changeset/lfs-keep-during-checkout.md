---
default: patch
---

On Linux in `shadow`, `git.worktree_create` can keep a downloaded Git LFS object in `<git dir>/lfs/objects` while the checkout still runs.
