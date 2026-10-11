---
default: patch
---

The git calls of the git methods carry the `-c` option lists of `5fd08069` and its variables of a request with `"shadowDirOff":true`. As on `5fd08069`, `git.status` does not read replace objects, and the failure texts of `git.worktree_create` have no graft-file hint of git. A create with a tree path over 260 characters now succeeds on Windows, and a create writes absolute paths with `worktree.useRelativePaths` in the user config.
