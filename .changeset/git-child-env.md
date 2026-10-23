---
default: patch
---

Most git calls now keep only the absolute entries of the daemon's `PATH`, `GIT_EXEC_PATH` and library-path variables, keep the entries of `LD_PRELOAD` and `LD_AUDIT` that are absolute or hold no separator, and drop a `GIT_SSH` that names a relative path. A loader line on stderr no longer changes `branch` or fails a create.
