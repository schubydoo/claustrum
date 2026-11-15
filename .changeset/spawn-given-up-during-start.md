---
default: patch
---

`process.spawn` refuses an id that a `process.kill` gave up during its start, and kills the child that it started.
