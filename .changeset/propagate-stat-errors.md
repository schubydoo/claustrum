---
default: patch
---

`files.stat`, `files.read` and `files.validate` now report a stat failure that is not "does not exist", in place of the missing-path result. An example is a path component that is a file.
