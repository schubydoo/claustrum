---
default: patch
---

On Linux and macOS process.kill KILL after the child ended, before its exit frame, kills the group at once, so a process of its group that holds output ends.
