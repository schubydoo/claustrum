---
default: patch
---

`process.stdin` now queues bytes after the child closed its input, until the queue is full. A write with no bytes at `offset` equal to `applied` answers `"duplicate": true`. A `process.reattach` from a new connection sends its stored frames before any live frame.
