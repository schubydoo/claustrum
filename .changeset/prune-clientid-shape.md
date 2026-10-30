---
default: patch
---

`plugins.prune` now gives the skip answer when the clientId folder of the socket is not 8 lowercase hexadecimal characters. It then deletes nothing, and `root` is `""` and `minAgeDays` is `0`. The 8-hex rule is claustrum's own, not measured.
