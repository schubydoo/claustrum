---
default: patch
---

On Linux a starting daemon keeps the record of a child it cannot inspect and sends it no signal. On Linux and macOS a daemon without the run-folder lock reaps and records nothing, a failed socket chmod ends the start, and `process.killAndWait` sends the group `SIGKILL` when the grace runs out.
