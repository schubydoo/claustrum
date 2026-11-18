---
default: patch
---

On Windows, the git calls of `git.status` now place the `lfs.storage` item after `--work-tree` and resolve a junction in `path` for the top-level check.
