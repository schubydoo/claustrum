---
default: minor
---

`git.worktree_create` in the state `shadow` runs `git diff-files` after the checkout, and when files read as changed the reply comes no sooner than 1.1 s after the checkout.
