---
default: patch
---

With no git on the daemon PATH, git.info, git.list_branches and, on Linux and macOS, git.status now make no git-shadow folder or lock file, as the reference does on Linux.
