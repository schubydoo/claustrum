---
default: patch
---

`git.worktree_create` refuses a group-writable `worktreeRoot` unless /etc/passwd and /etc/group show its group as the user's private group, as the reference does on Linux and macOS.
