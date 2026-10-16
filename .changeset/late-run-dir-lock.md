---
default: minor
---

On Linux and macOS a daemon that started while a process that is not a daemon held `daemon.lock` now tries the lock again every 30 s. A try that gets it writes the owner record and logs one line, as on `5fd08069`.
