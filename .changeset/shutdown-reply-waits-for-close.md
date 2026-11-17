---
default: patch
---

On Linux and macOS the `server.shutdown` reply now waits for the stop to start closing the connections, as on Windows.
