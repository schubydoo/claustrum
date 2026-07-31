---
default: patch
---

`git.status` now trims the whole porcelain output before splitting it, so the first change loses its leading space and every later one keeps it, matching the reference.
