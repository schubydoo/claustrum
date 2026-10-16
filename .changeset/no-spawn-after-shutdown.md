---
default: patch
---

A `process.spawn` child whose start returns after a shutdown began is ended at once and gets no reply, also with `-keep-children`. A shutdown leaves no temp record file.
