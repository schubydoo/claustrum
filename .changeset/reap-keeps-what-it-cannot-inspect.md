---
default: patch
---

On Linux a starting daemon keeps the record of a child whose stat it cannot read and sends no signal. On Linux and macOS a daemon without the run folder lock reaps and records nothing, and a failed socket chmod ends the start.
