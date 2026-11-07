---
default: patch
---

On Linux and macOS the end of a child removes its record only while the file still holds the `start` that the daemon wrote.
