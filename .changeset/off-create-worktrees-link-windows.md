---
default: patch
---

On Windows a `git.worktree_create` with `"shadowDirOff":true` refuses a junction or a directory symbolic link at `.git\worktrees`. It removes the leaf it made and writes no registration behind the link, as `5fd08069` does in round `tier3-02` (Windows VM).
