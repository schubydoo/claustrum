---
default: patch
---

Without `worktreeRoot`, `git.worktree_remove` no longer deletes `worktreePath` when `baseRepo` holds no git repository. It answers an error instead, as reference build `f6010b97` does.
