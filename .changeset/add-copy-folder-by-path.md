---
default: patch
---

On Windows `git.status` and `git.worktree_create` make the short-lived folders of a request by their full path, not through a root. Cell X3 of round `task077-06` (Windows VM) denies new folders in the daemon's folder, with the backup and restore privileges on. There a create after a first `git.info` succeeds with the reply of `5fd08069`.
