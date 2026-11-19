---
default: patch
---

On Windows, git.worktree_create in the state shadow now makes the worktree where the `.git` folder of baseRepo has the reparse tag IO_REPARSE_TAG_PROJFS.
