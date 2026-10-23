---
default: patch
---

`git.worktree_create` now creates a long tree path on Windows, gives a new worktree absolute paths where the user's git config asks for relative ones, and its failure texts no longer start with git's graft-file hint.
