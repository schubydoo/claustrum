---
default: patch
---

`-install` now matches the reference: its temp sweep spares entries under 10 minutes old, the prune ignores them, the `ldd` probe stops at 5 s, `-cli-url` creates the cli-dir before downloading, and checksums compare case-sensitively. `-libc-probe-timeout` is deprecated.
