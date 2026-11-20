---
default: patch
---

After a good checkout, git.worktree_create now keeps the downloaded Git LFS objects after the settle call, right before the reply, as the reference does on macOS.
