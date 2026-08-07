---
default: minor
---

The daemon now recovers from a panic in any request handler instead of crashing, replying with a `-32603` "internal panic" error and staying up for other connections, matching the reference daemon's per-request panic isolation.
