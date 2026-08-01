---
default: patch
---

`-serve` now creates a missing socket directory (mode `0700`) instead of refusing to start, and waits until the daemon is actually accepting before returning, so a client that connects immediately after launching it no longer races an unopened socket.
