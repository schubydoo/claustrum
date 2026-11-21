---
default: patch
---

On Windows git.worktree_create now succeeds with a filter that only the repository defines with = in its name and `smudge = cat`, as the reference does.
