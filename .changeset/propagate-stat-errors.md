---
default: patch
---

`files.stat`, `files.read` and `files.validate` now report a stat failure other than "does not exist" instead of the missing-path result — e.g. a path component that is a file.
