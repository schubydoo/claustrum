---
default: patch
---

Every hardened git call and the repository owner probe now pass `-c core.fsmonitor=` with an empty value, as reference build c8e2f083 does, instead of `core.fsmonitor=false`.
