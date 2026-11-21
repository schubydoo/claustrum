---
default: minor
---

On Linux and macOS, with no git on the daemon's PATH, the git methods and a gitRead spawn now use a git that the login shell's PATH holds as an install, and git.worktree_create answers `git_not_found` when there is no git it can use.
