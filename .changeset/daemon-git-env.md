---
default: patch
---

The git methods now drop the daemon's `GIT_CONFIG` and `GIT_CONFIG_PARAMETERS` from repository git calls and refuse a malformed `GIT_CONFIG_COUNT`. `git.status` answers `isRepo:false` for a `baseRepo` that does not resolve, such as `<dir>/missing/..`, and `git.list_branches` does so for such a `path`.
