---
default: patch
---

Exited processes are now dropped from the process table 15 minutes after they finish, freeing their replay buffers, so `process.reattach` on a long-finished id reports `found:false` as the reference does.
