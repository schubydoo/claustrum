---
default: patch
---

On Linux and macOS the `server.shutdown` reply now races the close of the connection, so a client can read EOF with no reply.
