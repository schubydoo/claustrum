---
default: patch
---

On Windows the daemon now tries a held rename again for 2 s, for a git file copy and for the `git.worktree_create` staging folder. A staging rename that still fails fails the create and rolls it back, as `5fd08069` does.
