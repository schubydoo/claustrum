---
default: patch
---

On Linux a failed checkout of `git.worktree_create` now kills the process group of its git before the reap, which ends a member that still runs.
