---
default: patch
---

git.worktree_create now makes the excludes read of a first request before a git directory trust refusal, as git.status does.
