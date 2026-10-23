---
default: minor
---

On Linux and macOS a daemon that started while another process held `daemon.lock` now tries the lock again every 30 s, and writes its owner record and logs one line when it gets the lock.
