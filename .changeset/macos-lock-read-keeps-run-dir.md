---
default: patch
---

On macOS the host cleaner keeps a run folder when its 15 s deadline ends the `lsof` run for the `daemon.lock`. Before, it removed the folder, also with a live holder.
