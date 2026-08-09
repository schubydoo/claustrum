---
default: patch
---

`process.kill` and `process.killAndWait` now deliver a non-KILL signal to the spawned process alone, not its whole process group, so a graceful stop no longer kills the background jobs the command started, while `escalate:true` still sweeps the group with SIGKILL as the reference does.
