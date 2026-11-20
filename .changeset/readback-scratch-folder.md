---
default: patch
---

The long-running hash-object of the content check of git.worktree_create now reads each object from its own file in a .readback-scratch-<n> folder, removed after its answer.
