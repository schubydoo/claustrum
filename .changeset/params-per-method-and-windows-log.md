---
default: patch
---

A method now ignores a member that another method of its namespace reads, whatever its type, and `plugins.prune` with no `params` member answers `-32602`.
