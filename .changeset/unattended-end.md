---
default: minor
---

process.spawn now ends a child with an unattendedSeconds above 0 once no connection holds it, as 87375cc7 does. Linux and macOS send SIGTERM and a group SIGKILL after at most 7 s, and Windows ends the job.
