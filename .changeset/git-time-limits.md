---
default: minor
---

The git calls of `git.list_branches`, `git.info`, `git.status` and `git.worktree_create` now stop at the reference time limits, most after 2 minutes, and wait for git's output as the reference does. `-git-timeout` and its `claustrum.conf` key are ignored, and either one logs one warning.
