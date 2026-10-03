---
default: patch
---

On Linux and macOS the daemon now records and marks children on every socket shape and binds its socket after the reap of its start. On every system it sends no signal to a process whose `id` a new spawn takes. A session supersede ends the old process before the new one starts. The `-serve` launcher waits 12 s for the daemon, and a signal during a start no longer ends the start. `process.killAndWait` and a session supersede send the group `SIGKILL` only when the grace runs out.
