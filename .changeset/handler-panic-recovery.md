---
default: minor
---

The daemon now recovers from a panic in any request handler instead of crashing, replying `-32603 "recovered panic: <v>"` and staying up for other connections, so a bug in one handler can no longer orphan managed child processes or leave a stale socket behind.
