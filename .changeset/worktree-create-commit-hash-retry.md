---
default: patch
---

A create whose content check cannot hash the commit now answers `worktree_verify_retry` with a try-again text, as 87375cc7 does on Windows for a git hash of the commit that exits 1.
