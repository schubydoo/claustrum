---
default: patch
---

The shutdown now labels a child whose kill still waits as `shutdown` in the exit log line, as the reference does on Linux, and in `killedBy`. Before, the label stayed `client`.
