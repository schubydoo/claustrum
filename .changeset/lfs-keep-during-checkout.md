---
default: patch
---

On Linux and macOS in `shadow`, `git.worktree_create` keeps each Git LFS object in `<git dir>/lfs/objects` within about 1 s of its download, while the checkout still runs.
