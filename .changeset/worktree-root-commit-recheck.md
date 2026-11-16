---
default: patch
---

In the state `shadow` on Linux and macOS, a `git.worktree_create` with a `worktreeRoot` and a commit id from `sourceBranch` asks git about the repository right after the add, and runs the three location calls again there.
