---
default: patch
---

On Windows a remove with a junction between baseRepo and the worktree now answers success, deletes the branch and keeps the folder. A junction moved during a create or remove can add or delete a same-named branch in a second repository, and D19 is retired.
