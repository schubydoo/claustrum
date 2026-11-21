---
default: patch
---

On Windows the git methods now name the first folder below the share root that fails the share look, as the reference does. A git.worktree_create or git.worktree_remove of a subfolder of a repository on a share now gets the answers of the repository folder.
