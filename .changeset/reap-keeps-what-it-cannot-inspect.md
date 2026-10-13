---
default: patch
---

On Linux a starting daemon keeps the record of a child that it cannot inspect, or drops it when the child is not its own to read, and sends no signal in both cases. On Linux and macOS a daemon without the run folder lock reaps and records nothing, and a failed socket chmod ends the start.
