---
default: patch
---

With git 2.56, a `.git` file whose git dir is gone gets the `5fd08069` frames on the git methods in the measured cells: "no repository" for a gone worktree entry, the error of the call about the repository for any other git dir, and the hooks refusal with `"shadowDirOff":true` (Linux VM). On Windows, a daemon `GIT_DIR=.git` on a linked worktree gets the refusal of a file of the git directory, and a daemon `GIT_DIR` file with no `gitdir:` line gets the refusal of a config, as on `5fd08069`. The `gitdir:` test that tells the two apart is claustrum's own choice.
