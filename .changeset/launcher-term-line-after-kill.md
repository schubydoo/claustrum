---
default: patch
---

On Linux the end of an exited launcher logs its SIGTERM line after its group SIGKILL and its refusal line, and none after a shutdown in the wait, as the reference does.
