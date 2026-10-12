---
default: minor
---

The git methods run git on the daemon's own copy of a repository's git settings, as `5fd08069` does. `git.info` reports the state in a new member `settingsControl`, and `"shadowDirOff":true` turns the copy off for one request. On Linux and macOS a create removes an old registration whose record spells the new worktree path as the request does. On Windows the git methods refuse a junction or a directory symbolic link at `.git`.
