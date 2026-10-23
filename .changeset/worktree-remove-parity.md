---
default: patch
---

`git.worktree_remove` now deletes the worktree and its registration itself, as the reference does, and refuses a link or a file at the worktree path, a locked worktree (D22) and a junction on the path on Windows (D19). On Windows a create whose path crosses a junction between `baseRepo` and the leaf answers `failed to create parent directory: <path> is not a directory` and creates nothing.
