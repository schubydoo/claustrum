---
default: patch
---

On Linux and macOS a failed `git.worktree_create` add removes a file or a link put in place of the leaf, and a leaf replaced before the add gets the `has no .git file` refusal.
