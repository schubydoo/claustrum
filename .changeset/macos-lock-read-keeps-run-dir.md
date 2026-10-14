---
default: patch
---

On macOS the host cleaner keeps a run folder and does not retire its daemon when the 15 s deadline ends the `lsof` run of the lock read or of the busy read. Before, it removed the folder and sent SIGTERM to the daemon.
