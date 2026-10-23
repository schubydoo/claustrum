---
default: patch
---

Most git calls now keep only absolute entries of the daemon's `PATH`, `GIT_EXEC_PATH` and loader variables, and drop a relative `GIT_SSH`. A loader line on stderr no longer changes `branch` or fails a create.
