---
default: patch
---

On Windows the five git methods that look at a share now name the first folder below the share root when every look on the share fails, as the reference does. A git.worktree_create or git.worktree_remove of a subfolder of a repository on a share now gets the answers of the repository folder.
