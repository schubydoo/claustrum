---
default: patch
---

A method now ignores a member that another method of its namespace reads, whatever its type, and `plugins.prune` with no `params` member answers `-32602`, as `5fd08069` does. On Windows a second daemon on a live socket now truncates the held `remote-server.log`, as `5fd08069` does.
