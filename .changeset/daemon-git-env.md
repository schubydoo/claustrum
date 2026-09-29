---
default: patch
---

The git methods now drop the daemon's `GIT_CONFIG` and `GIT_CONFIG_PARAMETERS` from repository calls. They keep its `GIT_CONFIG_COUNT` pairs, filter its `GIT_ALLOW_PROTOCOL`, and refuse a malformed count. `git.status` answers `isRepo:false` for a `baseRepo` that does not resolve, such as `<dir>/missing/..`. `git.list_branches` does so for such a `path` and for one inside a managed worktrees directory.
