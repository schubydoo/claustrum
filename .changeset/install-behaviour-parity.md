---
default: patch
---

`-install` no longer executes `ldd` when the musl loader marker already answers the question, and it consumes the local `-cli-zst` blob once decompression succeeds rather than only on a fully successful install — both matching the reference.
