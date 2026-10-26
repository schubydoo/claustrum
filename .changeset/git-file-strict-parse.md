---
default: patch
---

The walk that finds the git directory of a folder now reads a `.git` file with a strict parse. The file must start with `gitdir: ` with one blank. Trailing CRs and LFs go, other trailing white space stays, and then a NUL ends the path. A path that the NUL makes empty names the folder itself. Any other `.git` file means no repository. `git.worktree_remove` now deletes what the reference deletes on Linux for a NUL or a blank in that file.
