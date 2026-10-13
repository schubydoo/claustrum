---
default: patch
---

On Linux and macOS a starting daemon keeps the record of a child it cannot inspect and sends no signal. Without the run folder lock it reaps and records nothing. A failed socket chmod ends the start.
