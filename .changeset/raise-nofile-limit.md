---
default: patch
---

On Unix, claustrum now sets the RLIMIT_NOFILE soft limit of the daemon to min(hard, 65536) at serve startup. As a result, process.spawn children inherit a high open-file limit. This matches reference build `19f30c46`.
