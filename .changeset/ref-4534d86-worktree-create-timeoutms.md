---
default: minor
---

`git.worktree_create` now accepts a caller-supplied `timeoutMs` (milliseconds) that bounds the worktree add and checkout, answers `errorCode:"timeout"` when it fires, and advertises the `git.worktree_create.timeoutMs` feature, matching reference build 4534d86 (absent or 0 arms no deadline, so the default reply stays byte-identical).
