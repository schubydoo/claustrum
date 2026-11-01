---
default: minor
---

A git.worktree_create with a settings copy starts ssh in its checkout through a door that refuses an odd Git LFS host or path. A repository's own core.sshCommand is never used there.
