---
default: patch
---

The repository owner probe now passes `-c core.fsmonitor=` with an empty value instead of `core.fsmonitor=false`, as the reference does on Windows. Every hardened git call passes the same empty value.
