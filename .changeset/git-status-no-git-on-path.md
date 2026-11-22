---
default: patch
---

With no git on the daemon's PATH and none that it uses on the login shell's PATH, git.status now answers `isRepo: false` for a linked worktree. A git that PATH finds later in a session folder gets its passing-over line.
