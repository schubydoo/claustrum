---
default: patch
---

On Windows, with `GIT_DIR` in the daemon environment, a git request with a settings copy now makes the git calls of a request with no `GIT_DIR` there.
