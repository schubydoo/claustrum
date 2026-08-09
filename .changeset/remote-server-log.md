---
default: patch
---

`-serve` now writes its output to `remote-server.log` beside the socket (mode 0600) instead of the launcher's stdout and stderr, and the file survives a graceful shutdown.
