---
default: patch
---

On macOS the host cleaner now stops reading a daemon after an `lsof` run that it gave up on, so its pass ends after one 17 s wait for that daemon instead of three.
