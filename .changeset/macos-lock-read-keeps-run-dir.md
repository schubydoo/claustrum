---
default: patch
---

On macOS the host cleaner keeps a run folder when the deadline ends the `lsof` run for its `daemon.lock`, as on `5fd08069` (cells L1 and L2, macOS VM). Before, it removed the folder, also with a live holder.
