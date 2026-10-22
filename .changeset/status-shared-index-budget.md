---
default: patch
---

`git.status` on a linked worktree now follows `5fd08069` for `sharedindex.*` files (rounds `tier3-02` and `tier3-04`, Linux VM). A file with 40 hex digits is bound at 1 GiB and copied. The names `x` and `ab` get neither step. A sum of these files over 2 GiB is refused. The same steps for 64 hex digits, and the `index` in the sum, are claustrum's own, not measured.
