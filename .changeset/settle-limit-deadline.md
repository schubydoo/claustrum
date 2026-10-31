---
default: patch
---

The timeoutMs deadline of git.worktree_create now starts before the first git call, not at the add, which moves the stop of the settle call.
