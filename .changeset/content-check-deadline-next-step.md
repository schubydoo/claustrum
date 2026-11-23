---
default: patch
---

The git.worktree_create content check now tests the `timeoutMs` deadline at its next step after a git call that the deadline passed during. With only `worktrees` in `.claude/`, the copy step still runs its config listing. At the deadline the check kills its running `cat-file --batch` and `hash-object` helpers. On Linux and macOS the reply waits at most 2 s for the stdout of a killed file hash. On Windows the rollback waits at most 3 s for the killed helpers to exit.
