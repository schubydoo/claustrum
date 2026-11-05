---
default: patch
---

On Windows git.worktree_create and git.worktree_remove drop a name before .. in baseRepo by text, a junction, a missing name or a file, as the reference does. git.list_branches does the same in path, and lists the branches of a path through a junction.
