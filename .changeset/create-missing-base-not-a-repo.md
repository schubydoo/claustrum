---
default: patch
---

git.worktree_create of a baseRepo that does not exist answers not_a_repo, also below a managed-worktrees marker or a .claude/worktrees folder, as the reference does on Linux and macOS.
