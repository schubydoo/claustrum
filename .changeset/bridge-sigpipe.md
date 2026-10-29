---
default: patch
---

`-bridge` now exits with code 0 and is not killed by SIGPIPE when its stdout is closed and the daemon sends another frame, on Linux and macOS.
