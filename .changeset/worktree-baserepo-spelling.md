---
default: patch
---

With `worktreeRoot`, both worktree methods now refuse a relative, absent or `..` `baseRepo` (Linux, macOS). Both refuse a `baseRepo` that is not missing but does not resolve, like `<T>/a.txt/..` or a Windows junction mid-path. Without `worktreeRoot`, remove now refuses `<T>/missing/..` under a refused `GIT_CONFIG_COUNT` and keeps the worktree (Linux, macOS).
