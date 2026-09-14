---
default: patch
---

claustrum now sets the daemon's RLIMIT_NOFILE soft limit to min(hard, 65536) at serve startup on Unix, so process.spawn children inherit a high open-file limit, matching reference build `19f30c46`.
