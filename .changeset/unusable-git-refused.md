---
default: minor
---

`git.worktree_create` refuses a `.git` that git cannot use with a new text in place of `not_a_repo`, takes a `commondir` of `./.`, and `server.capabilities` lists `git.worktree_create.unusable_git_refused`.
