---
default: patch
---

On Windows git.worktree_remove without worktreeRoot, on a folder with no repository whose listing passes, with no daemon GIT_DIR or an absolute one that names the `.git` folder of a repository, and no daemon GIT_COMMON_DIR, now runs the listing and the rev-parse one time, not two times, as the reference does in the state shadow.
