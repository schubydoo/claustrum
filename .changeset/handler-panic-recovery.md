---
default: patch
---

The daemon now recovers from a panic in any request handler instead of crashing, replying `-32603 "recovered panic: <v>"` and staying up for other connections, so a handler bug can no longer orphan managed child processes or leave a stale socket behind.
