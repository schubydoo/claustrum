---
default: patch
---

`git.worktree_create` looks for a stale registration in the common git directory of `baseRepo`, as `5fd08069` does. A create with a separate git directory, or in a submodule, that finds a stale registration of the new path now succeeds (round `tier3-02`, Linux, macOS and Windows VMs, the submodule on a Linux VM). On Windows the step now keeps a locked entry and keeps two stale entries (round `tier3-05`, Windows VM).
