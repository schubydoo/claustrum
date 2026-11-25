---
default: patch
---

On Linux and macOS a shutdown no longer waits 1 s for a child whose SIGKILL failed with EPERM, as 87375cc7 does on Linux.
