---
default: patch
---

The Git LFS seed of `git.worktree_create` checks the sha256 of an object with a second hard link, and leaves out an object that fails.
