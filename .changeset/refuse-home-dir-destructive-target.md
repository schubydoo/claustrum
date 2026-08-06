---
default: patch
---

`files.extract_tar` now refuses a `destDir` that is or contains the home directory, so a tilde or relative path can no longer reach the recursive delete it performs before unpacking — paths under home, such as `~/.claude/…`, are unaffected.
