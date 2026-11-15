---
default: patch
---

On Linux and macOS, after a link to a copy replaces `.git` right after the git-dir answer, the `--git-dir=` calls of `git.worktree_create` name a git-shadow folder of the new git directory.
