---
default: patch
---

On Windows the daemon tests its child jobs at start. After a failed test each process.spawn runs outside a job and answers outsideJob:true.
