---
default: patch
---

With `worktreeRoot`, `git.worktree_create` and `git.worktree_remove` refuse reserved names, a repository in the per-repository folder, its git directory, its listed worktrees, long link chains and links to `/`, and delete nothing. Create also refuses session folders.
