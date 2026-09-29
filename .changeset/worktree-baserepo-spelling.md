---
default: patch
---

With `worktreeRoot`, both worktree methods now refuse a relative, absent or `..` `baseRepo` (Linux, macOS). Both refuse a `baseRepo` that is not missing but does not resolve, like `<T>/a.txt/..`, or on Windows `<T>\missing\..` and a junction mid-path.
