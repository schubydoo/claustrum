---
default: patch
---

`files.read` now refuses a FIFO, a device or a pipe with `-32602 files.read: not a regular file`, as `5fd08069` does. The null device still reads on Linux and macOS. `-files-read-regular-only` is deprecated and sets nothing.
