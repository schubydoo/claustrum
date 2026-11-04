---
default: patch
---

On Windows `git.worktree_remove` of a gone worktree keeps the registration behind a junction or a directory link at the registrations folder, and still deletes the branch.
