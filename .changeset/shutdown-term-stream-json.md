---
default: minor
---

On Linux and macOS a shutdown sends SIGTERM to a stream-json child, and the group SIGKILL if it is still there after 1.5 s. The cleanup line says ended.
