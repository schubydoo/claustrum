---
default: patch
---

The socket golden now pins the four remaining frames that echo a tilde-expanded path — `files.read`, `files.extract_tar`, `git.worktree_create`'s error text and `process.spawn`'s `cwd` error — so a regression in the expanded spelling can no longer pass unnoticed on any of the eight frames that carry it.
