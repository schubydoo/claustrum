---
default: minor
---

`git.worktree_create` now copies `.worktreeinclude` and `.claude/` files the way reference build `f6010b97` does, including its directory scan and its batching of long git calls.
