---
default: minor
---

The process `exit` stream frame now carries `signal` (the terminating signal name, unix only) and `killedBy` (`client` for process.kill and process.killAndWait, `shutdown` for the shutdown sweep) after `exitCode`, matching reference build 4534d86, with both omitempty so a normal exit stays byte-identical.
