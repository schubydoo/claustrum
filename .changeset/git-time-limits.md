---
default: minor
---

The git calls of `git.list_branches`, `git.info`, `git.status` and `git.worktree_create` now stop at the time limits of `5fd08069`, for example 2 minutes for one call. `-git-timeout` and its `claustrum.conf` key are ignored now, and either one logs one warning on `-serve`.
