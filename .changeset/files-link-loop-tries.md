---
default: patch
---

On Linux `files.stat`, `files.list` and `files.validate` try a path again after a link-loop error, up to 4 tries in all, about 10 ms apart.
