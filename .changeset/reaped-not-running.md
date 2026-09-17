---
default: patch
---

A reaped process still inside its exit drain now reports `running:false` on `process.reattach`. A `process.stdin` write of fresh bytes answers `-32602 Process not running` there too, so `applied` and `stdinApplied` stop counting bytes that the closed pipe discards. An offset gap still answers `-32003` and a wholly duplicate write still succeeds. Reference build `90fca6e6` tests running AND not reaped on both paths, where it tested running alone before.
