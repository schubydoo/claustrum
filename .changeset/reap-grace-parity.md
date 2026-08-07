---
default: patch
---

`process.killAndWait` now waits 7 seconds for a SIGKILL'd process to be reaped instead of 5, matching the reference daemon, so a child that is reaped between those two points is reported as dead rather than still running.
