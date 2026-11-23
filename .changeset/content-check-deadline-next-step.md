---
default: patch
---

The git.worktree_create content check now stops at its next step after a git call that the `timeoutMs` deadline passed during. After a stop at the deadline, the copy step runs its config listing also with only `worktrees` in `.claude/`. At the deadline the check kills its running `cat-file --batch` and `hash-object` helpers. On Linux and macOS the reply waits at most 2 s for the stdout of a killed file hash. On Windows the rollback waits at most 3 s for the killed helpers to exit.
