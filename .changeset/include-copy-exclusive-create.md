---
default: patch
---

`git.worktree_create` no longer writes over a file, or through a link, that the checkout made when it copies ignored files into the new worktree.
