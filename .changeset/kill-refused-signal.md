---
default: patch
---

On Linux and macOS, `process.kill` answers `-32603` with the system error, such as `operation not permitted`, when the system refuses the signal, and that child gets no `killedBy` at its later exit. `process.killAndWait` logs a refused first signal and a refused group kill, and a child that still runs 7 s after its `SIGKILL`.
