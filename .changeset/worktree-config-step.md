---
default: patch
---

A failed copy of `config.worktree` into a new worktree now fails `git.worktree_create` and rolls it back, as `5fd08069` does. On Linux and macOS the copy replaces a file or an empty folder at its place, and a linked `baseRepo` gives the file and the git options of its own entry. With `extensions.worktreeConfig` on, the git methods refuse a `<git dir>/config.worktree` that is not a regular file, cannot be opened or is over 8 MiB, before any add.
