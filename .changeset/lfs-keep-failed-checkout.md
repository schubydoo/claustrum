---
default: patch
---

On Linux and macOS a failed or stopped checkout of `git.worktree_create` in the state `shadow` keeps its downloaded Git LFS objects by a hard link on the same file system when the disk has the space.
