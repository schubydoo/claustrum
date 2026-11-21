---
default: patch
---

With no git on the PATH of the daemon, git.list_branches now answers isRepo false with no branches in place of the git cannot run error, as the reference does on Linux and macOS (on Windows this is claustrum's own).
