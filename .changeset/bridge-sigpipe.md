---
default: patch
---

On Linux and macOS, if the stdout of `-bridge` is closed and the daemon sends another frame, the bridge exits with code 0 and is not killed by SIGPIPE.
