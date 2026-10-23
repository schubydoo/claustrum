---
default: patch
---

On Windows `git.worktree_create` now stages the new registration and moves it into place as the reference does, and tries a held rename again for 2 s. Without `"shadowDirOff":true` the git methods refuse a junction or a directory link at `.git`. With it, a create refuses one at `.git\worktrees`.
