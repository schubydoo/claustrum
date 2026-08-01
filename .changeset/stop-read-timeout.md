---
default: patch
---

`-stop` now gives up waiting for the daemon's reply after 2 seconds like the reference, instead of blocking forever when the daemon accepts the connection but never answers.
