---
default: patch
---

`claustrum -stop` now unlinks the socket path on every exit path — including when the dial fails and no daemon was ever reached — matching the reference, so a stale socket left behind by a killed daemon no longer stops the next `-serve` from binding.
