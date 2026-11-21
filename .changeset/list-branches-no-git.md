---
default: patch
---

With no git on the PATH of the daemon, git.list_branches now answers isRepo false with no branches, as the reference does, in place of the git cannot run error, on Linux and macOS.
