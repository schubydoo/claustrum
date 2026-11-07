---
default: patch
---

The content check of git.worktree_create reads the commit and its trees through one long-running git cat-file, and its checked log line counts those that separate git runs read.
