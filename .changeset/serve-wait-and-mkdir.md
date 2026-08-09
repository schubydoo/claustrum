---
default: patch
---

`-serve` now creates a missing socket directory (mode `0700`) instead of refusing to start, and waits until the daemon is accepting before returning, so a client connecting immediately after launch no longer races an unopened socket.
