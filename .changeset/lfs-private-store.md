---
default: minor
---

On Linux and macOS the checkout of `git.worktree_create` in the state `shadow` uses a private Git LFS store in its `.checkout-<n>` folder. Local objects come from the store of the repository, and downloaded objects go back to it when its disk has 1 GiB free beyond the object. Then stale `.claude-` temp files in the shard of a kept object go. `server.capabilities` lists `git.worktree_create.own_lfs_store` there.
