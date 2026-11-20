---
default: patch
---

On Linux and macOS, if a link replaced the git directory of baseRepo after the add, git.worktree_create with `"shadowDirOff":true` now refuses with `unsafe_path`.
