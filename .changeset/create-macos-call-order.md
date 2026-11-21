---
default: patch
---

On macOS git.worktree_create now asks git about the repository before the read of its config after a hold that ends at its add, and an attach add runs one more config listing before its add, as the reference does.
