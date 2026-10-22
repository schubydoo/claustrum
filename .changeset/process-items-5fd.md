---
default: patch
---

`process.stdin` now queues bytes after the child closed its input, until the queue is full. A request with no bytes at `offset` equal to `applied` answers `"duplicate": true`. A `process.reattach` from a new connection sends no live frame before its stored frames end. On Linux and macOS, `process.killAndWait` with `"signal": "KILL"` inside the exit drain, after the child ended and before its exit frame, kills its group at once.
