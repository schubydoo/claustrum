---
default: patch
---

`-stop` now gives up waiting for the daemon's reply after 2 seconds like the reference instead of blocking forever, and no longer echoes that reply frame to stdout.
