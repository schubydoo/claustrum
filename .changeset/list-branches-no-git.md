---
default: patch
---

With no git on the daemon's PATH and none that it uses on the login shell's PATH, git.list_branches now answers isRepo false with no branches. This answer replaces the git cannot run error and follows the reference on Linux and macOS (on Windows it is claustrum's own).
