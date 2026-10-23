---
default: patch
---

`files.read` now refuses a FIFO, a device or a pipe with `-32602 files.read: not a regular file`. The null device still reads on Linux and macOS. `-files-read-regular-only` and its `claustrum.conf` key are ignored and log one warning.
