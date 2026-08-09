---
default: patch
---

`claustrum -stop` now unlinks the socket path on every exit path — including when the dial fails and no daemon was ever reached — matching the reference, so a stale socket no longer blocks the next `-serve` from binding.
