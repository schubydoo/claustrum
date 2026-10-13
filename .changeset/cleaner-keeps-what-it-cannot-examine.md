---
default: patch
---

On Linux the host cleaner keeps a run folder when `/proc/locks` cannot be read, and it sends no signal to a stopped daemon, as `5fd08069` does.
