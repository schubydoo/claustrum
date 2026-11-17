---
default: patch
---

git.status with no git on the PATH of the daemon now answers `isRepo: false` for a linked worktree. A git that PATH finds later in a session folder gets its passing-over line.
