---
default: patch
---

`git.status` and `git.list_branches` now recognize a bare repository and a `.git` directory as repositories, and `git.worktree_create` ignores a `sourceBranch` that is not a local branch instead of failing the request.
