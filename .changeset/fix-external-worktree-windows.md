---
default: patch
---

On Windows, gate the external worktree-location capability off to match the reference: `git.worktree_create`/`worktree_remove` refuse any `worktreeRoot` with "a custom worktree location is not supported on Windows hosts yet", and `server.capabilities` drops `git.worktree.external_root` from its features on Windows.
