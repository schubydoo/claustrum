---
default: patch
---

`process.kill` and `process.killAndWait` now deliver a non-KILL signal to the spawned process alone rather than its whole process group, so a graceful stop no longer takes down background jobs the command started, while `escalate:true` still sweeps the group with SIGKILL as the reference does.
