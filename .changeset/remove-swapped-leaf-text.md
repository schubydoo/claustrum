---
default: patch
---

If another folder or a link replaces the worktree folder while git.worktree_remove deletes it, the reply is now "failed to remove worktree: `<leaf name>` was replaced while the daemon was using it". Nothing is deleted, as before.
