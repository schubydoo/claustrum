---
default: patch
---

process.stdin now rejects a write with `-32002 stdin backpressure: queue full` once the per-process async stdin queue is full, and the cap is 16 MiB (was 8 MiB), matching the reference `4534d86` — previously a producer that outran a non-reading child blocked the request indefinitely.
