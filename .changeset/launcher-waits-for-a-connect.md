---
default: patch
---

On Linux the `-serve` launcher no longer reports a failed take-over when the new socket reuses the inode number of the old one. It now exits 0 only after a connect works, as `5fd08069` does on Linux and macOS. On Linux, with a folder that holds files at the socket path, the launcher now waits the 12 s bound and exits 1, as `5fd08069` does.
