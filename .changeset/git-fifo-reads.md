---
default: patch
---

On Linux and macOS the daemon's own reads of a `.git` file, a `commondir` file and a `gitdir` record no longer wait on a FIFO. The one exception is the read of the `gitdir` record after `git worktree add`.
