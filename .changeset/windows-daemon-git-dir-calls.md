---
default: patch
---

On Windows, with an absolute `GIT_DIR` of a folder and no `GIT_COMMON_DIR` in the daemon environment, a git request with a settings copy now pins the git directory of its own trusted walk and sends the daemon `GIT_DIR` to none of its calls on the repository.
