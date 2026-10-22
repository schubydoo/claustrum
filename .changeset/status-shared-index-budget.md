---
default: patch
---

`git.status` on a linked worktree now follows `5fd08069` for `sharedindex.*` files (rounds `tier3-02`, `tier3-04`, `tier3-20` and `tier3-23`, Linux VM). A file with 40 hex digits is bound at 1 GiB and copied. The names `x` and `ab` get neither step. A sum of the `index` and these files over 2 GiB is refused. A 40-hex name that is a symlink passes and is not copied. A hex FIFO is skipped too (claustrum's own, not measured). The same steps for 64 hex digits are claustrum's own, not measured.
