---
default: patch
---

The daemon's run-dir-lock eviction ladder (SIGTERM/SIGKILL plus the `previous owner of <rundir>: terminated|killed|survivor` summary), held-by-`--stop`, flock-unsupported, and orphan-exit log messages now match the reference `4534d86` wording (measured at runtime); this is off-wire, and claustrum keeps its own level tag and startup banner, and the holder-refusal and changed-hands lines keep claustrum's wording.
