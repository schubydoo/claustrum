---
default: patch
---

On macOS git.worktree_create now asks git about the repository before the read of its config after a hold that ends at its add. An attach add runs one more config listing before its add, as the reference does. Under a live hold of another request, a create with no shadowDirOff now checks out the commit id. It then runs the settle step, as the reference does on macOS (Linux and Windows not measured).
