---
default: minor
---

On Linux and macOS, git.worktree_create refuses a new worktree whose .git file is gone after the checkout, with unsafe_path. The worktree folder goes, and the registration and the branch stay.
