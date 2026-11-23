---
default: patch
---

The git.worktree_create content check now tests the `timeoutMs` deadline at its next step after a git call that the deadline passed during. With only `worktrees` in `.claude/`, the copy step still runs its config listing. On Linux the deadline kills the running `cat-file --batch` and commit `hash-object`.
