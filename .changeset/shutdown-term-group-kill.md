---
default: patch
---

On Linux and macOS plain stream-json children ended by the shutdown SIGTERM now get a group SIGKILL after their exit, as 87375cc7 and 13047a51 do.
