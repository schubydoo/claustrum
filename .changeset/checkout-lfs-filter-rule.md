---
default: patch
---

With a settings copy `git.worktree_create` now follows the git-lfs rule of `5fd08069`. A repository-only `lfs` filter with the `git lfs install` lines gets those lines as checkout items, and `git-lfs smudge --skip %f` passes. There a git-lfs program key of the repository no longer refuses the request, and a failed checkout text that holds `smudge filter lfs failed` gets a note about ssh, credential and proxy settings.
