---
default: patch
---

On macOS git.worktree_create of a branch now asks git about the repository before the read of its config after a hold that ends at its add. On macOS an attach add runs one more config listing before its add, as the reference does after a git.info (claustrum's own with no git.info first). Under a live hold of another request, a create with no shadowDirOff now checks out the commit id. It then runs the settle step, as the reference does on macOS for a hold that ends after the read-tree call (claustrum's own on Linux and Windows).
