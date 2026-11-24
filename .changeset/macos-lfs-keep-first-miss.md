---
default: patch
---

On macOS the Git LFS keep of `git.worktree_create` now ends at the first object without room, as 4c111e86 and 87375cc7 do, its copy computes the sha256 in the same pass, a keep during the checkout runs until git exits, and a failed checkout keeps by copy under one 3 s bound for the whole pass.
