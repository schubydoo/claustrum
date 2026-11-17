---
default: patch
---

A git request no longer waits forever when a FIFO is in place of a folder of the daemon. It gets a "not a directory" error at once.
