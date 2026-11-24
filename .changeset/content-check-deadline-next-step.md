---
default: patch
---

The git.worktree_create content check now stops at the next step that sees the `timeoutMs` deadline. After a stop at the deadline, the copy step runs its config listing also with only `worktrees` in `.claude/`. At the deadline the check kills its running `cat-file --batch` helper and its commit, tree and file `hash-object` helpers. On Linux and macOS the reply waits at most 2 s for the stdout of a killed file hash. On Windows the check waits at most 3 s for the killed helpers to exit, before the rollback.
