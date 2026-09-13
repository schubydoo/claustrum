---
default: minor
---

The startup host cleaner now runs on darwin. It was linux-only before. On darwin it reads processes with sysctl kern.proc, and argv and environment with KERN_PROCARGS2. It reads open files, sockets and the run-dir lock with lsof, because darwin has no /proc. The judge, kill and tidy decisions are shared with linux. This matches reference build `19f30c46` and was validated on a macOS VM.
