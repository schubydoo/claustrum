---
default: minor
---

The `-serve` daemon now writes its output to `remote-server.log` beside the socket, with mode 0600, in place of the launcher's stdout and stderr. The file stays after a graceful shutdown.
