---
default: patch
---

`-install` now reports `musl` when a musl loader of any architecture is present, and reports no libc at all on macOS and Windows, matching the reference daemon.
