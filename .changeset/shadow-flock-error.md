---
default: minor
---

Git requests go on after a `flock` error of `ENOLCK` on Linux. The `git-shadow` sweep then removes such a stale folder only past 7 days.
