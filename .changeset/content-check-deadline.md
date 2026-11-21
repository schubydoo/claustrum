---
default: patch
---

The content check of git.worktree_create now stops at the `timeoutMs` deadline with no more reads, and its log lines give the broken pipe of a batch that ends after one answer and count the trees of a read per folder depth.
