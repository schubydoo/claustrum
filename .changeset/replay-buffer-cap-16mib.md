---
default: patch
---

Cap the per-process replay buffer at 16 MiB instead of 50 MiB, so `process.reattach` reports the same `firstSeq` floor as the reference daemon.
