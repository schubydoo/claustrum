---
default: patch
---

`git.status`, `git.worktree_create` and `git.worktree_remove` now follow the off switch at each git command, as `5fd08069` does. A Linux VM measured every cell, a Windows VM cells C0 to C2, C4 and C5, and a macOS VM cells C2, S1 and C0. A hold that starts moves the later commands to the git directory. After a hold ends, a later command uses the copy, and if no start test passed yet it runs one first. Under the hold the index of a create checkout goes to the temporary folder.
