---
default: patch
---

git.worktree_create now matches the reference `4534d86` on a failed `git worktree add`: the error message is reported on a single line (git's stderr lines joined with a space, no longer a literal newline), and the pre-created leaf directory is rolled back so a retry at the same path succeeds, while a pre-existing branch is left intact.
