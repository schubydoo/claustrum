---
default: patch
---

The git methods clean the `PATH`, `GIT_EXEC_PATH`, `GIT_SSH` and loader variables of the daemon for most git calls, as 5fd08069 does. The start test of the settings copy gets no `GIT_` name of the daemon. `git.info` and `git.worktree_create` read git answers from stdout only, so a loader line on stderr no longer changes `branch` or fails a create.
