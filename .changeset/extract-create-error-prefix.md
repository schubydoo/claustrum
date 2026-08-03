---
default: patch
---

`files.extract_tar` now carries the reference's `create <entry>: ` prefix when an archive entry's target lands on an existing directory, so the error field reads `create ../sub: open <dest>: is a directory` instead of the bare Go error.
