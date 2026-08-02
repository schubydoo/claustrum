---
default: patch
---

`process.killAndWait` clamps `timeoutMs` at 30000 ms instead of 600000 ms, so a caller asking for a longer grace against a signal-ignoring child now gets its exit frame at 30 s, matching the reference.
