---
default: patch
---

On Linux and macOS the `status` call of git.status now has its `lfs.storage` item right after `--work-tree`, as the reference does on Linux and Windows.
