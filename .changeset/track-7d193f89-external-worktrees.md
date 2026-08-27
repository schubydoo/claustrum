---
default: minor
---

Track reference `claude-ssh` build `7d193f89`: `git.worktree_create`/`git.worktree_remove` accept a `worktreeRoot` to place the session worktree outside the repository (the `external_root` capability), guarded by an ownership/writability check, two-level containment, an empty-directory requirement, and a `.claude-managed-worktrees` marker; a `baseRepo` that itself sits under a managed-worktrees tree is refused.
