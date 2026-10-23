---
default: patch
---

The host cleaner no longer ends stranded daemons, and it keeps a run folder and sends no signal when it cannot read the lock state, through `/proc/locks` on Linux or `lsof` on macOS. Its log prefix changes from `[process.HostClean]` to `[hostclean]`.
