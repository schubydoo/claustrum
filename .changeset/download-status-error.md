---
default: patch
---

A non-200 CLI download now reports `download failed with status <code>` like the reference, instead of a differently-worded message that also leaked the download URL into the `__INSTALL_RESULT__` line.
