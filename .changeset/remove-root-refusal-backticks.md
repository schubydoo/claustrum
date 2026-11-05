---
default: patch
---

git.worktree_create and git.worktree_remove put the path in backticks in their worktreeRoot shape and in-repo refusals, and the Windows remove refusal quotes it too.
